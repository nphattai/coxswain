// Delivery lifecycle tests for the cox-pi wiring against a faithful fake of the Pi 0.86.1 session (testing-fake-pi.ts),
// whose prompt path rejects a second prompt exactly like AgentSession/Agent do. Run with node --test.
//
// FAIL_TO_PASS (dogfood F-4, F-5, F-6, F-7, F-9): on beedd57 the extension sent every text with
// sendUserMessage(.., {deliverAs: "followUp"}) whatever Pi's state, so a text arriving while a prompt was starting took
// the prompt path and was rejected ("Agent is already processing a prompt", shown as `Extension "<runtime>" error`) and
// lost - the leader checkpoint on restart (L3) - and a wake backlog at start spun the hook pair (L1). Its cox children
// also inherited an open stdin pipe, which hung `cox hook prompt-drain` (every stub below reads stdin first).
import { test, beforeEach } from "node:test";
import assert from "node:assert/strict";
import { mkdtempSync, mkdirSync, writeFileSync, readFileSync, rmSync, chmodSync, existsSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { setTimeout as sleep } from "node:timers/promises";
import { FakePiSession } from "./testing-fake-pi.ts";

// The startup grace is read at module load; shorten it for the bare-restart cases before importing the extension.
process.env.COX_PI_STARTUP_GRACE_MS = "150";
const { default: makeExtension } = await import("./cox-pi.ts");
const { __resetProcessSingleton } = await import("./cox-supervisor.ts");
beforeEach(__resetProcessSingleton);

const ACTIVATED = join(import.meta.dirname, ".cox-pi.activated");
const WAKES = "Watcher wakes for wk (--epic /tmp/ws/proj/epics/wk) since your last turn:\n[gen 1] worker_done a: done\n[gen 10] status b: note";

// coxStub writes a fake `cox` that reads stdin to EOF FIRST (a child left with an open stdin pipe hangs here, like the
// real prompt-drain did), logs each call, and answers:
//   - hook stop-rewake: exit 2 with a reopen line while <dir>/acked is absent (an unacked backlog), else blocks
//   - hook prompt-drain: prints the wakes while unacked, else nothing
//   - hook session-start: after `sessionDelay` seconds prints `sessionOut`
//   - anything else: exit 0
//   - rewake "repair": stop-rewake always exits 2 with a watcher repair line (no wake gen) - a wedged watcher the leader
//     does not repair (dogfood AC5 run 1); "restart0": exits 0 at once with a restart note (AC5 run 2 / F-10);
//     "gen": exits 2 with the wake gen read from <dir>/gen.
function coxStub(
  dir: string,
  opts: { sessionOut?: string; sessionDelay?: number; rewake?: "backlog" | "repair" | "restart0" | "budget0" | "gen" } = {},
): { bin: string; log: string; acked: string } {
  const log = join(dir, "cox.log");
  const acked = join(dir, "acked");
  const bin = join(dir, "cox");
  writeFileSync(
    bin,
    `#!/bin/sh
cat >/dev/null
echo "$*" >> '${log}'
if [ "$1 $2" = "hook stop-rewake" ]; then
  case '${opts.rewake ?? "backlog"}' in
  repair) echo 'Watcher for beta is not alive; run: cox watch --epic /tmp/ws/proj/epics/beta --replace' 1>&2; exit 2 ;;
  restart0) echo 'cox: watcher for beta was not alive; restarted it (cox watch --epic /tmp/ws/proj/epics/beta)' 1>&2; exit 0 ;;
  budget0) n=$(( $(cat '${dir}/n' 2>/dev/null || echo 3) + 1 )); echo $n > '${dir}/n'
    echo "cox: watcher for beta still not alive after $n blocks this turn; ending the turn to avoid a wedge" 1>&2; exit 0 ;;
  gen) echo "Watcher wake while idle. Run cox wake drain, then ack-through:" 1>&2; echo "[gen $(cat '${dir}/gen')] status b: n" 1>&2; exit 2 ;;
  esac
  if [ -e '${acked}' ]; then exec sleep 30; fi
  echo 'Watcher wake while idle. Run cox wake drain --epic /tmp/ws/proj/epics/wk, then ack-through.' 1>&2; exit 2
fi
if [ "$1 $2" = "hook prompt-drain" ]; then
  [ -e '${acked}' ] || printf '%s' '${WAKES}'
  exit 0
fi
if [ "$1 $2" = "hook session-start" ]; then
  sleep ${opts.sessionDelay ?? 0}
  printf '%s' '${opts.sessionOut ?? ""}'
  exit 0
fi
exit 0
`,
  );
  chmodSync(bin, 0o755);
  return { bin, log, acked };
}

function calls(log: string, sub: string): number {
  if (!existsSync(log)) return 0;
  return readFileSync(log, "utf8").split("\n").filter((l) => l.startsWith(sub)).length;
}

// withEnv sets the launch env for one role, runs body, restores every key.
async function withEnv(vars: Record<string, string | undefined>, body: () => Promise<void>): Promise<void> {
  const keys = ["COX_STORY", "COX_EPIC", "COX_ROLE", "COX_BIN", "ORCA_TERMINAL_HANDLE", "COX_BUSY_GEN"];
  const prev: Record<string, string | undefined> = {};
  for (const k of keys) prev[k] = process.env[k];
  for (const k of keys) {
    const v = vars[k];
    if (v === undefined) delete process.env[k];
    else process.env[k] = v;
  }
  try {
    await body();
  } finally {
    for (const k of keys) {
      if (prev[k] === undefined) delete process.env[k];
      else process.env[k] = prev[k];
    }
    try {
      rmSync(ACTIVATED);
    } catch {
      /* not written */
    }
  }
}

// start activates the extension on a fake session and fires session_start the way Pi does at launch.
async function start(fake: FakePiSession, cwd: string): Promise<void> {
  makeExtension(fake.api as never);
  await fake.emit("session_start", { reason: "startup" });
  void cwd;
}

// modelAcks makes the fake "model" ack whenever the wakes reach it, as a leader turn would with `cox wake ack-through`.
function modelAcks(fake: FakePiSession, acked: string): void {
  fake.api.on("message_start", (event) => {
    const c = String((event as { message?: { content?: unknown } }).message?.content ?? "");
    if (c.includes("[gen 1]")) writeFileSync(acked, "");
  });
}

async function waitFor(pred: () => boolean, ms = 5000): Promise<void> {
  const t0 = Date.now();
  while (!pred()) {
    if (Date.now() - t0 > ms) throw new Error("waitFor timed out");
    await sleep(10);
  }
}

const count = (xs: string[], needle: string) => xs.filter((t) => t.includes(needle)).length;

test("leader + 10-wake backlog + launch prompt: wakes delivered once, settles, bounded spawns, zero <runtime> errors (F-5, F-9)", async () => {
  const dir = mkdtempSync(join(tmpdir(), "coxpi-backlog-"));
  const { bin, log, acked } = coxStub(dir);
  await withEnv({ COX_ROLE: "leader", COX_BIN: bin }, async () => {
    const fake = new FakePiSession();
    fake.preflightMs = 100;
    modelAcks(fake, acked);
    await start(fake, dir);
    await sleep(20); // Pi submits the CLI launch prompt right after extension init
    await fake.prompt("you are the leader");
    await fake.idle();
    await sleep(400); // any spin would show here
    assert.deepEqual(fake.runtimeErrors, [], "no rejected prompt");
    assert.equal(count(fake.texts(), "[gen 1]"), 1, "the backlog reaches the model exactly once");
    assert.equal(fake.messages.filter((m) => m.role === "user").length, 1, "one turn: the launch prompt; no reopen turn");
    assert.ok(calls(log, "hook stop-rewake") <= 2, `stop-rewake spawns bounded (armed at start + re-armed at settle): ${calls(log, "hook stop-rewake")}`);
    assert.equal(calls(log, "hook prompt-drain"), 1, "one prompt-drain per real turn");
    await fake.emit("session_shutdown", {});
  });
  rmSync(dir, { recursive: true, force: true });
});

test("leader + backlog, bare restart (no launch prompt): one reopen turn after the grace, then settles (F-5)", async () => {
  const dir = mkdtempSync(join(tmpdir(), "coxpi-bare-"));
  const { bin, log, acked } = coxStub(dir);
  await withEnv({ COX_ROLE: "leader", COX_BIN: bin }, async () => {
    const fake = new FakePiSession();
    modelAcks(fake, acked);
    await start(fake, dir);
    await sleep(600);
    await fake.idle();
    await sleep(300);
    assert.deepEqual(fake.runtimeErrors, []);
    assert.equal(fake.messages.filter((m) => m.role === "user").length, 1, "exactly one reopen turn");
    assert.equal(count(fake.texts(), "[gen 1]"), 1);
    assert.ok(calls(log, "hook stop-rewake") <= 2, `bounded: ${calls(log, "hook stop-rewake")}`);
    await fake.emit("session_shutdown", {});
  });
  rmSync(dir, { recursive: true, force: true });
});

test("leader restart: the checkpoint landing while the launch prompt starts is injected into that turn, once (L3, F-9)", async () => {
  const dir = mkdtempSync(join(tmpdir(), "coxpi-l3-"));
  const { bin, acked } = coxStub(dir, { sessionOut: "Checkpoint for story _leader (attempt 1) LEADER-CKPT", sessionDelay: 0.2 });
  writeFileSync(acked, ""); // no backlog: isolate the checkpoint
  await withEnv({ COX_ROLE: "leader", COX_BIN: bin }, async () => {
    const fake = new FakePiSession();
    fake.preflightMs = 2000; // the checkpoint (~0.2-0.7s) lands mid-preflight
    fake.turnMs = 1500; // long enough that the old wiring's prompt-path followUp collides with this run
    await start(fake, dir);
    await sleep(20);
    await fake.prompt("you are the leader");
    await fake.idle();
    await sleep(200);
    assert.deepEqual(fake.runtimeErrors, [], "no `Extension \"<runtime>\" error`");
    assert.equal(count(fake.texts(), "LEADER-CKPT"), 1, "the checkpoint reaches the model exactly once");
    assert.equal(fake.messages.filter((m) => m.role === "user").length, 1, "as context of the launch turn, not a turn of its own");
    await fake.emit("session_shutdown", {});
  });
  rmSync(dir, { recursive: true, force: true });
});

test("leader: session-start notice never becomes an extra turn; the first turn says push harness (F-6, F-7)", async () => {
  const dir = mkdtempSync(join(tmpdir(), "coxpi-fresh-"));
  const { bin, acked } = coxStub(dir, { sessionOut: "No checkpoint at x yet (first attempt or none written). Start from the story." });
  writeFileSync(acked, "");
  await withEnv({ COX_ROLE: "leader", COX_BIN: bin }, async () => {
    const fake = new FakePiSession();
    fake.preflightMs = 2000; // the notice lands while the launch prompt starts
    fake.turnMs = 1500;
    await start(fake, dir);
    await sleep(20);
    await fake.prompt("you are the leader");
    await fake.idle();
    await sleep(300);
    assert.deepEqual(fake.runtimeErrors, []);
    assert.equal(fake.messages.filter((m) => m.role === "user").length, 1, "one turn only");
    fake.preflightMs = 0;
    fake.turnMs = 30;
    const ctx = fake.messages.find((m) => m.role === "custom")?.text ?? "";
    assert.match(ctx, /PUSH harness/, "F-7: the Pi leader is told it is a push harness");
    assert.match(ctx, /Never run `cox wake wait`/);
    await fake.prompt("second turn");
    await fake.idle();
    assert.equal(count(fake.texts(), "PUSH harness"), 1, "once per session");
    await fake.emit("session_shutdown", {});
  });
  rmSync(dir, { recursive: true, force: true });
});

test("worker resume: the checkpoint rides the resume prompt's context, never a rejected prompt (q001 requirement 2)", async () => {
  const dir = mkdtempSync(join(tmpdir(), "coxpi-wresume-"));
  const epic = join(dir, "epic");
  mkdirSync(join(epic, "handoffs"), { recursive: true });
  writeFileSync(join(epic, "handoffs", "w1.md"), "saved\n");
  const { bin } = coxStub(dir, { sessionOut: "Checkpoint for story w1 WORKER-CKPT", sessionDelay: 0.2 });
  await withEnv({ COX_STORY: "w1", COX_EPIC: epic, COX_BIN: bin }, async () => {
    const fake = new FakePiSession();
    fake.preflightMs = 2000;
    fake.turnMs = 1500;
    await start(fake, dir);
    await sleep(20);
    await fake.prompt("Your task is the story file ... Progress note from your previous attempt: resume");
    await fake.idle();
    await sleep(200);
    assert.deepEqual(fake.runtimeErrors, []);
    assert.equal(count(fake.texts(), "WORKER-CKPT"), 1, "delivered exactly once");
    assert.equal(fake.messages.filter((m) => m.role === "user").length, 1, "inside the resume turn");
    await fake.emit("session_shutdown", {});
  });
  rmSync(dir, { recursive: true, force: true });
});

test("worker resume: a checkpoint landing after the run started goes as a followUp into that run", async () => {
  const dir = mkdtempSync(join(tmpdir(), "coxpi-wlate-"));
  const epic = join(dir, "epic");
  mkdirSync(join(epic, "handoffs"), { recursive: true });
  writeFileSync(join(epic, "handoffs", "w1.md"), "saved\n");
  const { bin } = coxStub(dir, { sessionOut: "LATE-CKPT", sessionDelay: 0.3 });
  await withEnv({ COX_STORY: "w1", COX_EPIC: epic, COX_BIN: bin }, async () => {
    const fake = new FakePiSession();
    fake.turnMs = 3000; // the checkpoint (~0.3-0.8s) lands while this run streams
    await start(fake, dir);
    await sleep(20);
    await fake.prompt("resume");
    await fake.idle();
    assert.deepEqual(fake.runtimeErrors, []);
    assert.equal(count(fake.texts(), "LATE-CKPT"), 1);
    assert.equal(fake.messages.filter((m) => m.role === "user").length, 2, "launch + the queued followUp, same run");
    await fake.emit("session_shutdown", {});
  });
  rmSync(dir, { recursive: true, force: true });
});

test("a rejected idle send is requeued and delivered on the next turn exactly once (q001 requirement 1)", async () => {
  const dir = mkdtempSync(join(tmpdir(), "coxpi-reject-"));
  const epic = join(dir, "epic");
  mkdirSync(join(epic, "handoffs"), { recursive: true });
  writeFileSync(join(epic, "handoffs", "w1.md"), "saved\n");
  const { bin } = coxStub(dir, { sessionOut: "RETRY-CKPT" });
  await withEnv({ COX_STORY: "w1", COX_EPIC: epic, COX_BIN: bin }, async () => {
    const fake = new FakePiSession();
    fake.preflightMs = 300;
    await start(fake, dir); // no launch prompt: after the grace the checkpoint is sent as an idle prompt
    await waitFor(() => fake.promptAttempts >= 1); // our prompt entered its preflight
    const foreign = fake.foreignRun("FOREIGN", 500); // Pi starts a run we cannot see coming: ours will be rejected
    await foreign;
    await fake.idle();
    await sleep(200);
    assert.equal(fake.runtimeErrors.length, 1, `exactly the one forced rejection, no spam: ${fake.runtimeErrors}`);
    assert.equal(count(fake.texts(), "RETRY-CKPT"), 1, "requeued and delivered exactly once");
    await fake.emit("session_shutdown", {});
  });
  rmSync(dir, { recursive: true, force: true });
});

test("a run Pi cannot start (broken model) never loops: prompts and hook spawns stay bounded (F-5)", async () => {
  const dir = mkdtempSync(join(tmpdir(), "coxpi-broken-"));
  const { bin, log } = coxStub(dir); // never acked: stop-rewake keeps exiting 2
  await withEnv({ COX_ROLE: "leader", COX_BIN: bin }, async () => {
    const fake = new FakePiSession();
    fake.brokenModel = true;
    await start(fake, dir);
    await sleep(1200);
    assert.ok(fake.promptAttempts <= 2, `prompt attempts bounded: ${fake.promptAttempts}`);
    assert.ok(calls(log, "hook stop-rewake") <= 3, `stop-rewake bounded: ${calls(log, "hook stop-rewake")}`);
    assert.ok(calls(log, "hook prompt-drain") <= 2, `prompt-drain bounded: ${calls(log, "hook prompt-drain")}`);
    await fake.emit("session_shutdown", {});
  });
  rmSync(dir, { recursive: true, force: true });
});

test("reopen EPISODE budget: a wedged watcher the leader does not repair costs at most 3 reopen turns + one warning (finding 8)", async () => {
  const dir = mkdtempSync(join(tmpdir(), "coxpi-episode-"));
  const { bin, log } = coxStub(dir, { rewake: "repair" });
  await withEnv({ COX_ROLE: "leader", COX_BIN: bin }, async () => {
    const fake = new FakePiSession();
    await start(fake, dir); // bare restart: the first reopen goes after the grace
    await waitFor(() => fake.notifications.some((n) => n.includes("pausing reopen turns")), 15000);
    await sleep(3000); // past the budget: any further reopen turn or spin would show here
    const reopenTurns = fake.messages.filter((m) => m.role === "user" && m.text.includes("Watcher for beta")).length;
    assert.equal(reopenTurns, 3, `exactly REOPEN_BUDGET reopen turns, got ${reopenTurns}`);
    assert.equal(fake.notifications.filter((n) => n.includes("pausing reopen turns")).length, 1, "one visible warning");
    assert.deepEqual(fake.runtimeErrors, []);
    const drains = readFileSync(log, "utf8").split("\n").filter((l) => l.startsWith("hook prompt-drain"));
    assert.ok(drains.length >= 3 && drains.every((l) => l.includes("--reopen")), `reopen-opened turns keep the Go budget: ${drains}`);
    const spawns = calls(log, "hook stop-rewake");
    assert.ok(spawns <= 7, `re-arms back off past the budget: ${spawns} stop-rewake spawns`);
    // A user prompt ends the episode: the next reopen is delivered again.
    await fake.prompt("captain: what is going on?");
    await fake.idle();
    await waitFor(() => fake.messages.filter((m) => m.role === "user" && m.text.includes("Watcher for beta")).length > reopenTurns, 15000);
    const after = fake.messages.filter((m) => m.role === "user" && m.text.includes("Watcher for beta")).length;
    assert.ok(after > reopenTurns, "a user prompt resets the episode");
    const userDrain = readFileSync(log, "utf8").split("\n").filter((l) => l.startsWith("hook prompt-drain") && !l.includes("--reopen"));
    assert.equal(userDrain.length, 1, "the user turn's prompt-drain resets the Go budget (no --reopen)");
    await fake.emit("session_shutdown", {});
  });
  rmSync(dir, { recursive: true, force: true });
});

test("reopen episode: a new wake gen starts a new episode (finding 8)", async () => {
  const dir = mkdtempSync(join(tmpdir(), "coxpi-newgen-"));
  const { bin } = coxStub(dir, { rewake: "gen" });
  writeFileSync(join(dir, "gen"), "7");
  await withEnv({ COX_ROLE: "leader", COX_BIN: bin }, async () => {
    const fake = new FakePiSession();
    await start(fake, dir);
    await waitFor(() => fake.notifications.some((n) => n.includes("pausing reopen turns")), 15000);
    const gen7 = fake.messages.filter((m) => m.role === "user" && m.text.includes("[gen 7]")).length;
    assert.equal(gen7, 3, `the unacked gen 7 episode stops at the budget, got ${gen7}`);
    writeFileSync(join(dir, "gen"), "8"); // a new wake arrives
    await waitFor(() => fake.messages.some((m) => m.role === "user" && m.text.includes("[gen 8]")), 20000);
    const gen8 = fake.messages.filter((m) => m.role === "user" && m.text.includes("[gen 8]")).length;
    assert.ok(gen8 >= 1, "a new wake gen is delivered despite the spent episode");
    await fake.emit("session_shutdown", {});
  });
  rmSync(dir, { recursive: true, force: true });
});

test("a stop-rewake that returns 0 at once (a watcher restart that dies) is re-armed with backoff, note shown once (F-10)", async () => {
  const dir = mkdtempSync(join(tmpdir(), "coxpi-backoff-"));
  const { bin, log } = coxStub(dir, { rewake: "restart0" });
  await withEnv({ COX_ROLE: "leader", COX_BIN: bin }, async () => {
    const fake = new FakePiSession();
    await start(fake, dir);
    await sleep(3500);
    const spawns = calls(log, "hook stop-rewake");
    assert.ok(spawns >= 2 && spawns <= 4, `bounded re-arm rate (1s, 2s, ... backoff): ${spawns} spawns in 3.5s`);
    assert.equal(fake.notifications.filter((n) => n.includes("restarted it")).length, 1, "the note is surfaced once");
    assert.equal(fake.messages.length, 0, "no model turn");
    await fake.emit("session_shutdown", {});
  });
  rmSync(dir, { recursive: true, force: true });
});

test("the Go block-budget note (a changing count) is shown once per episode, re-arms back off (AC5 run 1 rerun)", async () => {
  const dir = mkdtempSync(join(tmpdir(), "coxpi-budgetnote-"));
  const { bin, log } = coxStub(dir, { rewake: "budget0" });
  await withEnv({ COX_ROLE: "leader", COX_BIN: bin }, async () => {
    const fake = new FakePiSession();
    await start(fake, dir);
    await waitFor(() => calls(log, "hook stop-rewake") >= 3, 15000);
    const notes = fake.notifications.filter((n) => n.includes("still not alive after"));
    assert.equal(notes.length, 1, `one visible warning, not one per block: ${notes}`);
    assert.equal(fake.messages.length, 0, "no model turn");
    await fake.emit("session_shutdown", {});
  });
  rmSync(dir, { recursive: true, force: true });
});
