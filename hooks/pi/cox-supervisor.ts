// cox-supervisor.ts - the runtime-independent lifecycle core of the Coxswain Pi extension. It encodes the invariants
// adapted from Firstmate (see reports/firstmate-pi-comparison.md), factored out of the Pi API wiring so they can be
// tested deterministically without a real Pi session (DESIGN section 4). The wiring in cox-pi.ts feeds real Pi events
// into these methods.
//
// Invariants:
//   - one generation-scoped `cox wake wait` child at a time; session_start activates a fresh generation
//   - successor-before-delivery: a durable wake starts the next wait child and only delivers once it is live
//   - exactly one pi.sendUserMessage(..., {deliverAs:"followUp"}) per wake
//   - bounded retry on unexpected child closure, with exhaustion surfaced, never hidden
//   - stale-generation callbacks (from /new, /resume, /fork, shutdown) are no-ops
//   - session_shutdown / process exit retire the active generation

export interface SupervisorEffects {
  // spawnWait starts a `cox wake wait` child bound to generation gen. Returns whether the child started.
  spawnWait: (gen: number) => boolean;
  // deliver delivers one durable wake to the session (pi.sendUserMessage followUp). Called at most once per wake.
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

  // onWake handles a durable wake reported by the wait child of generation g. Successor-before-delivery: it starts the
  // successor wait child first and delivers exactly once, only after the successor is live.
  onWake(g: number, text: string): void {
    if (g !== this.gen) return; // stale generation: no-op
    this.retries = 0;
    if (!this.startChild()) {
      this.fx.onExhausted("successor cox wake wait child failed to start; not delivering blind");
      return;
    }
    this.fx.deliver(text); // exactly one followUp per wake
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
