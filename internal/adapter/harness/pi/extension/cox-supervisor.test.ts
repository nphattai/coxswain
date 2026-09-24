// Deterministic lifecycle tests for the Coxswain Pi extension core (no real Pi session). Run with:
//   node --test hooks/pi/cox-supervisor.test.ts
// These prove the invariants the pi card's wake=push / checkpoint=auto claims are gated on (DESIGN section 4).
import { test } from "node:test";
import assert from "node:assert/strict";
import { Outbox, Supervisor, TurnEndLatch, claimProcessSingleton, __resetProcessSingleton, type SupervisorEffects } from "./cox-supervisor.ts";

// DESIGN item 2: the process-global singleton claim is true exactly once per process, so a second cox extension load in
// one Pi process (launch `-e` plus a project-local `.pi/extensions/` copy) stays inert.
test("claimProcessSingleton is true once per process, false thereafter", () => {
  __resetProcessSingleton();
  assert.equal(claimProcessSingleton(), true, "first activation claims the process");
  assert.equal(claimProcessSingleton(), false, "second activation is denied");
  assert.equal(claimProcessSingleton(), false, "and stays denied");
  __resetProcessSingleton();
  assert.equal(claimProcessSingleton(), true, "a reset (test-only) re-enables the claim");
  __resetProcessSingleton();
});

// recorder builds SupervisorEffects that log calls in order, so tests can assert ordering (successor-before-delivery).
function recorder(spawnOk = true) {
  const log: string[] = [];
  const fx: SupervisorEffects = {
    spawnWait: (gen) => {
      log.push(`spawn:${gen}`);
      return spawnOk;
    },
    deliver: (text) => log.push(`deliver:${text}`),
    onExhausted: (reason) => log.push(`exhausted:${reason}`),
  };
  return { fx, log };
}

test("session_start activates a fresh generation and starts one wait child", () => {
  const { fx, log } = recorder();
  const s = new Supervisor(fx);
  s.sessionStart();
  assert.equal(s.generation(), 1);
  assert.equal(s.liveGeneration(), 1);
  assert.deepEqual(log, ["spawn:1"]);
});

// FAIL_TO_PASS (dogfood F-5): the old onWake spawned a successor wait child BEFORE delivering. With an unacked backlog
// that successor exits 2 at once, against a turn that has not started yet - ~20 hook spawns/s. A wake now retires its
// child and delivers exactly once; the caller re-arms when the opened turn settles.
test("a wake delivers exactly once and spawns no successor (re-armed at settle, F-5)", () => {
  const { fx, log } = recorder();
  const s = new Supervisor(fx);
  s.sessionStart(); // spawn:1
  s.onWake(1, "wake-a");
  assert.deepEqual(log, ["spawn:1", "deliver:wake-a"], "no successor spawn before or after the delivery");
  assert.equal(s.liveGeneration(), null, "the exited child is not live; agent_settled re-arms");
  s.onWake(1, "wake-b"); // a late duplicate callback of the same generation still delivers once, spawns nothing
  assert.equal(log.filter((l) => l.startsWith("spawn:")).length, 1);
});

test("a wake for a stale generation is a no-op (one live generation)", () => {
  const { fx, log } = recorder();
  const s = new Supervisor(fx);
  s.sessionStart(); // gen 1
  s.sessionStart(); // gen 2 (e.g. /resume); gen 1 callbacks are now stale
  const before = log.length;
  s.onWake(1, "stale"); // stale generation
  assert.equal(log.length, before, "stale-generation wake must not spawn or deliver");
  assert.ok(!log.includes("deliver:stale"));
});

test("session_shutdown retires the generation; a later callback for it no-ops", () => {
  const { fx, log } = recorder();
  const s = new Supervisor(fx);
  s.sessionStart(); // gen 1
  const g = s.generation();
  s.sessionShutdown();
  assert.equal(s.liveGeneration(), null, "shutdown drops the live child");
  const before = log.length;
  s.onWake(g, "after-shutdown");
  s.onUnexpectedClose(g);
  assert.equal(log.length, before, "callbacks for the retired generation must no-op");
});

test("bounded retry on unexpected close, then exhaustion is surfaced", () => {
  const { fx, log } = recorder();
  const s = new Supervisor(fx, 2); // maxRetries = 2
  s.sessionStart(); // spawn:1
  s.onUnexpectedClose(1); // retry 1 -> spawn
  s.onUnexpectedClose(1); // retry 2 -> spawn
  s.onUnexpectedClose(1); // exhausted
  const spawns = log.filter((l) => l === "spawn:1").length;
  assert.equal(spawns, 3, "one initial + two retries");
  assert.ok(
    log.some((l) => l.startsWith("exhausted:")),
    "exhaustion must be surfaced, never hidden",
  );
  assert.equal(s.liveGeneration(), null, "no live child after exhaustion");
});

