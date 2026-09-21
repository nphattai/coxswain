// Behavioral tests for the cox-pi wiring's session_start checkpoint injection. Run with node --test.
//
// FAIL_TO_PASS (dogfood W1): a fresh worker (no saved checkpoint) must NOT receive a session-start followUp. The old
// wiring always delivered `cox hook session-start` stdout - which for a fresh session is only the "No checkpoint yet,
// start from the story" notice - as a followUp, queuing a second turn behind the launch prompt so the worker redid the
// whole task and emitted a duplicate completion (two worker_done for one dispatch). A saved checkpoint (resume) must
// still be injected.
import { test } from "node:test";
import assert from "node:assert/strict";
import { mkdtempSync, mkdirSync, writeFileSync, readFileSync, rmSync, chmodSync, existsSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import makeExtension from "./cox-pi.ts";

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

test("resuming worker (checkpoint present) DOES get the injected checkpoint as a followUp", async () => {
  const epic = mkdtempSync(join(tmpdir(), "coxpi-resume-"));
  mkdirSync(join(epic, "handoffs"), { recursive: true });
  writeFileSync(join(epic, "handoffs", "w1.md"), "checkpoint body\n");
  // Stub cox: print the injected text regardless of args, so the wiring's followUp delivery is observable.
  const stub = join(epic, "coxstub.sh");
  writeFileSync(stub, "#!/bin/sh\necho INJECTED-CHECKPOINT-TEXT\n");
  chmodSync(stub, 0o755);
  const prev = { story: process.env.COX_STORY, epic: process.env.COX_EPIC, role: process.env.COX_ROLE, bin: process.env.COX_BIN };
  process.env.COX_STORY = "w1";
  process.env.COX_EPIC = epic;
  delete process.env.COX_ROLE; // worker
  process.env.COX_BIN = stub;
  try {
    const { pi, handlers, sent } = fakePi();
    makeExtension(pi as never);
    await handlers["session_start"]?.({}, { cwd: epic, ui: { notify: () => {} } });
    await waitFor(() => sent.length > 0);
    assert.equal(sent.length, 1, "a resuming worker must receive exactly one injected followUp");
    assert.match(sent[0].text, /INJECTED-CHECKPOINT-TEXT/);
    assert.deepEqual(sent[0].opts, { deliverAs: "followUp" });
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
    await handlers["agent_settled"]?.({}, {});
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
    await handlers["agent_settled"]?.({}, {});
    // Wait past the exec latency the gen-present case needed; with no gen the stub must never run at all.
    await new Promise((r) => setTimeout(r, 400));
    assert.equal(existsSync(log), false, "a session with no armed gen must write no busy record");
  });
  rmSync(epic, { recursive: true, force: true });
});
