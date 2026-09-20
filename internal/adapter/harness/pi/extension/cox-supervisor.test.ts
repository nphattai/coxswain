// Deterministic lifecycle tests for the Coxswain Pi extension core (no real Pi session). Run with:
//   node --test hooks/pi/cox-supervisor.test.ts
// These prove the invariants the pi card's wake=push / checkpoint=auto claims are gated on (DESIGN section 4).
import { test } from "node:test";
import assert from "node:assert/strict";
import { Supervisor, TurnEndLatch, type SupervisorEffects } from "./cox-supervisor.ts";

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

test("successor-before-delivery: a wake spawns the successor before delivering, exactly once", () => {
  const { fx, log } = recorder();
  const s = new Supervisor(fx);
  s.sessionStart(); // spawn:1
  s.onWake(1, "wake-a");
  // The successor child (spawn) must come before the delivery, and there is exactly one delivery.
  assert.deepEqual(log, ["spawn:1", "spawn:1", "deliver:wake-a"]);
  const spawnIdx = log.lastIndexOf("spawn:1");
  const deliverIdx = log.indexOf("deliver:wake-a");
  assert.ok(spawnIdx < deliverIdx, "successor spawn must precede delivery");
  assert.equal(log.filter((l) => l.startsWith("deliver:")).length, 1, "exactly one delivery per wake");
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

test("a successor that will not start does not deliver blind and surfaces the failure", () => {
  const { fx, log } = recorder(false); // spawnWait always fails
  const s = new Supervisor(fx);
  s.sessionStart(); // could not establish a live child (spawn fails)
  // A wake must not deliver while the successor is not live.
  s.onWake(s.generation(), "blind");
  assert.ok(!log.includes("deliver:blind"), "must not deliver when the successor is not live");
  assert.ok(log.some((l) => l.startsWith("exhausted:")), "successor failure is surfaced");
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
