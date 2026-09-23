// testing-fake-pi.ts - a test double of the Pi 0.86.1 session an extension talks to. Test-only: it is not embedded in
// the cox binary or installed. It reproduces exactly the AgentSession/Agent behaviour the Coxswain extension's delivery
// depends on (@earendil-works/pi-coding-agent 0.86.1, dist/core/agent-session.js and pi-agent-core dist/agent.js):
//
//   - prompt(): `input` event (streamingBehavior reported only while a run is active), then - when a run is active - a
//     followUp is queued (or it throws without streamingBehavior); otherwise a preflight that awaits before_agent_start
//     while isStreaming is still FALSE, then _runAgentPrompt: isStreaming = true, agent.prompt throws "Agent is already
//     processing a prompt..." when another run holds the agent, and the finally ALWAYS clears isStreaming and emits
//     agent_settled - also for the loser of a start race, while the winner still runs.
//   - a run emits agent_start, a message_start per prompt message (user + before_agent_start custom), "thinks", then
//     one message_start per queued followUp, then settles.
//   - the ExtensionAPI's sendUserMessage returns void and swallows a rejection into `Extension "<runtime>" error`
//     (agent-session.js:2122), recorded here in runtimeErrors.
import { setTimeout as sleep } from "node:timers/promises";

type Handler = (event: unknown, ctx: unknown) => unknown;

export interface FakeMessage {
  role: "user" | "custom";
  text: string;
}

export class FakePiSession {
  handlers: Record<string, Handler[]> = {};
  runtimeErrors: string[] = []; // what Pi would print as `Extension "<runtime>" error: ...`
  messages: FakeMessage[] = []; // every message_start, in order: what the model actually received
  notifications: string[] = [];
  promptAttempts = 0; // every prompt() entry, accepted or not
  compacting = false;
  // turnMs is how long a run "thinks" (the model call); preflightMs is the extra preflight latency before
  // before_agent_start (auth/compaction checks).
  turnMs = 30;
  preflightMs = 0;
  // brokenModel: the run fails before streaming (a model/auth failure inside the agent run): no agent_start, no
  // messages, but the settle still comes.
  brokenModel = false;

  private isAgentRunActive = false; // AgentSession._isAgentRunActive (isStreaming)
  private agentBusy = false; // Agent.activeRun
  private abortSignal = new AbortController().signal;
  private followUps: string[] = [];
  private runs: Promise<void>[] = [];

  readonly api = {
    on: (evt: string, h: Handler) => {
      (this.handlers[evt] ??= []).push(h);
    },
    sendUserMessage: (content: string, opts?: { deliverAs?: "steer" | "followUp" }) => {
      const p = this.prompt(content, { source: "extension", streamingBehavior: opts?.deliverAs });
      this.track(p);
    },
  };

  ctx(cwd = "/") {
    const self = this;
    return {
      cwd,
      ui: { notify: (m: string) => this.notifications.push(m) },
      isIdle: () => !this.isAgentRunActive && !this.compacting,
      get signal() {
        return self.agentBusy ? self.abortSignal : undefined; // Agent.activeRun?.abortController.signal
      },
      abort: () => {},
    };
  }

  async emit(evt: string, event: unknown): Promise<unknown[]> {
    const out: unknown[] = [];
    for (const h of this.handlers[evt] ?? []) out.push(await h({ type: evt, ...(event as object) }, this.ctx()));
    return out;
  }

  // prompt mirrors AgentSession.prompt(); a caller outside the extension (the CLI launch prompt, the user) awaits it and
  // sees its rejection, like interactive mode does.
  async prompt(text: string, opts: { source?: string; streamingBehavior?: "steer" | "followUp" } = {}): Promise<void> {
    this.promptAttempts += 1;
    await this.emit("input", {
      text,
      source: opts.source ?? "interactive",
      streamingBehavior: this.isAgentRunActive ? opts.streamingBehavior : undefined,
    });
    if (this.isAgentRunActive) {
      if (!opts.streamingBehavior) throw new Error("Agent is already processing. Specify streamingBehavior ('steer' or 'followUp') to queue the message.");
      this.followUps.push(text);
      return;
    }
    if (this.compacting) throw new Error("Cannot submit a prompt while compaction is in progress.");
    if (this.preflightMs) await sleep(this.preflightMs);
    const msgs: FakeMessage[] = [{ role: "user", text }];
    for (const r of await this.emit("before_agent_start", { prompt: text })) {
      const m = (r as { message?: { content?: string } } | undefined)?.message;
      if (m) msgs.push({ role: "custom", text: String(m.content ?? "") });
    }
    // _runAgentPrompt
    this.isAgentRunActive = true;
    try {
      if (this.agentBusy) throw new Error("Agent is already processing a prompt. Use steer() or followUp() to queue messages, or wait for completion.");
      this.agentBusy = true;
      try {
        if (this.brokenModel) return;
        await this.emit("agent_start", {});
        for (const m of msgs) await this.deliver(m);
        await sleep(this.turnMs);
        while (this.followUps.length) {
          await this.deliver({ role: "user", text: this.followUps.shift()! });
          await sleep(this.turnMs);
        }
      } finally {
        this.agentBusy = false;
      }
    } finally {
      this.isAgentRunActive = false;
      await this.emit("agent_settled", {});
    }
  }

  // foreignRun mirrors a run Pi starts without an extension-visible prompt (sendCustomMessage with triggerTurn calls
  // _runAgentPrompt directly: no input, no before_agent_start). While it holds the agent, any prompt reaching
  // agent.prompt is rejected.
  foreignRun(text: string, ms: number): Promise<void> {
    const p = (async () => {
      this.isAgentRunActive = true;
      try {
        if (this.agentBusy) throw new Error("Agent is already processing a prompt. Use steer() or followUp() to queue messages, or wait for completion.");
        this.agentBusy = true;
        try {
          await this.emit("agent_start", {});
          await this.deliver({ role: "custom", text });
          await sleep(ms);
        } finally {
          this.agentBusy = false;
        }
      } finally {
        this.isAgentRunActive = false;
        await this.emit("agent_settled", {});
      }
    })();
    this.runs.push(p.catch(() => {}));
    return p;
  }

  private async deliver(m: FakeMessage): Promise<void> {
    this.messages.push(m);
    await this.emit("message_start", { message: { role: m.role, content: m.text } });
  }

  // track records an extension-initiated prompt; its rejection becomes a runtime error, exactly as Pi swallows it.
  private track(p: Promise<void>): void {
    this.runs.push(
      p.catch((e: unknown) => {
        this.runtimeErrors.push(e instanceof Error ? e.message : String(e));
      }),
    );
  }

  // idle resolves once no run is active and no extension prompt is pending, polling briefly.
  async idle(ms = 5000): Promise<void> {
    const t0 = Date.now();
    while (Date.now() - t0 < ms) {
      await Promise.all(this.runs);
      if (!this.isAgentRunActive && !this.agentBusy) {
        await sleep(20);
        if (!this.isAgentRunActive && !this.agentBusy) return;
      }
      await sleep(10);
    }
    throw new Error("fake pi never went idle");
  }

  // texts returns the text of every message the model received (message_start), for assertions.
  texts(): string[] {
    return this.messages.map((m) => m.text);
  }
}
