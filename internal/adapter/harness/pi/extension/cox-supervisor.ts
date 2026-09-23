// cox-supervisor.ts - the runtime-independent lifecycle core of the Coxswain Pi extension. It encodes the invariants
// adapted from Firstmate (see reports/firstmate-pi-comparison.md), factored out of the Pi API wiring so they can be
// tested deterministically without a real Pi session (DESIGN section 4). The wiring in cox-pi.ts feeds real Pi events
// into these methods.
//
// Invariants:
//   - one generation-scoped wait child at a time; session_start activates a fresh generation
//   - a durable wake retires its child and delivers exactly once; the next wait child is armed when the turn it opens
//     settles (agent_settled), never before - a successor armed before delivery exits 2 again at once on an unacked
//     backlog and spun ~20 hook spawns/s against a turn that had not started yet (dogfood F-5)
//   - bounded retry on unexpected child closure, with exhaustion surfaced, never hidden
//   - stale-generation callbacks (from /new, /resume, /fork, shutdown) are no-ops
//   - session_shutdown / process exit retire the active generation
//   - every text reaches Pi through the Outbox, which only uses a path Pi 0.86.1 accepts at that moment and confirms
//     delivery by observation (see Outbox)

export interface SupervisorEffects {
  // spawnWait starts a `cox wake wait` child bound to generation gen. Returns whether the child started.
  spawnWait: (gen: number) => boolean;
  // deliver delivers one durable wake to the session (through the Outbox). Called at most once per wake.
  deliver: (text: string) => void;
  // onExhausted surfaces a recovery failure (retry budget spent, or a successor that would not start).
  onExhausted: (reason: string) => void;
}

export class Supervisor {
  private gen = 0;
  private liveChildGen: number | null = null;
  private retries = 0;
  private fx: SupervisorEffects;
  private maxRetries: number;

  constructor(fx: SupervisorEffects, maxRetries = 3) {
    this.fx = fx;
    this.maxRetries = maxRetries;
  }

  // sessionStart activates a fresh generation and starts its wait child. /new, /resume, /fork each fire session_start,
  // so each bumps the generation and any in-flight callback for the old generation becomes stale.
  sessionStart(): void {
    this.gen += 1;
    this.retries = 0;
    this.startChild();
  }

  // sessionShutdown retires the active generation: the live child is dropped and the generation is bumped so any
  // still-in-flight callback carries a now-stale generation and no-ops.
  sessionShutdown(): void {
    this.liveChildGen = null;
    this.gen += 1;
  }

  // onWake handles a durable wake reported by the wait child of generation g: the child has exited, so no child is live,
  // and the wake is delivered exactly once. The next child is armed by the caller when the turn this opens settles.
  onWake(g: number, text: string): void {
    if (g !== this.gen) return; // stale generation: no-op
    this.retries = 0;
    this.liveChildGen = null;
    this.fx.deliver(text); // exactly one delivery per wake
  }

  // onTimeout handles a wait child that reached its deadline with no wake (a normal `cox wake wait` timeout, exit 3):
  // it simply starts a fresh wait child for the same generation - not a wake, not a retry.
  onTimeout(g: number): void {
    if (g !== this.gen) return; // stale: no-op
    this.retries = 0;
    this.startChild();
  }

  // onUnexpectedClose handles a wait child that closed without reporting a wake. It retries up to maxRetries, then
  // surfaces exhaustion rather than silently stopping wake delivery.
  onUnexpectedClose(g: number): void {
    if (g !== this.gen) return; // stale: no-op
    if (this.retries >= this.maxRetries) {
      this.liveChildGen = null;
      this.fx.onExhausted(`cox wake wait child closed ${this.retries} times without a wake; wake delivery stopped`);
      return;
    }
    this.retries += 1;
    this.startChild();
  }

  private startChild(): boolean {
    const ok = this.fx.spawnWait(this.gen);
    this.liveChildGen = ok ? this.gen : null;
    return ok;
  }

  // Test/inspection accessors.
  generation(): number {
    return this.gen;
  }
  liveGeneration(): number | null {
    return this.liveChildGen;
  }
}

