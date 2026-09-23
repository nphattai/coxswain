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
//
// Delivery (dogfood F-9): every text the extension sends - a stop-rewake reopen, a checkpoint, a supervision notice -
// goes through one Outbox (cox-supervisor.ts) that only uses a path Pi 0.86.1 accepts at that moment and confirms
// delivery by observing message_start, so nothing is rejected as `Extension "<runtime>" error` and nothing is lost.
// Every `cox` child runs with stdin closed (dogfood F-4: an open, never-written stdin pipe hung `cox hook prompt-drain`).
import type { ExtensionAPI, ExtensionContext } from "@earendil-works/pi-coding-agent";
import { spawn, execFile, type ChildProcess } from "node:child_process";
import { existsSync, readFileSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { Outbox, Supervisor, TurnEndLatch, claimProcessSingleton, releaseProcessSingleton } from "./cox-supervisor.ts";
import { coxArgs, resolveEpic } from "./cox-commands.ts";

const EPIC_MARKER = "cox-pi.epic"; // written next to the extension by `cox workspace hooks --harness pi --epic <dir>`
const ACTIVATED_MARKER = ".cox-pi.activated"; // written on session_start so cox can confirm Pi actually loaded this extension
// REOPEN_BUDGET bounds a reopen EPISODE: consecutive stop-rewake reopens with no user prompt in between and no new wake
// gen. In Pi every reopen is a new turn, so a per-turn budget never trips (dogfood finding 8: 49 reopen turns in 3
// min over a dead watcher). Past it: one visible warning, no more reopen turns until a user prompt or a new wake gen;
// the idle waiter keeps re-arming (with backoff) so a new gen is still seen. The Go block budget bounds the same
// episode server-side (prompt-drain --reopen keeps it).
const REOPEN_BUDGET = 3;
// Re-arm backoff (dogfood F-10): a stop-rewake that returns within QUICK_EXIT_MS (exit 0 at once - no active epic, a
// watcher restart, the Go budget spent - or an exit 2 past the episode budget) is re-armed after a doubling delay from
// REARM_MIN_MS up to REARM_MAX_MS instead of at once, so a waiter that cannot wait never spins. A waiter that lived
// longer, or a user prompt, resets it.
const QUICK_EXIT_MS = 5000;
const REARM_MIN_MS = 1000;
const REARM_MAX_MS = 60000;
// STARTUP_GRACE_MS: after session_start Pi submits the CLI launch prompt, if any (interactive-mode.js:829). Until a
// prompt enters or this grace passes, the Outbox holds its texts rather than start a turn that could collide with the
// launch prompt. A bare `pi` restart waits at most this long. COX_PI_STARTUP_GRACE_MS tunes it (a slow machine, tests).
const STARTUP_GRACE_MS = Number(process.env.COX_PI_STARTUP_GRACE_MS) || 1500;
// PUSH_NOTE rides the first turn context of every leader session (dogfood F-7): a Pi leader read the workspace's
// pull-harness idle rule and parked itself in `cox wake wait`.
const PUSH_NOTE =
  "Coxswain: this Pi leader runs on a PUSH harness - cox delivers watcher wakes to you as turns by itself. Never run " +
  "`cox wake wait`; when you have nothing left to do, end your turn.";
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
  // DESIGN item 2: if a cox extension already activated in this Pi process, this second load stays inert - it wires no
  // handlers and spawns no child, so there is one wake child, one busy writer, and one checkpoint per event.
  if (!claimProcessSingleton()) return;
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
  // The current reopen episode (finding 8): reopens delivered, the wake gens it covers, and whether its warning showed.
  let reopenCount = 0;
  let episodeGens = new Set<string>();
  let episodeWarned = false;
  let rearmDelay = 0; // the current re-arm backoff (ms); 0 = re-arm at once
  // Whether the turn now starting came from a user prompt (not our own reopen/context prompt): only a user turn resets
  // the Go block budget and the reopen episode.
  let userTurn = true;
  const warned = new Set<string>(); // stop-rewake warnings already shown this episode (each shown once)
  // live is false between session_shutdown and the next session_start: a late settle or timer then re-arms nothing and
  // sends nothing into a session that is going away.
  let live = true;
  // Whether this session's leader turn context already carried PUSH_NOTE (F-7): once per session.
  let pushNoted = false;

  // outbox is the only sender of text into Pi (F-9). Its state reads come from the latest ctx; with no ctx yet (before
  // any event), or a ctx that cannot answer, the session is treated as neither streaming nor quiet, so nothing is sent
  // blind.
  const piState = (read: (ctx: ExtensionContext) => boolean): boolean => {
    try {
      return lastCtx ? read(lastCtx) : false;
    } catch {
      return false;
    }
  };
  const outbox = new Outbox({
    prompt: (text) => {
      if (live) pi.sendUserMessage(text);
    },
    followUp: (text) => {
      if (live) pi.sendUserMessage(text, { deliverAs: "followUp" });
    },
    streaming: () => piState((ctx) => !ctx.isIdle()),
    quiet: () => piState((ctx) => ctx.isIdle() && ctx.signal === undefined),
  });

  // applyBusy reports one lifecycle transition into the busy record, best-effort: a missing gen means no write, and a
  // refusal (a stale gen after re-arm) is swallowed so it never breaks Pi's own lifecycle.
  function applyBusy(state: "busy" | "idle", event: string): void {
    if (!busyGen || !epic || !story) return;
    execCox(cox, coxArgs.busyApply(epic, story, state, busyGen, event), process.env, () => {
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
      const started = Date.now();
      let err = "";
      c.stderr?.on("data", (d) => {
        err += String(d);
      });
      c.on("exit", (code) => {
        if (c !== child) return; // a superseded child: its exit is stale, ignore it
        child = null;
        const lived = Date.now() - started;
        if (code === 2) {
          onRewakeReopen(gen, err.trim(), lived); // a wake or repair line: reopen the turn (episode-bounded)
        } else if (code === 0) {
          // idle timeout, or the Go side ended the wait (restarted watcher, spent block budget): surface its note once
          // and re-arm, backing off when it returned at once.
          const note = err.trim();
          const key = note.replace(/\d+/g, "#"); // the Go budget note embeds a changing count: show it once per episode
          if (note && !warned.has(key)) {
            warned.add(key);
            lastCtx?.ui?.notify?.(note, "warning");
          }
          rearm(gen, lived);
        } else {
          sup.onUnexpectedClose(gen); // unexpected close: bounded retry, exhaustion surfaced
        }
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

  // rearm starts the next idle waiter for gen: at once after a waiter that really waited, else after the backoff.
  function rearm(gen: number, lived: number): void {
    if (!live) return;
    if (lived >= QUICK_EXIT_MS) {
      rearmDelay = 0;
      sup.onTimeout(gen);
      return;
    }
    rearmDelay = rearmDelay ? Math.min(rearmDelay * 2, REARM_MAX_MS) : REARM_MIN_MS;
    setTimeout(() => sup.onTimeout(gen), rearmDelay).unref?.(); // a stale gen (a turn started meanwhile) no-ops
  }

  // resetEpisode starts a new reopen episode (a user prompt, or a new wake gen).
  function resetEpisode(gens: Set<string>): void {
    reopenCount = 0;
    episodeGens = gens;
    episodeWarned = false;
    warned.clear();
    rearmDelay = 0;
  }

  // onRewakeReopen delivers a stop-rewake exit-2 reopen, bounded per EPISODE (finding 8). A wake gen the episode has not
  // seen starts a new episode. Past the budget it shows one warning and delivers nothing more; the waiter re-arms with
  // backoff so a new gen or a user prompt is still noticed, and a reopen that keeps coming cannot spin or cost turns.
  function onRewakeReopen(gen: number, reopenText: string, lived: number): void {
    const gens = new Set(reopenText.match(/\[gen \d+\]/g) ?? []);
    if ([...gens].some((g) => !episodeGens.has(g))) resetEpisode(gens);
    reopenCount += 1;
    if (reopenCount > REOPEN_BUDGET) {
      if (!episodeWarned) {
        episodeWarned = true;
        lastCtx?.ui?.notify?.(
          `cox: stop-rewake reopened ${REOPEN_BUDGET} times with no new wake and no prompt from you; pausing reopen turns ` +
            `until you prompt or a new wake arrives. Last reopen: ${reopenText.split("\n")[0]}`,
          "warning",
        );
      }
      rearm(gen, lived);
      return;
    }
    sup.onWake(gen, reopenText || DEFAULT_REOPEN);
  }

  const sup = new Supervisor({
    spawnWait: startRewakeChild,
    deliver: (text) => {
      // Exactly one visible delivery per reopen (AC 3: a hook exit 2 is surfaced, never swallowed). It opens a new
      // leader turn (or is superseded by a turn already starting); that turn's before_agent_start runs prompt-drain and
      // injects the actual per-epic wakes.
      outbox.push("reopen", text);
    },
    onExhausted: (reason) => {
      // Surfaced, never hidden: tell the operator so the leader can recover the wake channel by hand.
      outbox.push("context", `Coxswain wake supervision stopped: ${reason}. Recover with \`cox wake drain\` and relaunch.`);
    },
  });

  // session_start: inject the saved checkpoint (recovery context) and, for a leader, activate a fresh generation and
  // begin waiting for wakes. A fresh generation makes any callback from a prior session stale (one live generation
  // across /new, /resume, /fork, reload, quit).
  pi.on("session_start", async (_event, ctx) => {
    lastCtx = ctx;
    live = true;
    markActivated(); // startup handshake: confirm to cox that Pi loaded this extension
    outbox.sessionStart();
    pushNoted = false;
    setTimeout(() => outbox.startupGrace(), STARTUP_GRACE_MS).unref?.();
    injectCheckpoint(ctx);
    if (isLeader) sup.sessionStart();
  });

  // input: a prompt entered Pi (the launch prompt, the user, or our own). An idle prompt starts its preflight now, so
  // the Outbox stops sending until the run streams (F-9).
  pi.on("input", async (event, ctx) => {
    lastCtx = ctx;
    const e = event as { streamingBehavior?: string; source?: string };
    outbox.input(e.streamingBehavior);
    if (e.streamingBehavior === undefined) {
      userTurn = e.source !== "extension";
      if (userTurn) resetEpisode(new Set()); // a user prompt ends the reopen episode (finding 8)
    }
    return { action: "continue" as const };
  });

  // message_start: Pi put a message into the run; it confirms every Outbox item whose text it carries.
  pi.on("message_start", async (event, ctx) => {
    lastCtx = ctx;
    outbox.message(messageText((event as { message?: unknown }).message));
  });

  // before_agent_start (leader): the Pi analogue of Claude's UserPromptSubmit - run `cox hook prompt-drain` and inject
  // the drained per-epic wakes as turn context (item 4). A starting turn SUPERSEDES the idle stop-rewake child (leader
  // change #2): retire it BEFORE draining so a wake is delivered once (drained here), never both injected and reopened.
  // Both roles: whatever the Outbox holds (a resume checkpoint, a notice) rides this turn's context - the one path Pi
  // accepts even while a prompt is starting (F-9) - instead of a separate prompt that would be rejected.
  pi.on("before_agent_start", async (_event, ctx) => {
    lastCtx = ctx;
    const parts: string[] = [];
    if (isLeader) {
      sup.sessionShutdown(); // retire the pending child's generation so its in-flight callback no-ops
      killChild(); // terminate the pending stop-rewake process
      const out = await capture(cox, coxArgs.promptDrain(epic, !userTurn), childEnv).catch(() => null);
      if (out && out.code !== 0) {
        const msg = out.stderr.trim();
        if (msg) ctx.ui?.notify?.(`cox hook prompt-drain: ${msg}`, "warning");
      } else if (out && out.stdout.trim()) {
        parts.push(out.stdout.trim()); // the unread wakes across the supervised epics
      }
      if (!pushNoted) {
        parts.push(PUSH_NOTE);
        pushNoted = true;
      }
    }
    parts.push(...outbox.beforeAgentStart()); // after the await: anything that arrived meanwhile rides this turn too
    if (parts.length === 0) return undefined;
    return { message: { customType: "cox-wakes", content: parts.join("\n\n"), display: true } };
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
    outbox.agentStart();
    applyBusy("busy", "agent_start");
    startInterruptWatch(ctx);
  });

  // agent_settled: the turn has fully settled (Pi fires it even on abort/failure - no retry/compaction/continuation
  // follows), so reporting idle here also covers the abort and error paths, the DESIGN's "finally block" requirement.
  // Reported for both roles BEFORE the leader-only supervision so a worker's idle is never gated on the leader branch.
  // For a leader, before_agent_start retired the wait child, so the latch re-arms a fresh stop-rewake long-poll here.
  // Only a QUIET settle counts (Outbox.settled): a prompt that lost a start race also settles, while the winning run
  // still holds the agent, and must not report idle, retire the interrupt watcher, or re-arm the leader's idle waiter.
  pi.on("agent_settled", async (_event, ctx) => {
    lastCtx = ctx;
    if (!outbox.settled()) return; // requeues what this run did not carry and delivers it now that Pi is idle
    applyBusy("idle", "agent_settled");
    killInterruptChild(); // the turn ended: retire the interrupt watcher until the next turn
    if (!isLeader || !live) return;
    const healthy = sup.liveGeneration() !== null;
    latch.onSettled(healthy, () => {
      sup.sessionStart(); // re-establish the idle wait child
      latch.consumed();
    });
  });

  // A manual /compact at idle holds the Outbox (Pi refuses a prompt while compacting); retry once it ends.
  pi.on("session_compact", async (_event, ctx) => {
    lastCtx = ctx;
    outbox.flush();
  });
  pi.on("session_compact_failed", async (_event, ctx) => {
    lastCtx = ctx;
    outbox.flush();
  });

  // session_shutdown / process exit: retire the active generation and kill its child, so no stale callback mutates a
  // later session and no orphan wait child survives.
  pi.on("session_shutdown", async (event, _ctx) => {
    live = false;
    // A /reload re-instantiates the extensions: hand the claim to the reloaded instance.
    if ((event as { reason?: string }).reason === "reload") releaseProcessSingleton();
    sup.sessionShutdown();
    killChild();
    killInterruptChild();
  });
  process.once("exit", () => {
    killChild();
    killInterruptChild();
  });

  // injectCheckpoint runs `cox hook session-start` and, when it yields recovery context, hands it to the Outbox, which
  // injects it into the first turn's context (a resume prompt's own turn), so the session starts from the saved state. Absence/failure is visible (a notify), never a silent success.
  function injectCheckpoint(ctx: ExtensionContext): void {
    if (isLeader && !epic) {
      // Unbound leader: workspace-mode session-start injects the per-epic leader checkpoint for every active epic. The
      // Go hook prints nothing when there is nothing to recover, so we deliver only non-empty stdout (no double-turn).
      execCox(cox, coxArgs.sessionStart("", "", ctx.cwd), childEnv, (err, stdout, stderr) => {
        if (err) {
          ctx.ui?.notify?.(`cox hook session-start: ${String(stderr || err).trim()}`, "warning");
          return;
        }
        const text = (stdout || "").trim();
        if (text) outbox.push("context", text);
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
    execCox(cox, coxArgs.sessionStart(epic, identity, ctx.cwd), childEnv, (err, stdout, stderr) => {
      const text = (stdout || "").trim();
      if (err) {
        ctx.ui?.notify?.(`cox hook session-start: ${String(stderr || err).trim()}`, "warning");
        return;
      }
      if (text) outbox.push("context", text);
    });
  }
}

// execCox runs a `cox` child with its stdin CLOSED. execFile leaves stdin an open pipe, and a child that reads stdin
// (`cox hook prompt-drain` reads the harness envelope) then waits for an EOF that never comes (dogfood F-4). Every cox
// child the extension runs goes through here or through spawn with stdin "ignore".
function execCox(
  cmd: string,
  args: string[],
  env: NodeJS.ProcessEnv,
  cb: (err: Error | null, stdout: string, stderr: string) => void,
): void {
  const c = execFile(cmd, args, { env }, (err, stdout, stderr) => cb(err, String(stdout ?? ""), String(stderr ?? "")));
  c.stdin?.end();
}

// messageText flattens a Pi message's content (a string, or text parts) for delivery confirmation.
function messageText(message: unknown): string {
  const content = (message as { content?: unknown } | undefined)?.content;
  if (typeof content === "string") return content;
  if (!Array.isArray(content)) return "";
  return content
    .map((p) => (p && typeof p === "object" && (p as { type?: unknown }).type === "text" ? String((p as { text?: unknown }).text ?? "") : ""))
    .join("\n");
}

// run executes a command and resolves on exit 0, rejecting otherwise, so a caller can surface failures.
function run(cmd: string, args: string[], env: NodeJS.ProcessEnv): Promise<void> {
  return new Promise((resolve, reject) => {
    execCox(cmd, args, env, (err) => (err ? reject(err) : resolve()));
  });
}

// capture runs a command and resolves with its exit code and captured output, so a caller can inject stdout / surface
// stderr. It never rejects: a non-zero exit is reported as { code } for the caller to handle.
function capture(cmd: string, args: string[], env: NodeJS.ProcessEnv): Promise<{ code: number; stdout: string; stderr: string }> {
  return new Promise((resolve) => {
    execCox(cmd, args, env, (err, stdout, stderr) => {
      const errCode = (err as { code?: unknown } | null)?.code;
      const code = typeof errCode === "number" ? errCode : err ? 1 : 0;
      resolve({ code, stdout, stderr });
    });
  });
}
