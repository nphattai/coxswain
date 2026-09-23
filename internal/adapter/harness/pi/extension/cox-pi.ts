// cox-pi.ts - the Coxswain Pi extension. It wires real Pi lifecycle events to the runtime-independent core in
// cox-supervisor.ts (tested deterministically in cox-supervisor.test.ts). It owns NOTIFICATION only; Coxswain's
// append-only wake/event log, drain/ack, inbox, question/reply, and checkpoint files remain authoritative (DESIGN
// section 4). cox never mutates user-level Pi config: this extension is installed project-local by
// `cox workspace init` / `cox workspace hooks --harness pi` and loaded explicitly at launch with `-e`.
//
// Roles (from env / the installed marker): a LEADER maps Pi lifecycle events onto the SAME Go-owned leader hooks the
// Claude/Codex leader hooks run - `cox hook prompt-drain | stop-rewake | precompact | session-start` (DESIGN item 4) -
// so the Go side stays the single owner of multi-epic drain, rewake, the turn-boundary watcher guard, and checkpoints.
// Unbound (no COX_EPIC, no epic marker) the leader supervises EVERY active epic of the workspace (item 3); a marker or
// COX_EPIC binds it to one. A WORKER keeps its own bound busy/interrupt/checkpoint wiring. Failure or absence stays
// visible and is never reported as an automatic checkpoint success.
import type { ExtensionAPI, ExtensionContext } from "@earendil-works/pi-coding-agent";
import { spawn, execFile, type ChildProcess } from "node:child_process";
import { existsSync, readFileSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { Supervisor, TurnEndLatch } from "./cox-supervisor.ts";
import { coxArgs, resolveEpic } from "./cox-commands.ts";

const EPIC_MARKER = "cox-pi.epic"; // written next to the extension by `cox workspace hooks --harness pi --epic <dir>`
const ACTIVATED_MARKER = ".cox-pi.activated"; // written on session_start so cox can confirm Pi actually loaded this extension
// REOPEN_BUDGET bounds the leader's client-side per-turn stop-rewake reopens. The Go budget keyed on ORCA_TERMINAL_HANDLE
// bounds the real dead-watcher guard loop server-side (3 blocks/turn -> exit 0 + warning); this is the belt-and-suspenders
// backstop for a stop-rewake that keeps exiting 2 without a real turn starting (a broken cox). Reset each turn.
const REOPEN_BUDGET = 3;
// DEFAULT_REOPEN is delivered when stop-rewake exits 2 with no message on stderr (should not happen; keeps the wake path
// visible rather than delivering an empty followUp).
const DEFAULT_REOPEN = "A Coxswain watcher wake arrived while idle. Run `cox wake drain`, handle each wake, then ack.";

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
  // Epic binding: COX_EPIC (set by the launch seam for a worker) wins, else the marker installed for a BOUND leader.
  // Empty = an UNBOUND leader: its hooks run in workspace mode (no --epic) so the Go side supervises every active epic.
  const epic = resolveEpic(process.env.COX_EPIC, readEpicMarker());
  const isLeader = (process.env.COX_ROLE || (story && story !== "_leader" ? "worker" : "leader")) === "leader";
  // Harness-owned busy state (DESIGN wave-3 item 2): the gen armed at dispatch. Absent gen (or no epic/story) means this
  // session reports no state - never a guess. Both worker and leader roles report, using whatever gen the launch env
  // carries (a leader carries none today, so it simply does not write).
  const busyGen = (process.env.COX_BUSY_GEN ?? "").trim();
  // The stable per-session handle the leader hooks key their block budget / single-waiter lock on, the way the Claude
  // hook envelope's ORCA_TERMINAL_HANDLE does (leader ruling 2026-09-23). Inherit it when Orca set it; otherwise
  // synthesize a stable per-process id so `cox hook prompt-drain` (reset) and `cox hook stop-rewake` (bump) key the same
  // budget file. Threaded to the leader hook children via their env.
  const handle = (process.env.ORCA_TERMINAL_HANDLE || "").trim() || `pi-${process.pid}`;
  const childEnv = { ...process.env, ORCA_TERMINAL_HANDLE: handle };

  // Latest lifecycle ctx, captured so an async child callback (which has no ctx of its own) can surface a warning.
  let lastCtx: ExtensionContext | null = null;
  // Per-turn stop-rewake reopen count (leader change #1); reset at the real turn start (before_agent_start).
  let reopenCount = 0;

  // applyBusy reports one lifecycle transition into the busy record, best-effort: a missing gen means no write, and a
  // refusal (a stale gen after re-arm) is swallowed so it never breaks Pi's own lifecycle.
  function applyBusy(state: "busy" | "idle", event: string): void {
    if (!busyGen || !epic || !story) return;
    execFile(cox, coxArgs.busyApply(epic, story, state, busyGen, event), () => {
      /* best-effort: cox rejects a stale gen; that must not disturb the turn */
    });
  }

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

  // Interrupt through the harness (DESIGN wave-3 item 4): while a turn runs, one `cox inbox interrupt-wait` child blocks
  // for a durable interrupt record; when it arrives (exit 0) the turn is aborted through Pi's own API (ctx.abort()),
  // which the backend keystroke cannot do for Pi 0.86.1 (dogfood F-C). The child is (re)spawned on agent_start and
  // retired on agent_settled. Worker only (a leader is not interrupted this way): gated on epic + a real story.
  const INTERRUPT_MAX = "30m"; // ponytail: the child recycles well within cox's 1h server-side cap; the turn kills it at settled
  let interruptChild: ChildProcess | null = null;
  let interruptWatching = false;

  function killInterruptChild(): void {
    interruptWatching = false;
    if (interruptChild) {
      try {
        interruptChild.kill();
      } catch {
        /* already gone */
      }
      interruptChild = null;
    }
  }

  function startInterruptWatch(ctx: ExtensionContext): void {
    if (isLeader || !epic || !story || story === "_leader") return;
    interruptWatching = true;
    spawnInterruptChild(ctx);
  }

  function spawnInterruptChild(ctx: ExtensionContext): void {
    if (!interruptWatching) return;
    try {
      const c = spawn(cox, coxArgs.interruptWait(epic, story, INTERRUPT_MAX), {
        stdio: ["ignore", "ignore", "ignore"],
      });
      interruptChild = c;
      c.on("exit", (code) => {
        if (c !== interruptChild) return; // superseded or killed at settled: ignore its exit
        interruptChild = null;
        if (code === 0) {
          interruptWatching = false; // the interrupt aborts this turn; the next agent_start re-arms
          try {
            ctx.abort(); // abort the running turn; agent_settled then reports idle
          } catch {
            /* not streaming, or the API rejected it: the record is already consumed */
          }
        } else if (interruptWatching) {
          spawnInterruptChild(ctx); // timeout (exit 3) or unexpected close: re-arm while the turn is live
        }
      });
      c.on("error", () => {
        if (c !== interruptChild) return;
        interruptChild = null;
        /* spawn failure: leave the keystroke fallback as the only interrupt path */
      });
    } catch {
      interruptChild = null;
    }
  }

  // startRewakeChild spawns one `cox hook stop-rewake` child for a generation (the leader's idle wake long-poll) and
  // routes its exit back into the supervisor. `--harness claude` reopens via exit 2 with the reopen/repair text on
  // stderr (item 4 + item 1 turn-boundary guard). No epic => the Go side waits on every active epic (item 3). The child
  // carries ORCA_TERMINAL_HANDLE so its block budget keys the same file prompt-drain resets.
  function startRewakeChild(gen: number): boolean {
    if (!isLeader) return false;
    try {
      killChild();
      const c = spawn(cox, coxArgs.stopRewake(epic), {
        env: childEnv,
        stdio: ["ignore", "ignore", "pipe"],
      });
      child = c;
      let err = "";
      c.stderr?.on("data", (d) => {
        err += String(d);
      });
      c.on("exit", (code) => {
        if (c !== child) return; // a superseded child: its exit is stale, ignore it
        child = null;
        if (code === 2) onRewakeReopen(gen, err.trim()); // a wake / tick / repair line: reopen the turn
        else if (code === 0) sup.onTimeout(gen); // idle timeout with nothing to reopen: re-arm a fresh long-poll
        else sup.onUnexpectedClose(gen); // unexpected close: bounded retry, exhaustion surfaced
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

  // onRewakeReopen delivers a stop-rewake exit-2 reopen, bounded by the per-turn budget (leader change #1). Past the
  // budget it neither delivers nor re-arms - it surfaces one visible warning and waits for the next turn's
  // before_agent_start to reset and re-arm - so a stop-rewake that keeps exiting 2 without a real turn cannot spin.
  function onRewakeReopen(gen: number, reopenText: string): void {
    reopenCount += 1;
    if (reopenCount > REOPEN_BUDGET) {
      lastCtx?.ui?.notify?.(
        `cox: stop-rewake reopened ${REOPEN_BUDGET}+ times this turn without progress; pausing wake supervision until the next turn`,
        "warning",
      );
      return;
    }
    sup.onWake(gen, reopenText || DEFAULT_REOPEN);
  }

  const sup = new Supervisor({
    spawnWait: startRewakeChild,
    deliver: (text) => {
      // Exactly one visible followUp per reopen (AC 3: a hook exit 2 is surfaced, never swallowed). The followUp opens a
      // new leader turn; that turn's before_agent_start runs prompt-drain and injects the actual per-epic wakes.
      void pi.sendUserMessage(text, { deliverAs: "followUp" });
    },
    onExhausted: (reason) => {
      // Surfaced, never hidden: tell the operator via a followUp so the leader can recover the wake channel by hand.
      void pi.sendUserMessage(
        `Coxswain wake supervision stopped: ${reason}. Recover with \`cox wake drain\` and relaunch.`,
        { deliverAs: "followUp" },
      );
    },
  });

  // session_start: inject the saved checkpoint (recovery context) and, for a leader, activate a fresh generation and
  // begin waiting for wakes. A fresh generation makes any callback from a prior session stale (one live generation
  // across /new, /resume, /fork, reload, quit).
  pi.on("session_start", async (_event, ctx) => {
    lastCtx = ctx;
    markActivated(); // startup handshake: confirm to cox that Pi loaded this extension
    injectCheckpoint(ctx);
    if (isLeader) sup.sessionStart();
  });

  // before_agent_start (leader): the Pi analogue of Claude's UserPromptSubmit - run `cox hook prompt-drain` and inject
  // the drained per-epic wakes as turn context (item 4). A starting turn SUPERSEDES the idle stop-rewake child (leader
  // change #2): retire it BEFORE draining so a wake is delivered once (drained here), never both injected and reopened.
  pi.on("before_agent_start", async (_event, ctx) => {
    if (!isLeader) return undefined;
    lastCtx = ctx;
    sup.sessionShutdown(); // retire the pending child's generation so its in-flight callback no-ops
    killChild(); // terminate the pending stop-rewake process
    reopenCount = 0; // change #1: a real turn starts -> reset the per-turn reopen budget
    const out = await capture(cox, coxArgs.promptDrain(epic), childEnv).catch(() => null);
    if (!out) return undefined;
    if (out.code !== 0) {
      const msg = out.stderr.trim();
      if (msg) ctx.ui?.notify?.(`cox hook prompt-drain: ${msg}`, "warning");
      return undefined;
    }
    const text = out.stdout.trim();
    if (!text) return undefined; // nothing unread across the supervised epics: inject nothing
    return { message: { customType: "cox-wakes", content: text, display: true } };
  });

  // session_before_compact: PERSIST a checkpoint from the current context BEFORE Pi summarizes it. `cox hook precompact`
  // writes <epic>/handoffs/<story>.md (whereas `cox checkpoint facts` only prints). Unbound leader (no epic) uses the
  // workspace path (per-epic leader checkpoint). Failure is left visible; never reported as an automatic success.
  pi.on("session_before_compact", async (_event, ctx) => {
    if (!isLeader && !epic) {
      ctx.ui?.notify?.("cox precompact skipped: no epic binding (COX_EPIC or installed marker)", "warning");
      return;
    }
    await run(cox, coxArgs.precompact(epic, epic ? identity : "", ctx.cwd), childEnv).catch((e: unknown) => {
      ctx.ui?.notify?.(`cox hook precompact failed before compaction: ${String(e)}`, "warning");
    });
  });

  // agent_start: a turn is running -> busy (DESIGN wave-3 item 2). Applied for both roles using the env gen. A worker
  // also arms the interrupt watcher for this turn (item 4), capturing the live ctx so an interrupt can abort it.
  pi.on("agent_start", async (_event, ctx) => {
    lastCtx = ctx;
    applyBusy("busy", "agent_start");
    startInterruptWatch(ctx);
  });

  // agent_settled: the turn has fully settled (Pi fires it even on abort/failure - no retry/compaction/continuation
  // follows), so reporting idle here also covers the abort and error paths, the DESIGN's "finally block" requirement.
  // Reported for both roles BEFORE the leader-only supervision so a worker's idle is never gated on the leader branch.
  // For a leader, before_agent_start retired the wait child, so the latch re-arms a fresh stop-rewake long-poll here.
  pi.on("agent_settled", async (_event, ctx) => {
    lastCtx = ctx;
    applyBusy("idle", "agent_settled");
    killInterruptChild(); // the turn ended: retire the interrupt watcher until the next turn
    if (!isLeader) return;
    const healthy = sup.liveGeneration() !== null;
    latch.onSettled(healthy, () => {
      sup.sessionStart(); // re-establish the idle wait child
      latch.consumed();
    });
  });

  // session_shutdown / process exit: retire the active generation and kill its child, so no stale callback mutates a
  // later session and no orphan wait child survives.
  pi.on("session_shutdown", async (_event, _ctx) => {
    sup.sessionShutdown();
    killChild();
    killInterruptChild();
  });
  process.once("exit", () => {
    killChild();
    killInterruptChild();
  });

  // injectCheckpoint runs `cox hook session-start` and, when it yields recovery context, delivers it as a followUp so the
  // session starts from the saved state. Absence/failure is visible (a notify), never a silent success.
  function injectCheckpoint(ctx: ExtensionContext): void {
    if (isLeader && !epic) {
      // Unbound leader: workspace-mode session-start injects the per-epic leader checkpoint for every active epic. The
      // Go hook prints nothing when there is nothing to recover, so we deliver only non-empty stdout (no double-turn).
      execFile(cox, coxArgs.sessionStart("", "", ctx.cwd), { env: childEnv }, (err, stdout, stderr) => {
        if (err) {
          ctx.ui?.notify?.(`cox hook session-start: ${String(stderr || err).trim()}`, "warning");
          return;
        }
        const text = (stdout || "").trim();
        if (text) void pi.sendUserMessage(text, { deliverAs: "followUp" });
      });
      return;
    }
    if (!epic) return; // no epic binding: nothing to inject
    // A fresh session (no saved checkpoint) is already driven by its launch prompt. The hook still emits a
    // "No checkpoint yet, start from the story" notice on stdout, and delivering that as a followUp would queue a
    // SECOND turn behind the launch prompt - the worker then redoes the whole task and emits a duplicate completion
    // (dogfood W1: two worker_done for one dispatch). Only inject when a checkpoint actually exists, i.e. a resume;
    // absence stays visible via notify but never triggers a turn.
    if (!existsSync(join(epic, "handoffs", `${identity}.md`))) {
      ctx.ui?.notify?.("cox: no checkpoint yet (fresh start); running from the launch prompt", "info");
      return;
    }
    // Inject through `cox hook session-start` so the current git HEAD (from the worktree) drives the CHECKPOINT STALE
    // freshness check; a stale checkpoint exits non-zero with the warning on stderr, which we surface.
    execFile(cox, coxArgs.sessionStart(epic, identity, ctx.cwd), { env: childEnv }, (err, stdout, stderr) => {
      const text = (stdout || "").trim();
      if (err) {
        ctx.ui?.notify?.(`cox hook session-start: ${String(stderr || err).trim()}`, "warning");
        return;
      }
      if (text) void pi.sendUserMessage(text, { deliverAs: "followUp" });
    });
  }
}

// run executes a command and resolves on exit 0, rejecting otherwise, so a caller can surface failures.
function run(cmd: string, args: string[], env: NodeJS.ProcessEnv): Promise<void> {
  return new Promise((resolve, reject) => {
    execFile(cmd, args, { env }, (err) => (err ? reject(err) : resolve()));
  });
}

// capture runs a command and resolves with its exit code and captured output, so a caller can inject stdout / surface
// stderr. It never rejects: a non-zero exit is reported as { code } for the caller to handle.
function capture(cmd: string, args: string[], env: NodeJS.ProcessEnv): Promise<{ code: number; stdout: string; stderr: string }> {
  return new Promise((resolve) => {
    execFile(cmd, args, { env }, (err, stdout, stderr) => {
      const code = err && typeof (err as { code?: unknown }).code === "number" ? (err as { code: number }).code : err ? 1 : 0;
      resolve({ code, stdout: stdout || "", stderr: stderr || "" });
    });
  });
}