test("a wait-child timeout restarts the child without delivering or counting a retry", () => {
  const { fx, log } = recorder();
  const s = new Supervisor(fx, 2);
  s.sessionStart(); // spawn:1
  s.onUnexpectedClose(1); // retry 1
  s.onTimeout(1); // timeout resets retries and restarts, no delivery
  s.onUnexpectedClose(1); // retry 1 again (reset), not exhausted
  s.onUnexpectedClose(1); // retry 2
  assert.ok(!log.some((l) => l.startsWith("deliver:")), "a timeout never delivers");
  assert.ok(!log.some((l) => l.startsWith("exhausted:")), "timeout reset retries, so not exhausted yet");
  assert.equal(s.liveGeneration(), 1, "still supervising after a timeout restart");
});

test("agent_settled latch: one continuation per unhealthy cycle, no recursion", () => {
  const latch = new TurnEndLatch();
  let delivered = 0;
  const deliver = () => delivered++;

  latch.onSettled(false, deliver); // unhealthy -> one continuation
  assert.equal(delivered, 1);
  latch.onSettled(false, deliver); // still unhealthy, one already pending -> no recursion
  assert.equal(delivered, 1);
  assert.ok(latch.isPending());

  latch.consumed(); // the generated follow-up ran
  latch.onSettled(false, deliver); // unhealthy again -> a new continuation
  assert.equal(delivered, 2);

  latch.onSettled(true, deliver); // healthy -> clears the latch, no delivery
  assert.equal(delivered, 2);
  assert.ok(!latch.isPending());
});

// --- Outbox (dogfood F-9): only paths Pi accepts, confirmed delivery ---

// piState is a controllable stand-in for Pi's session flags; outboxRec logs every send.
function outboxRec() {
  const st = { streaming: false, quiet: false };
  const log: string[] = [];
  const box = new Outbox({
    prompt: (t) => log.push(`prompt:${t}`),
    followUp: (t) => log.push(`followUp:${t}`),
    streaming: () => st.streaming,
    quiet: () => st.quiet,
  });
  return { st, log, box };
}

test("outbox: holds during startup and a starting prompt; injects context at before_agent_start", () => {
  const { st, log, box } = outboxRec();
  box.sessionStart();
  st.quiet = true;
  box.push("context", "CKPT");
  assert.deepEqual(log, [], "startup: nothing is sent while the launch prompt may still enter");
  box.input(undefined); // the launch prompt enters its preflight
  st.quiet = true; // Pi still reports idle during preflight - the F-9 window
  box.flush();
  assert.deepEqual(log, [], "starting: never prompt into a preflight");
  assert.deepEqual(box.beforeAgentStart(), ["CKPT"], "the held context rides the starting turn");
  box.message("launch prompt"); // unrelated message
  box.message("... CKPT ..."); // the injected context message
  assert.deepEqual(box.pending(), [], "confirmed by message_start");
});

test("outbox: followUp only while streaming; prompt only when quiet", () => {
  const { st, log, box } = outboxRec();
  box.sessionStart();
  box.startupGrace(); // no launch prompt
  box.input(undefined);
  box.beforeAgentStart();
  st.streaming = true;
  box.agentStart();
  box.push("context", "N1");
  assert.deepEqual(log, ["followUp:N1"]);
  box.message("N1");
  st.streaming = false;
  st.quiet = true;
  assert.equal(box.settled(), true);
  box.push("context", "N2");
  assert.deepEqual(log, ["followUp:N1", "prompt:N2"]);
  assert.equal(box.phase(), "starting", "our own prompt is starting: the next push is held");
  box.push("context", "N3");
  assert.deepEqual(log, ["followUp:N1", "prompt:N2"]);
});

test("outbox: a rejected send is requeued at the quiet settle and delivered exactly once", () => {
  const { st, log, box } = outboxRec();
  box.sessionStart();
  st.quiet = true;
  box.startupGrace();
  box.push("context", "X");
  assert.deepEqual(log, ["prompt:X"]);
  // A foreign run grabbed the agent during our preflight; our prompt lost and settles while the winner still runs.
  st.quiet = false;
  box.agentStart(); // the winner's run
  assert.equal(box.settled(), false, "a race loser's settle is not quiet: nothing requeued or sent into the winner");
  assert.deepEqual(log, ["prompt:X"]);
  box.message("FOREIGN");
  st.quiet = true;
  assert.equal(box.settled(), true, "the winner's settle is quiet");
  assert.deepEqual(log, ["prompt:X", "prompt:X"], "X requeued and delivered on the next accepted path");
  box.message("X");
  assert.deepEqual(box.pending(), []);
  assert.equal(box.settled(), true);
  assert.equal(log.length, 2, "exactly once more, never again after confirmation");
});