// Outbox is the single sender of extension text into a Pi 0.86.1 session (dogfood F-9). Pi accepts extension text on
// exactly three paths, each only at certain moments:
//   1. turn context: the before_agent_start handler's returned message - always accepted, never a turn of its own;
//   2. a followUp while a run is streaming (after agent_start, before agent_settled) - Pi queues it;
//   3. a new prompt while nothing is in flight.
// AgentSession.prompt() runs a preflight (input handlers, before_agent_start) while isStreaming is still false, so a
// sendUserMessage in that window takes the prompt path, runs a SECOND before_agent_start, then agent.prompt throws
// "Agent is already processing a prompt"; the ExtensionAPI swallows it as `Extension "<runtime>" error` and the text is
// lost. The Outbox tracks the phase from the events the extension sees (input = an idle prompt entering preflight,
// agent_start, agent_settled) and never sends while a prompt is starting.
//
// The phase is inferred, so delivery is also CONFIRMED rather than assumed: every sent item stays inflight until a
// message_start carries its text (Pi emits one for every prompt message, user or custom, and every queued followUp);
// the final agent_settled returns every unconfirmed inflight item to pending for the next accepted path. A late
// confirmation also removes a requeued copy, so each item is delivered exactly once.
//
// Only a QUIET settle means idle. A prompt that loses a start race (it throws in agent.prompt because another run holds
// the agent) still settles through _runAgentPrompt's finally, and that finally clears Pi's isStreaming while the winner
// is still running. So "quiet" is Pi's own truth - no agent run flag AND no Agent.activeRun (ctx.signal, defined exactly
// while a run holds the agent) - and a loser's settle, which sees the winner's signal, changes nothing.
//
// Kinds: "context" (a checkpoint, a supervision notice) must reach the model; "reopen" (a stop-rewake nudge to open a
// leader turn) is superseded by any turn that starts, because that turn's prompt-drain delivers the wakes themselves -
// the wake stays durable in cox, and the next idle stop-rewake reopens again if it is still unacked.
export type OutboxPhase = "startup" | "idle" | "starting" | "running";
export type OutboxKind = "context" | "reopen";

export interface OutboxEffects {
  // prompt starts a new turn with text (pi.sendUserMessage(text)); only called when the session is quiet.
  prompt: (text: string) => void;
  // followUp queues text into the streaming run (pi.sendUserMessage(text, {deliverAs: "followUp"})).
  followUp: (text: string) => void;
  // streaming: Pi's isStreaming is set (!ctx.isIdle()), so a followUp is queued rather than prompted.
  streaming: () => boolean;
  // quiet: no agent run flag, no compaction, and no Agent.activeRun (ctx.isIdle() && !ctx.signal).
  quiet: () => boolean;
}

interface OutboxItem {
  kind: OutboxKind;
  text: string;
  inflight: boolean;
}

export class Outbox {
  private phaseNow: OutboxPhase = "startup";
  private items: OutboxItem[] = [];
  private fx: OutboxEffects;
  // ran: a run streamed (agent_start) since the last quiet settle. stalled: a quiet settle came with no run - a prompt
  // Pi accepted but could not run (a failing model/auth) - so re-prompting would only loop; held items wait for the
  // next real turn's context instead (never dropped, never spun).
  private ran = false;
  private stalled = false;

  constructor(fx: OutboxEffects) {
    this.fx = fx;
  }

  // push queues one text and delivers it as soon as an accepted path exists. At most one reopen waits unsent.
  push(kind: OutboxKind, text: string): void {
    if (kind === "reopen") {
      const waiting = this.items.find((i) => i.kind === "reopen" && !i.inflight);
      if (waiting) {
        waiting.text = text;
        this.flush();
        return;
      }
    }
    this.items.push({ kind, text, inflight: false });
    this.flush();
  }

  // sessionStart: a new session begins; the launch prompt (if any) is about to enter. Reopens of the old session are
  // superseded; context waits for the first turn (or the startup grace).
  sessionStart(): void {
    this.phaseNow = "startup";
    this.ran = false;
    this.stalled = false;
    this.items = this.items.filter((i) => i.kind !== "reopen");
    for (const i of this.items) i.inflight = false;
  }

  // startupGrace: no prompt entered within the grace after session_start (a bare `pi` restart), so the session is idle.
  startupGrace(): void {
    if (this.phaseNow !== "startup") return;
    this.phaseNow = "idle";
    this.flush();
  }

  // input: a prompt entered Pi. With no streamingBehavior it is an idle prompt starting its preflight (Pi's `input`
  // event reports streamingBehavior only when the input is queued into a running turn).
  input(streamingBehavior: string | undefined): void {
    if (streamingBehavior === undefined) this.phaseNow = "starting";
  }

