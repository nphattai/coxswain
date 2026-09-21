// cox-pi.ts - the Coxswain Pi extension. It wires real Pi lifecycle events to the runtime-independent core in
// cox-supervisor.ts (tested deterministically in cox-supervisor.test.ts). It owns NOTIFICATION only; Coxswain's
// append-only wake/event log, drain/ack, inbox, question/reply, and checkpoint files remain authoritative (DESIGN
// section 4). cox never mutates user-level Pi config: this extension is installed project-local by
// `cox workspace hooks --harness pi` and loaded explicitly at worker launch with `-e`.
//
// Roles (from env, set by the launch seam): a leader owns a generation-scoped `cox wake wait` child and delivers each
// durable wake as one followUp after establishing a successor. Both roles get automatic checkpoints: `cox checkpoint
// facts` before compaction and checkpoint injection on session start. Failure or absence stays visible and is never
// reported as an automatic checkpoint success.
import type { ExtensionAPI, ExtensionContext } from "@earendil-works/pi-coding-agent";
import { spawn, execFile, type ChildProcess } from "node:child_process";
import { readFileSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { Supervisor, TurnEndLatch } from "./cox-supervisor.ts";
import { coxArgs, resolveEpic } from "./cox-commands.ts";

const WAIT_MAX = "25m"; // ponytail: fixed wait window matching the terminal-plane `cox wake wait` default; tune in dogfood
const WAKE_NUDGE = "A Coxswain watcher wake arrived. Run `cox wake drain --epic $COX_EPIC`, handle each wake, then ack.";
const EPIC_MARKER = "cox-pi.epic"; // written next to the extension by `cox workspace hooks --harness pi --epic <dir>`
const ACTIVATED_MARKER = ".cox-pi.activated"; // written on session_start so cox can confirm Pi actually loaded this extension

// readEpicMarker reads the epic binding persisted next to this module, or "" when absent/unreadable.
function readEpicMarker(): string {
  try {
    return readFileSync(join(import.meta.dirname, EPIC_MARKER), "utf8");
  } catch {
    return "";
  }
}

// markActivated signals to cox that Pi loaded and ran this extension (the startup handshake). cox's dispatch waits for
// this marker and downgrades the effective card to pull/manual if it never appears (a load/version/API failure).
function markActivated(): void {
  try {
    writeFileSync(join(import.meta.dirname, ACTIVATED_MARKER), new Date().toISOString());
  } catch {
    /* best-effort: an unwritable extension dir leaves cox to downgrade, which is the safe direction */
  }
}

export default function (pi: ExtensionAPI): void {
  const story = process.env.COX_STORY ?? "";
  const cox = process.env.COX_BIN || "cox";
  const identity = story || "_leader";
  // Epic binding: COX_EPIC (set by the launch seam for a worker) wins, else the marker installed for a leader. Without
  // it, wake supervision stays idle rather than running against the wrong epic.
  const epic = resolveEpic(process.env.COX_EPIC, readEpicMarker());
  const isLeader = (process.env.COX_ROLE || (story && story !== "_leader" ? "worker" : "leader")) === "leader";

  let child: ChildProcess | null = null;
  const latch = new TurnEndLatch();

  function killChild(): void {
    if (child) {
      try {
        child.kill();
      } catch {
        /* already gone */
      }
      child = null;
    }
  }

  // startWaitChild spawns one `cox wake wait` child for a generation and routes its exit back into the supervisor:
  // exit 0 = a wake arrived, exit 3 = timeout (keep waiting), anything else = unexpected close (bounded retry).
  function startWaitChild(gen: number): boolean {
    if (!isLeader || !epic) return false;
    try {
      killChild();
      const c = spawn(cox, coxArgs.wakeWait(epic, WAIT_MAX), {
        stdio: ["ignore", "ignore", "ignore"],
      });
      child = c;
      c.on("exit", (code) => {
        if (c !== child) return; // a superseded child: its exit is stale, ignore it
        child = null;
        if (code === 0) sup.onWake(gen, WAKE_NUDGE);
        else if (code === 3) sup.onTimeout(gen);
        else sup.onUnexpectedClose(gen);
      });
      c.on("error", () => {
        if (c !== child) return;
        child = null;
        sup.onUnexpectedClose(gen);
      });
      return true;
    } catch {
      return false;
    }
  }

  const sup = new Supervisor({
    spawnWait: startWaitChild,
    deliver: (text) => {
      void pi.sendUserMessage(text, { deliverAs: "followUp" });
    },
    onExhausted: (reason) => {
      // Surfaced, never hidden: tell the operator via a followUp so the leader can recover the wake channel by hand.
      void pi.sendUserMessage(
        `Coxswain wake supervision stopped: ${reason}. Recover with \`cox wake drain --epic ${epic}\` and relaunch.`,
        { deliverAs: "followUp" },
      );
    },
  });

  // session_start: inject the saved checkpoint (recovery context) and, for a leader, activate a fresh generation and
  // begin waiting for wakes. A fresh generation makes any callback from a prior session stale (one live generation
  // across /new, /resume, /fork, reload, quit).
  pi.on("session_start", async (_event, ctx) => {
    markActivated(); // startup handshake: confirm to cox that Pi loaded this extension
    injectCheckpoint(ctx);
    if (isLeader) sup.sessionStart();
  });

  // session_before_compact: PERSIST a checkpoint from the current context BEFORE Pi summarizes it, for the bound
  // leader/worker identity. `cox hook precompact` writes <epic>/handoffs/<story>.md (whereas `cox checkpoint facts`
  // only prints). Failure is left visible; it is never reported as an automatic checkpoint success.
  pi.on("session_before_compact", async (_event, ctx) => {
    if (!epic) {
      ctx.ui?.notify?.("cox precompact skipped: no epic binding (COX_EPIC or installed marker)", "warn");
      return;
    }
    await run(cox, coxArgs.precompact(epic, identity, ctx.cwd)).catch((e: unknown) => {
      ctx.ui?.notify?.(`cox hook precompact failed before compaction: ${String(e)}`, "warn");
    });
  });

  // agent_settled: the turn-end health boundary. When wake supervision is unhealthy (leader with no live wait child),
  // schedule at most one bounded continuation to reopen the cycle; the latch prevents recursion.
  pi.on("agent_settled", async (_event, _ctx) => {
    if (!isLeader) return;
    const healthy = sup.liveGeneration() !== null;
    latch.onSettled(healthy, () => {
      sup.sessionStart(); // re-establish the wait child
      latch.consumed();
    });
  });

  // session_shutdown / process exit: retire the active generation and kill its child, so no stale callback mutates a
  // later session and no orphan wait child survives.
  pi.on("session_shutdown", async (_event, _ctx) => {
    sup.sessionShutdown();
    killChild();
  });
  process.once("exit", killChild);

  // injectCheckpoint runs `cox checkpoint inject` and, when it yields recovery context, delivers it as a followUp so the
  // session starts from the saved state. Absence/failure is visible (a notify), never a silent success.
  function injectCheckpoint(ctx: ExtensionContext): void {
    if (!epic) return; // no epic binding: nothing to inject
    // Inject through `cox hook session-start` so the current git HEAD (from the worktree) drives the CHECKPOINT STALE
    // freshness check; a stale checkpoint exits non-zero with the warning on stderr, which we surface.
    execFile(cox, coxArgs.sessionStart(epic, identity, ctx.cwd), (err, stdout, stderr) => {
      const text = (stdout || "").trim();
      if (err) {
        ctx.ui?.notify?.(`cox hook session-start: ${String(stderr || err).trim()}`, "warn");
        return;
      }
      if (text) void pi.sendUserMessage(text, { deliverAs: "followUp" });
    });
  }
}

// run executes a command and resolves on exit 0, rejecting otherwise, so a caller can surface failures.
function run(cmd: string, args: string[]): Promise<void> {
  return new Promise((resolve, reject) => {
    execFile(cmd, args, (err) => (err ? reject(err) : resolve()));
  });
}