test("outbox: at most one reopen waits; a starting turn supersedes it", () => {
  const { st, log, box } = outboxRec();
  box.sessionStart();
  box.push("reopen", "R1");
  box.push("reopen", "R2");
  assert.equal(box.pending().length, 1, "one pending reopen, latest text");
  assert.equal(box.pending()[0].text, "R2");
  box.input(undefined);
  assert.deepEqual(box.beforeAgentStart(), [], "the turn's prompt-drain delivers the wakes: the reopen is dropped");
  st.quiet = true;
  box.settled();
  assert.deepEqual(log, []);
});

test("outbox: a quiet settle with no run (a prompt Pi could not run) stalls idle prompts; the next real turn carries the item", () => {
  const { st, log, box } = outboxRec();
  box.sessionStart();
  st.quiet = true;
  box.startupGrace();
  box.push("context", "Y");
  assert.deepEqual(log, ["prompt:Y"]);
  box.input(undefined);
  box.beforeAgentStart(); // Y is inflight as the prompt text, not re-injected
  assert.equal(box.settled(), true, "settled with no agent_start");
  assert.deepEqual(log, ["prompt:Y"], "no re-prompt loop against a run that cannot start");
  box.push("context", "Z");
  assert.deepEqual(log, ["prompt:Y"], "still stalled");
  box.input(undefined); // the user types
  assert.deepEqual(box.beforeAgentStart(), ["Y", "Z"], "the next real turn carries every held item");
});

// --- Pi turn-end guard: firstmate tests/fm-turnend-guard.test.sh translated (R23) ------------------------------------
// Firstmate's .pi/extensions/fm-primary-turnend-guard.ts runs the guard on agent_settled once per LOGICAL agent run
// and injects at most one follow-up per run; the latch holds across internal tool turns and clears only when the
// generated follow-up settles or its delivery fails. Cox's Pi guard is the leader's `cox hook stop-rewake` child the
// extension spawns on a settle (its exit 2 with the guard banner is the follow-up), so each case drives the real wiring
// (cox-pi.ts) against a fake `cox` that logs every guard run. The fake Pi runs a delivered prompt the way Pi 0.86.1
// does (input, before_agent_start, agent_start, message_start, agent_settled), where firstmate's fake only settles:
// cox's Outbox confirms delivery from those events. Firstmate's typed FIRSTMATE_OP prefix is firstmate's Ahoy
// protocol (n/a); cox delivers on the path Pi accepts at that moment (prompt when idle), not always as a followUp.

type GuardCase = {
  handlers: Record<string, (event: unknown, ctx: unknown) => Promise<unknown> | unknown>;
  ctx: unknown;
  guardRuns: () => number;
  cleanup: () => Promise<void>;
};

async function guardCase(send: (text: string, c: GuardCase) => Promise<void>): Promise<GuardCase> {
  const { mkdtempSync, writeFileSync, chmodSync, existsSync, readFileSync, rmSync } = await import("node:fs");
  const { tmpdir } = await import("node:os");
  const { join } = await import("node:path");
  const dir = mkdtempSync(join(tmpdir(), "cox-pi-guard-"));
  const log = join(dir, "guard.log");
  const fake = join(dir, "cox");
  // The guard (stop-rewake) always blocks; the latched waiter (--guard=false) only waits; every other hook is a no-op.
  writeFileSync(
    fake,
    `#!/bin/sh
if [ "$1 $2" = "hook stop-rewake" ]; then
  case " $* " in *" --guard=false "*) exec sleep 30 ;; esac
  echo guard >> '${log}'
  echo 'TURN WOULD END BLIND - SUPERVISION IS OFF' >&2
  echo 'Watcher for e1 is not alive; run: cox watch --epic /e1 --replace' >&2
  exit 2
fi
exit 0
`,
  );
  chmodSync(fake, 0o755);
  const prev = { bin: process.env.COX_BIN, role: process.env.COX_ROLE, story: process.env.COX_STORY, epic: process.env.COX_EPIC };
  process.env.COX_BIN = fake;
  process.env.COX_ROLE = "leader";
  delete process.env.COX_STORY;
  process.env.COX_EPIC = dir;
  __resetProcessSingleton();
  const handlers: GuardCase["handlers"] = {};
  const ctx = { cwd: dir, isIdle: () => true, signal: undefined, ui: { notify() {} } };
  const c: GuardCase = {
    handlers,
    ctx,
    guardRuns: () => (existsSync(log) ? readFileSync(log, "utf8").trim().split("\n").filter(Boolean).length : 0),
    cleanup: async () => {
      await handlers["session_shutdown"]?.({ reason: "quit" }, ctx);
      rmSync(dir, { recursive: true, force: true });
      for (const [k, v] of Object.entries({ COX_BIN: prev.bin, COX_ROLE: prev.role, COX_STORY: prev.story, COX_EPIC: prev.epic })) {
        if (v === undefined) delete process.env[k];
        else process.env[k] = v;
      }
      __resetProcessSingleton();
    },
  };
  const { default: makeExtension } = await import("./cox-pi.ts");
  makeExtension({
    on: (evt: string, h: (event: unknown, ctx: unknown) => unknown) => {
      handlers[evt] = h as GuardCase["handlers"][string];
    },
    sendUserMessage: (text: string) => send(text, c),
  } as never);
  return c;
}