  // beforeAgentStart: a turn is about to run. Returns the context texts to inject into it (path 1); pending reopens are
  // superseded by this turn's prompt-drain.
  beforeAgentStart(): string[] {
    if (this.phaseNow !== "running") this.phaseNow = "starting";
    this.items = this.items.filter((i) => i.inflight || i.kind !== "reopen");
    const out: string[] = [];
    for (const i of this.items) {
      if (i.inflight) continue;
      i.inflight = true;
      out.push(i.text);
    }
    return out;
  }

  // agentStart: a run is streaming. Every reopen is superseded (this turn drained the wakes); anything pending goes as
  // a followUp (path 2).
  agentStart(): void {
    this.phaseNow = "running";
    this.ran = true;
    this.stalled = false;
    this.items = this.items.filter((i) => i.kind !== "reopen");
    this.flush();
  }

  // message: Pi emitted message_start with this text; it confirms every item the text carries.
  message(text: string): void {
    if (!text) return;
    this.items = this.items.filter((i) => !text.includes(i.text));
  }

  // settled: a prompt settled. It returns true when the session is QUIET: unconfirmed inflight items were lost (a
  // rejected prompt, a turn that died before emitting them) and are requeued and delivered on the next accepted path.
  // A settle while another run still holds the agent (a race loser's) returns false and changes nothing.
  settled(): boolean {
    if (!this.fx.quiet()) return false;
    if (!this.ran) this.stalled = true;
    this.ran = false;
    this.phaseNow = "idle";
    for (const i of this.items) i.inflight = false;
    this.flush();
    return true;
  }

  // flush sends pending items on the path the current phase allows; in startup/starting it holds them.
  flush(): void {
    const pending = this.items.filter((i) => !i.inflight);
    if (pending.length === 0) return;
    if (this.phaseNow === "running" && this.fx.streaming()) {
      for (const i of pending) {
        i.inflight = true;
        this.fx.followUp(i.text);
      }
      return;
    }
    if (this.phaseNow === "idle" && !this.stalled && this.fx.quiet()) {
      for (const i of pending) i.inflight = true;
      this.phaseNow = "starting"; // our own prompt enters preflight now
      this.fx.prompt(pending.map((i) => i.text).join("\n\n"));
    }
    // ponytail: startup/starting, a race-corrupted run (quiet false, streaming false), or Pi compacting holds; the next
    // beforeAgentStart/agentStart/settled/grace/compaction end flushes. A prompt that enters `input` and then dies before agent_start without a settle (an auth error) holds
    // items until the next turn - delayed, never dropped.
  }

  // Test/inspection accessors.
  phase(): OutboxPhase {
    return this.phaseNow;
  }
  pending(): { kind: OutboxKind; text: string; inflight: boolean }[] {
    return this.items.map((i) => ({ ...i }));
  }
}

// TurnEndLatch bounds the agent_settled health continuation: at most one generated follow-up may reopen a blind or
// unhealthy cycle, and the follow-up it generates cannot itself trigger another (no recursion across internal tool
// turns). A healthy settle clears the latch.
export class TurnEndLatch {
  private pending = false;

  onSettled(healthy: boolean, deliver: () => void): void {
    if (healthy) {
      this.pending = false;
      return;
    }
    if (this.pending) return; // a generated follow-up is already in flight: do not recurse
    this.pending = true;
    deliver();
  }

  // consumed marks the generated follow-up as handled, so a later unhealthy settle may generate another.
  consumed(): void {
    this.pending = false;
  }

  isPending(): boolean {
    return this.pending;
  }
}

// Process-global singleton (DESIGN item 2). A Pi process can load a cox extension TWICE: the launch `-e` path plus a
// project-local `.pi/extensions/` copy, because the target repo is itself a cox workspace (the latent double-load from
// item 1). Only the first activation may wire handlers and spawn children; the second must stay inert so there is one
// wake child, one busy writer, and one checkpoint per event. The marker lives on globalThis (shared across the two
// module instances in the process), keyed by a registered Symbol so a second module instance sees the same claim.
const SINGLETON_KEY = Symbol.for("coxswain.pi.extension.claimed");

// claimProcessSingleton returns true exactly once per process (the first cox extension activation) and false thereafter.
export function claimProcessSingleton(): boolean {
  const g = globalThis as unknown as Record<symbol, unknown>;
  if (g[SINGLETON_KEY]) return false;
  g[SINGLETON_KEY] = true;
  return true;
}

// __resetProcessSingleton clears the claim. Tests only: `node --test` runs one file per process, and a suite that
// activates the extension repeatedly resets the claim before each activation to stay isolated.
export function __resetProcessSingleton(): void {
  delete (globalThis as unknown as Record<symbol, unknown>)[SINGLETON_KEY];
}
