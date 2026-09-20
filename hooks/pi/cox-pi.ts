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
import { Supervisor, TurnEndLatch } from "./cox-supervisor.ts";

const WAIT_MAX = "25m"; // ponytail: fixed wait window matching the terminal-plane `cox wake wait` default; tune in dogfood
const WAKE_NUDGE = "A Coxswain watcher wake arrived. Run `cox wake drain --epic $COX_EPIC`, handle each wake, then ack.";

export default function (pi: ExtensionAPI): void {
  const epic = process.env.COX_EPIC ?? "";
  const story = process.env.COX_STORY ?? "";
  const cox = process.env.COX_BIN || "cox";
  const identity = story || "_leader";
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
      const c = spawn(cox, ["wake", "wait", "--max", WAIT_MAX, "--epic", epic], {
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
    injectCheckpoint(ctx);
    if (isLeader) sup.sessionStart();
  });

  // session_before_compact: write a checkpoint from the current context BEFORE Pi summarizes it, for the bound
  // leader/worker identity. Failure is left visible; it is never reported as an automatic checkpoint success.
  pi.on("session_before_compact", async (_event, ctx) => {
    await run(cox, ["checkpoint", "facts", "--epic", epic, "--story", identity]).catch((e: unknown) => {
      ctx.ui?.notify?.(`cox checkpoint facts failed before compaction: ${String(e)}`, "warn");
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
    execFile(cox, ["checkpoint", "inject", "--epic", epic, "--story", identity], (err, stdout) => {
      const text = (stdout || "").trim();
      if (err) {
        ctx.ui?.notify?.(`cox checkpoint inject failed: ${String(err)}`, "warn");
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
