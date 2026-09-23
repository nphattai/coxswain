// Behavioral tests for the cox-pi wiring's session_start checkpoint injection. Run with node --test.
//
// FAIL_TO_PASS (dogfood W1): a fresh worker (no saved checkpoint) must NOT receive a session-start followUp. The old
// wiring always delivered `cox hook session-start` stdout - which for a fresh session is only the "No checkpoint yet,
// start from the story" notice - as a followUp, queuing a second turn behind the launch prompt so the worker redid the
// whole task and emitted a duplicate completion (two worker_done for one dispatch). A saved checkpoint (resume) must
// still be injected.
import { test, beforeEach } from "node:test";
import assert from "node:assert/strict";
import { mkdtempSync, mkdirSync, writeFileSync, readFileSync, rmSync, chmodSync, existsSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import makeExtension from "./cox-pi.ts";
import { __resetProcessSingleton } from "./cox-supervisor.ts";

// DESIGN item 2: the extension is a process-global singleton. `node --test` runs this file in one process, so every
// makeExtension() call after the first would be inert; reset the claim before each test so each activates cleanly.
beforeEach(__resetProcessSingleton);

type Sent = { text: string; opts: unknown };

function fakePi() {
  const handlers: Record<string, (event: unknown, ctx: unknown) => unknown> = {};
  const sent: Sent[] = [];
  const pi = {
    on: (evt: string, h: (event: unknown, ctx: unknown) => unknown) => {
      handlers[evt] = h;
    },
    sendUserMessage: (text: string, opts: unknown) => {
      sent.push({ text, opts });
    },
  };
  return { pi, handlers, sent };
}

const ACTIVATED = join(import.meta.dirname, ".cox-pi.activated");
function cleanupMarker() {
  try {
    rmSync(ACTIVATED);
  } catch {
    /* not written */
  }
}

async function waitFor(pred: () => boolean, ms = 2000): Promise<void> {
  const t0 = Date.now();
  while (!pred() && Date.now() - t0 < ms) {
    await new Promise((r) => setTimeout(r, 10));
  }
}

test("fresh worker (no checkpoint) gets NO session-start followUp", async () => {
  const epic = mkdtempSync(join(tmpdir(), "coxpi-fresh-"));
  // Faithful stub of real cox on a no-checkpoint story: it prints the "start from the story" notice to stdout and exits
  // 0, and drops a marker proving it ran. The OLD wiring calls it and delivers that stdout as a followUp (test fails);
  // the FIXED wiring never invokes it on a fresh session because no handoffs/<story>.md exists (no marker, no send).
  const stub = join(epic, "coxstub.sh");
  const ranMarker = join(epic, "cox-ran");
  writeFileSync(stub, `#!/bin/sh\ntouch '${ranMarker}'\necho 'No checkpoint yet. Start from the story.'\n`);
  chmodSync(stub, 0o755);
  const prev = { story: process.env.COX_STORY, epic: process.env.COX_EPIC, role: process.env.COX_ROLE, bin: process.env.COX_BIN };
  process.env.COX_STORY = "w1";
  process.env.COX_EPIC = epic;
  delete process.env.COX_ROLE; // worker
  process.env.COX_BIN = stub; // real cox would print a notice + exit 0 here; the fix must not invoke it
  try {
    const { pi, handlers, sent } = fakePi();
    makeExtension(pi as never);
    await handlers["session_start"]?.({}, { cwd: epic, ui: { notify: () => {} } });
    // Wait well past the observed execFile latency: on the OLD path the stub would have run (marker) and delivered a
    // followUp by now; on the FIXED path neither ever happens.
    await waitFor(() => sent.length > 0 || existsSync(ranMarker), 1500);
    assert.equal(existsSync(ranMarker), false, "a fresh worker must not invoke the session-start hook");
    assert.equal(sent.length, 0, "a fresh worker must not receive an injected followUp");
  } finally {
    cleanupMarker();
    rmSync(epic, { recursive: true, force: true });
    Object.assign(process.env, {}); // restore
    if (prev.story === undefined) delete process.env.COX_STORY; else process.env.COX_STORY = prev.story;
    if (prev.epic === undefined) delete process.env.COX_EPIC; else process.env.COX_EPIC = prev.epic;
    if (prev.role === undefined) delete process.env.COX_ROLE; else process.env.COX_ROLE = prev.role;
    if (prev.bin === undefined) delete process.env.COX_BIN; else process.env.COX_BIN = prev.bin;
  }
});

test("resuming worker (checkpoint present) gets the checkpoint in its next turn's context, exactly once", async () => {
  const epic = mkdtempSync(join(tmpdir(), "coxpi-resume-"));
  mkdirSync(join(epic, "handoffs"), { recursive: true });
  writeFileSync(join(epic, "handoffs", "w1.md"), "checkpoint body\n");
  // Stub cox: print the injected text regardless of args, so the wiring's delivery is observable.
  const stub = join(epic, "coxstub.sh");
  const answered = join(epic, "answered");
  writeFileSync(stub, `#!/bin/sh\necho INJECTED-CHECKPOINT-TEXT\ntouch '${answered}'\n`);
  chmodSync(stub, 0o755);
  const prev = { story: process.env.COX_STORY, epic: process.env.COX_EPIC, role: process.env.COX_ROLE, bin: process.env.COX_BIN };
  process.env.COX_STORY = "w1";
  process.env.COX_EPIC = epic;
  delete process.env.COX_ROLE; // worker
  process.env.COX_BIN = stub;
  try {
    const { pi, handlers, sent } = fakePi();
    makeExtension(pi as never);
    const ctx = { cwd: epic, ui: { notify: () => {} } };
    await handlers["session_start"]?.({}, ctx);
    await waitFor(() => existsSync(answered), 3000);
    await new Promise((r) => setTimeout(r, 200)); // the session-start child's output reached the extension
    // A launch/resume prompt is about to start: the checkpoint rides its context (never a separate, rejectable prompt).
    const res = (await handlers["before_agent_start"]?.({ prompt: "resume" }, ctx)) as { message?: { content: string } } | undefined;
    assert.match(res?.message?.content ?? "", /INJECTED-CHECKPOINT-TEXT/);
    assert.equal(sent.length, 0, "no sendUserMessage: nothing that Pi could reject");
    const again = (await handlers["before_agent_start"]?.({ prompt: "next" }, ctx)) as { message?: unknown } | undefined;
    assert.equal(again, undefined, "handed over once (inflight until confirmed, not re-injected)");
  } finally {
    cleanupMarker();
    rmSync(epic, { recursive: true, force: true });
    if (prev.story === undefined) delete process.env.COX_STORY; else process.env.COX_STORY = prev.story;
    if (prev.epic === undefined) delete process.env.COX_EPIC; else process.env.COX_EPIC = prev.epic;
    if (prev.role === undefined) delete process.env.COX_ROLE; else process.env.COX_ROLE = prev.role;
    if (prev.bin === undefined) delete process.env.COX_BIN; else process.env.COX_BIN = prev.bin;
  }
});

// --- Harness-owned busy state (DESIGN wave-3 item 2) ---
// FAIL_TO_PASS: on the old wiring the extension never wrote the busy record, so a backend could not consult it. The
// fixed wiring Applies busy on agent_start and idle on agent_settled using COX_BUSY_GEN, and writes NOTHING when the gen
// is absent (never a guess).

// busyEnv sets the worker env the launch seam exports, runs body, then restores every key.
async function busyEnv(
  vars: { story?: string; epic?: string; gen?: string; bin?: string; role?: string },
  body: () => Promise<void>,
): Promise<void> {
  const keys = ["COX_STORY", "COX_EPIC", "COX_BUSY_GEN", "COX_BIN", "COX_ROLE"] as const;
  const prev: Record<string, string | undefined> = {};
  for (const k of keys) prev[k] = process.env[k];
  const set = (k: (typeof keys)[number], v: string | undefined) => {
    if (v === undefined) delete process.env[k];
    else process.env[k] = v;
  };
  set("COX_STORY", vars.story);
  set("COX_EPIC", vars.epic);
  set("COX_BUSY_GEN", vars.gen);
  set("COX_BIN", vars.bin);
  set("COX_ROLE", vars.role);
  try {
    await body();
  } finally {
    for (const k of keys) set(k, prev[k]);
    cleanupMarker();
  }
}

// logStub writes an executable that appends its argv (one line) to logPath, so a test asserts what cox was invoked with.
function logStub(dir: string, logPath: string): string {
  const stub = join(dir, "coxlog.sh");
  writeFileSync(stub, `#!/bin/sh\necho "$*" >> '${logPath}'\n`);
  chmodSync(stub, 0o755);
  return stub;
}

test("agent_start -> busy, agent_settled -> idle (worker, gen present)", async () => {
  const epic = mkdtempSync(join(tmpdir(), "coxpi-busy-"));
  const log = join(epic, "cox.log");
  const stub = logStub(epic, log);
  await busyEnv({ story: "w1", epic, gen: "g123", bin: stub }, async () => {
    const { pi, handlers } = fakePi();
    makeExtension(pi as never);
    await handlers["agent_start"]?.({}, {});
    await waitFor(() => existsSync(log) && readFileSync(log, "utf8").includes("agent_start"), 1500);
    // A race loser's settle (another run still holds the agent: ctx.signal set) is not the turn's end: no idle write.
    await handlers["agent_settled"]?.({}, { isIdle: () => true, signal: new AbortController().signal });
    await new Promise((r) => setTimeout(r, 300));
    assert.ok(!readFileSync(log, "utf8").includes("agent_settled"), "a race loser's settle must not report idle");
    await handlers["agent_settled"]?.({}, { isIdle: () => true, signal: undefined }); // the quiet settle
    await waitFor(() => readFileSync(log, "utf8").includes("agent_settled"), 1500);
    const lines = readFileSync(log, "utf8");
    assert.match(lines, /busy apply w1 busy --gen g123 --source pi-ext --event agent_start --epic/, "agent_start must apply busy with the env gen");
    assert.match(lines, /busy apply w1 idle --gen g123 --source pi-ext --event agent_settled --epic/, "agent_settled must apply idle with the env gen");
  });
  rmSync(epic, { recursive: true, force: true });
});

test("no COX_BUSY_GEN -> no busy write (never a guess)", async () => {
  const epic = mkdtempSync(join(tmpdir(), "coxpi-nogen-"));
  const log = join(epic, "cox.log");
  const stub = logStub(epic, log);
  await busyEnv({ story: "w1", epic, gen: undefined, bin: stub }, async () => {
    const { pi, handlers } = fakePi();
    makeExtension(pi as never);
    await handlers["agent_start"]?.({}, {});
    await handlers["agent_settled"]?.({}, { isIdle: () => true, signal: undefined });
    // Wait past the exec latency the gen-present case needed. The stub logs every cox call (the turn's interrupt-wait
    // child may run too); with no gen there must be no busy write among them.
    await new Promise((r) => setTimeout(r, 400));
    const calls = existsSync(log) ? readFileSync(log, "utf8") : "";
    assert.ok(!calls.includes("busy apply"), `a session with no armed gen must write no busy record: ${calls}`);
  });
  rmSync(epic, { recursive: true, force: true });
});

// --- Interrupt through the harness (DESIGN wave-3 item 4) ---
// FAIL_TO_PASS: the old wiring had no worker interrupt path, so a durable interrupt record could not abort a Pi turn. The
// fixed wiring spawns `cox inbox interrupt-wait` per turn and calls ctx.abort() when it exits 0 (an interrupt arrived).

// interruptStub writes a stub whose `inbox interrupt-wait` invocation behaves as `interrupt`: "now" exits 0 (an
// interrupt arrived), "block" sleeps (no interrupt). Every other cox call (busy apply) exits 0.
function interruptStub(dir: string, mode: "now" | "block"): string {
  const stub = join(dir, "coxint.sh");
  const body = mode === "now" ? "exit 0" : "sleep 5; exit 3";
  writeFileSync(stub, `#!/bin/sh\nif [ "$1" = inbox ]; then ${body}; fi\nexit 0\n`);
  chmodSync(stub, 0o755);
  return stub;
}

test("a turn is aborted when an interrupt record arrives (interrupt-wait exits 0)", async () => {
  const epic = mkdtempSync(join(tmpdir(), "coxpi-int-"));
  const stub = interruptStub(epic, "now");
  await busyEnv({ story: "w1", epic, gen: "g1", bin: stub }, async () => {
    let aborted = false;
    const { pi, handlers } = fakePi();
    makeExtension(pi as never);
    await handlers["agent_start"]?.({}, { abort: () => { aborted = true; } });
    await waitFor(() => aborted, 1500);
    assert.equal(aborted, true, "an interrupt record (interrupt-wait exit 0) must abort the running turn");
  });
  rmSync(epic, { recursive: true, force: true });
});

test("no interrupt -> the turn is not aborted", async () => {
  const epic = mkdtempSync(join(tmpdir(), "coxpi-noint-"));
  const stub = interruptStub(epic, "block");
  await busyEnv({ story: "w1", epic, gen: "g1", bin: stub }, async () => {
    let aborted = false;
    const { pi, handlers } = fakePi();
    makeExtension(pi as never);
    await handlers["agent_start"]?.({}, { abort: () => { aborted = true; } });
    await new Promise((r) => setTimeout(r, 400));
    assert.equal(aborted, false, "with no interrupt record the turn must keep running");
    await handlers["agent_settled"]?.({}, {}); // retire the still-blocking interrupt-wait child
  });
  rmSync(epic, { recursive: true, force: true });
});

// --- Leader hook parity (DESIGN item 4) ---
// FAIL_TO_PASS: the old leader wiring hand-rolled a single-epic `cox wake wait --epic <e>` loop and a static nudge. The
// fixed wiring drives the Go-owned leader hooks - `cox hook prompt-drain | stop-rewake` (unbound, so the Go side
// supervises every active epic), threads ORCA_TERMINAL_HANDLE so the block budget stays ON (leader ruling #1), retires
// the idle child when a turn starts (leader ruling #2), and caps per-turn reopens.

// leaderEnv sets a LEADER launch env (no story => leader), runs body, restores every key.
async function leaderEnv(
  vars: { epic?: string; bin: string; handle?: string },
  body: () => Promise<void>,
): Promise<void> {
  const keys = ["COX_STORY", "COX_EPIC", "COX_ROLE", "COX_BIN", "ORCA_TERMINAL_HANDLE", "COX_BUSY_GEN"] as const;
  const prev: Record<string, string | undefined> = {};
  for (const k of keys) prev[k] = process.env[k];
  const set = (k: (typeof keys)[number], v: string | undefined) => {
    if (v === undefined) delete process.env[k];
    else process.env[k] = v;
  };
  set("COX_STORY", undefined);
  set("COX_EPIC", vars.epic);
  set("COX_ROLE", "leader");
  set("COX_BIN", vars.bin);
  set("ORCA_TERMINAL_HANDLE", vars.handle);
  set("COX_BUSY_GEN", undefined);
  try {
    await body();
  } finally {
    for (const k of keys) set(k, prev[k]);
    cleanupMarker();
  }
}

// leaderStub writes a fake `cox` that logs each invocation (argv + inherited ORCA_TERMINAL_HANDLE) and dispatches:
//   - `hook stop-rewake`: "forever" always exits 2 (reopen), "once" exits 2 the first time then blocks, "block" blocks.
//   - `hook prompt-drain`: prints drainOut and exits 0.
//   - everything else (session-start, precompact, busy): exits 0 with no output.
function leaderStub(
  dir: string,
  opts: { log: string; count?: string; rewake: "forever" | "once" | "block"; drainOut?: string },
): string {
  const drainOut = opts.drainOut ?? "WAKES: epicA / epicB";
  let rewakeBody: string;
  if (opts.rewake === "forever") rewakeBody = `echo 'REOPEN-NUDGE' 1>&2; exit 2`;
  else if (opts.rewake === "once")
    rewakeBody = `n=$(cat '${opts.count}' 2>/dev/null||echo 0); n=$((n+1)); echo $n>'${opts.count}'; if [ "$n" = 1 ]; then echo 'REOPEN-NUDGE' 1>&2; exit 2; fi; sleep 5; exit 3`;
  else rewakeBody = `sleep 5; exit 3`;
  const stub = join(dir, "coxleader.sh");
  writeFileSync(
    stub,
    `#!/bin/sh\n` +
      `echo "ARGS $* HANDLE=$ORCA_TERMINAL_HANDLE" >> '${opts.log}'\n` +
      `if [ "$1" = hook ] && [ "$2" = stop-rewake ]; then ${rewakeBody}; fi\n` +
      `if [ "$1" = hook ] && [ "$2" = prompt-drain ]; then printf '%s' '${drainOut}'; exit 0; fi\n` +
      `exit 0\n`,
  );
  chmodSync(stub, 0o755);
  return stub;
}

test("leader: stop-rewake runs unbound (no --epic) with --harness pi and a handle; its reopen is held, not sent blind", async () => {
  const dir = mkdtempSync(join(tmpdir(), "coxpi-ldr-once-"));
  const log = join(dir, "cox.log");
  const bin = leaderStub(dir, { log, count: join(dir, "cnt"), rewake: "once" });
  await leaderEnv({ epic: undefined, bin }, async () => {
    const { pi, handlers, sent } = fakePi();
    makeExtension(pi as never);
    await handlers["session_start"]?.({}, { cwd: dir, ui: { notify: () => {} } }); // arms the stop-rewake child
    await waitFor(() => existsSync(join(dir, "cnt")), 2000);
    await new Promise((r) => setTimeout(r, 300));
    // This fake ctx cannot report Pi's state (no isIdle/signal), so the Outbox never sends blind (F-9).
    assert.equal(sent.length, 0);
    const logtext = readFileSync(log, "utf8");
    assert.match(logtext, /ARGS hook stop-rewake --harness pi HANDLE=\S+/, "stop-rewake carries --harness pi + a handle");
    assert.ok(!/stop-rewake[^\n]*--epic/.test(logtext), "an unbound leader's stop-rewake has no --epic (workspace mode)");
    assert.equal((logtext.match(/stop-rewake/g) ?? []).length, 1, "a reopen spawns no successor child (F-5)");
    await handlers["session_shutdown"]?.({}, {});
  });
  rmSync(dir, { recursive: true, force: true });
});

test("leader: the reopen budget counts REAL turns - a prompt that never streams does not reset it (F-5)", async () => {
  const dir = mkdtempSync(join(tmpdir(), "coxpi-ldr-budget-"));
  const log = join(dir, "cox.log");
  const bin = leaderStub(dir, { log, rewake: "forever" });
  const warns: string[] = [];
  // A quiet ctx (idle, no agent run): every settle re-arms the idle waiter, which exits 2 again at once.
  const ctx = { cwd: dir, ui: { notify: (m: string) => warns.push(m) }, isIdle: () => true, signal: undefined };
  await leaderEnv({ epic: undefined, bin }, async () => {
    const { pi, handlers } = fakePi();
    makeExtension(pi as never);
    await handlers["session_start"]?.({}, ctx);
    await waitFor(() => existsSync(log), 2000);
    await new Promise((r) => setTimeout(r, 400));
    assert.equal((readFileSync(log, "utf8").match(/stop-rewake/g) ?? []).length, 1, "no successor spawn before a settle");
    for (let i = 0; i < 5 && warns.length === 0; i++) {
      await waitFor(() => existsSync(log) && (readFileSync(log, "utf8").match(/stop-rewake/g) ?? []).length > i, 2000);
      await new Promise((r) => setTimeout(r, 100)); // the child's exit-2 callback has run
      // A prompt enters, drains, and settles WITHOUT agent_start (rejected / never streamed).
      await handlers["before_agent_start"]?.({ prompt: "x" }, ctx);
      await handlers["agent_settled"]?.({}, ctx);
    }
    await waitFor(() => warns.length > 0, 2000);
    assert.match(warns[0], /pausing reopen turns/, "the reopen budget trips instead of spinning");
    const spawns = (readFileSync(log, "utf8").match(/stop-rewake/g) ?? []).length;
    assert.ok(spawns <= 5, `bounded: ${spawns} stop-rewake spawns`);
    await handlers["agent_start"]?.({}, ctx); // a real turn resets it
    await handlers["session_shutdown"]?.({}, {});
  });
  rmSync(dir, { recursive: true, force: true });
});

test("leader: before_agent_start runs prompt-drain (unbound) and injects its stdout as turn context", async () => {
  const dir = mkdtempSync(join(tmpdir(), "coxpi-ldr-drain-"));
  const log = join(dir, "cox.log");
  const bin = leaderStub(dir, { log, rewake: "block", drainOut: "WAKES: epicA / epicB" });
  await leaderEnv({ epic: undefined, bin }, async () => {
    const { pi, handlers } = fakePi();
    makeExtension(pi as never);
    const res = (await handlers["before_agent_start"]?.({ prompt: "hi" }, { cwd: dir, ui: { notify: () => {} } })) as
      | { message?: { customType: string; content: string; display: boolean } }
      | undefined;
    assert.ok(res && res.message, "before_agent_start returns an injected message");
    assert.equal(res!.message!.customType, "cox-wakes");
    assert.match(res!.message!.content, /WAKES: epicA \/ epicB/);
    assert.equal(res!.message!.display, true);
    const logtext = readFileSync(log, "utf8");
    assert.match(logtext, /hook prompt-drain HANDLE=\S+/, "prompt-drain runs unbound (no --epic) with a handle");
    assert.ok(!/prompt-drain[^\n]*--epic/.test(logtext), "an unbound leader's prompt-drain has no --epic");
  });
  rmSync(dir, { recursive: true, force: true });
});

test("leader: a starting turn supersedes the pending idle child -> one delivery, never two (leader ruling #2)", async () => {
  const dir = mkdtempSync(join(tmpdir(), "coxpi-ldr-supersede-"));
  const log = join(dir, "cox.log");
  const bin = leaderStub(dir, { log, rewake: "block", drainOut: "DRAINED-ONCE" });
  await leaderEnv({ epic: undefined, bin }, async () => {
    const { pi, handlers, sent } = fakePi();
    makeExtension(pi as never);
    await handlers["session_start"]?.({}, { cwd: dir, ui: { notify: () => {} } }); // arms a blocking stop-rewake child
    const res = (await handlers["before_agent_start"]?.({ prompt: "hi" }, { cwd: dir, ui: { notify: () => {} } })) as
      | { message?: { content: string } }
      | undefined;
    assert.match(res!.message!.content, /DRAINED-ONCE/, "before_agent_start drains once");
    await new Promise((r) => setTimeout(r, 300));
    assert.equal(sent.length, 0, "the superseded stop-rewake child must not also deliver a followUp");
    await handlers["session_shutdown"]?.({}, {});
  });
  rmSync(dir, { recursive: true, force: true });
});

// --- Single cox extension per process (DESIGN item 2) ---
// FAIL_TO_PASS: without the singleton guard a second activation in one process wires a second set of handlers (a second
// wake child, a second busy writer, a duplicate checkpoint per event). The fix makes the second activation inert.
test("a second cox extension activation in the same process is inert", async () => {
  const first = fakePi();
  makeExtension(first.pi as never); // claims the process
  const second = fakePi();
  makeExtension(second.pi as never); // must be a no-op
  assert.ok(Object.keys(first.handlers).length > 0, "the first activation wires its lifecycle handlers");
  assert.equal(Object.keys(second.handlers).length, 0, "the second activation registers no handlers");
  assert.equal(second.sent.length, 0, "the second activation delivers nothing");
});

test("bound leader (COX_EPIC set): prompt-drain and stop-rewake narrow to the one epic", async () => {
  const dir = mkdtempSync(join(tmpdir(), "coxpi-ldr-bound-"));
  const log = join(dir, "cox.log");
  const bin = leaderStub(dir, { log, rewake: "block", drainOut: "X" });
  await leaderEnv({ epic: "/bound/epic", bin }, async () => {
    const { pi, handlers } = fakePi();
    makeExtension(pi as never);
    await handlers["before_agent_start"]?.({ prompt: "hi" }, { cwd: dir, ui: { notify: () => {} } });
    await handlers["session_start"]?.({}, { cwd: dir, ui: { notify: () => {} } }); // arms stop-rewake (bound)
    await new Promise((r) => setTimeout(r, 150));
    const logtext = readFileSync(log, "utf8");
    assert.match(logtext, /hook prompt-drain --epic \/bound\/epic/, "a bound leader's prompt-drain carries --epic");
    assert.match(logtext, /hook stop-rewake --harness pi --epic \/bound\/epic/, "a bound leader's stop-rewake carries --epic");
    await handlers["session_shutdown"]?.({}, {});
  });
  rmSync(dir, { recursive: true, force: true });
});

// FAIL_TO_PASS (dogfood F-4): every `cox` child runs with stdin CLOSED. The old wiring used execFile, which leaves stdin
// an open pipe; `cox hook prompt-drain` read it for the harness envelope and the leader's first turn hung forever. This
// stub reads stdin to EOF before answering, so an open stdin hangs before_agent_start past the deadline.
test("leader: prompt-drain (and every cox child) gets a closed stdin - before_agent_start never hangs", async () => {
  const dir = mkdtempSync(join(tmpdir(), "coxpi-stdin-"));
  const stub = join(dir, "cox");
  writeFileSync(stub, `#!/bin/sh\ncat >/dev/null\nif [ "$2" = prompt-drain ]; then printf 'STDIN-OK'; fi\nif [ "$2" = stop-rewake ]; then exec sleep 30; fi\nexit 0\n`);
  chmodSync(stub, 0o755);
  await leaderEnv({ epic: undefined, bin: stub }, async () => {
    const { pi, handlers } = fakePi();
    makeExtension(pi as never);
    const res = (await Promise.race([
      handlers["before_agent_start"]?.({ prompt: "hi" }, { cwd: dir, ui: { notify: () => {} } }),
      new Promise((r) => setTimeout(() => r("HUNG"), 5000)),
    ])) as { message?: { content: string } } | string;
    assert.notEqual(res, "HUNG", "prompt-drain blocked on an open stdin");
    assert.match((res as { message: { content: string } }).message.content, /STDIN-OK/);
    await handlers["session_shutdown"]?.({}, {});
  });
  rmSync(dir, { recursive: true, force: true });
});

// A settle that arrives after session_shutdown (a run still in flight while the session goes away) re-arms no idle
// waiter: before the guard, it started a new generation that kept reopening into a closed session.
test("leader: nothing re-arms after session_shutdown", async () => {
  const dir = mkdtempSync(join(tmpdir(), "coxpi-shutdown-"));
  const log = join(dir, "cox.log");
  const bin = leaderStub(dir, { log, rewake: "block" });
  const quiet = { cwd: dir, ui: { notify: () => {} }, isIdle: () => true, signal: undefined };
  await leaderEnv({ epic: undefined, bin }, async () => {
    const { pi, handlers } = fakePi();
    makeExtension(pi as never);
    await handlers["session_start"]?.({}, quiet);
    await waitFor(() => existsSync(log) && readFileSync(log, "utf8").includes("stop-rewake"), 2000);
    await handlers["before_agent_start"]?.({ prompt: "x" }, quiet); // a turn starts: the waiter is retired
    await handlers["agent_start"]?.({}, quiet);
    await handlers["session_shutdown"]?.({}, {});
    await handlers["agent_settled"]?.({}, quiet); // the in-flight run settles after the shutdown
    await new Promise((r) => setTimeout(r, 400));
    assert.equal((readFileSync(log, "utf8").match(/stop-rewake/g) ?? []).length, 1, "no waiter re-armed after shutdown");
  });
  rmSync(dir, { recursive: true, force: true });
});