// runDelivered plays the run Pi starts for a delivered prompt, ending in that run's own settle.
async function runDelivered(c: GuardCase, text: string): Promise<void> {
  await c.handlers["input"]?.({ source: "extension" }, c.ctx);
  await c.handlers["before_agent_start"]?.({}, c.ctx);
  await c.handlers["agent_start"]?.({}, c.ctx);
  await c.handlers["message_start"]?.({ message: { content: text } }, c.ctx);
  await c.handlers["agent_settled"]?.({ type: "agent_settled" }, c.ctx);
}

// startUserRun plays a logical run the user starts (Pi fires before_agent_start for every run it starts).
async function startUserRun(c: GuardCase): Promise<void> {
  await c.handlers["input"]?.({ source: "interactive" }, c.ctx);
  await c.handlers["before_agent_start"]?.({}, c.ctx);
  await c.handlers["agent_start"]?.({}, c.ctx);
}

async function until(pred: () => boolean, ms = 5000): Promise<void> {
  const t0 = Date.now();
  while (!pred() && Date.now() - t0 < ms) await new Promise((r) => setTimeout(r, 10));
}

const settle = (ms = 400) => new Promise((r) => setTimeout(r, ms));

// fm: tests/fm-turnend-guard.test.sh:1061
test("FM/fm-turnend-guard/pi_extension_injects_once_per_logical_agent_run", async () => {
  let prompts = 0;
  const c = await guardCase(async (text, c) => {
    prompts += 1;
    assert.ok(text.includes("TURN WOULD END BLIND"), `unexpected prompt: ${text}`);
    await runDelivered(c, text); // the generated follow-up's own run settles inside the delivery
  });
  try {
    assert.equal(c.handlers["turn_end"], undefined, "guard still treats internal Pi turns as logical runs");
    assert.ok(c.handlers["agent_settled"], "agent_settled handler was not registered");

    await startUserRun(c); // a no-tool run
    await c.handlers["agent_settled"]({ type: "agent_settled" }, c.ctx);
    await until(() => prompts >= 1);
    await settle();
    assert.equal(prompts, 1, `no-tool run injected ${prompts} follow-ups`);

    await startUserRun(c); // a multi-tool run: internal turns are notifications only
    for (let i = 0; i < 3; i += 1) await c.handlers["turn_end"]?.({ type: "turn_end", turnIndex: i }, c.ctx);
    await c.handlers["agent_settled"]({ type: "agent_settled" }, c.ctx);
    await until(() => prompts >= 2);
    await settle();
    assert.equal(prompts, 2, `multi-tool run produced ${prompts - 1} follow-ups`);
    assert.equal(c.guardRuns(), 2, `guard predicate ran ${c.guardRuns()} times for two logical runs`);
  } finally {
    await c.cleanup();
  }
});

// fm: tests/fm-turnend-guard.test.sh:1128
test("FM/fm-turnend-guard/pi_extension_retries_after_followup_delivery_failure", async () => {
  let attempts = 0;
  const c = await guardCase(async (text, c) => {
    attempts += 1;
    if (attempts === 1) throw new Error("synthetic delivery failure");
    await runDelivered(c, text);
  });
  try {
    await startUserRun(c);
    await c.handlers["agent_settled"]({ type: "agent_settled" }, c.ctx);
    await until(() => attempts >= 1);
    await settle();
    await startUserRun(c);
    await c.handlers["agent_settled"]({ type: "agent_settled" }, c.ctx);
    await until(() => attempts >= 2);
    await settle();
    assert.equal(attempts, 2, `expected delivery retry, saw ${attempts} attempts`);
  } finally {
    await c.cleanup();
  }
});
