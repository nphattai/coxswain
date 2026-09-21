// Behavioral tests for the cox-pi wiring's session_start checkpoint injection. Run with node --test.
//
// FAIL_TO_PASS (dogfood W1): a fresh worker (no saved checkpoint) must NOT receive a session-start followUp. The old
// wiring always delivered `cox hook session-start` stdout - which for a fresh session is only the "No checkpoint yet,
// start from the story" notice - as a followUp, queuing a second turn behind the launch prompt so the worker redid the
// whole task and emitted a duplicate completion (two worker_done for one dispatch). A saved checkpoint (resume) must
// still be injected.
import { test } from "node:test";
import assert from "node:assert/strict";
import { mkdtempSync, mkdirSync, writeFileSync, rmSync, chmodSync, existsSync } from "node:fs";
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
