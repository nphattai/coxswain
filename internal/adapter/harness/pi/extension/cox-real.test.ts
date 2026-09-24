// Real-binary test: the Pi leader extension exactly as a real `cox workspace init` installs it, driven through the Pi
// 0.86.1 lifecycle (testing-fake-pi.ts) against the REAL built `cox` - no fake cox. Run by tests/pi-extension/run.sh,
// which builds the binary and sets COX_REAL_BIN; skipped (visibly) without it.
//
// FAIL_TO_PASS (dogfood F-4): every other extension test used a fake `cox`, so nothing caught that the real
// `cox hook prompt-drain` blocked on the open stdin pipe the extension gave it: a real Pi leader hung on its first turn.
// Here the first turn must finish within the deadline with the real wake injected.
import { test } from "node:test";
import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { mkdtempSync, mkdirSync, writeFileSync, existsSync, readFileSync, realpathSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { FakePiSession } from "./testing-fake-pi.ts";

const bin = process.env.COX_REAL_BIN ?? "";

// withDeadline fails the test instead of hanging when the real cox blocks.
async function withDeadline<T>(p: Promise<T>, ms: number, what: string): Promise<T> {
  let timer: NodeJS.Timeout | undefined;
  const dead = new Promise<never>((_, reject) => {
    timer = setTimeout(() => reject(new Error(`${what} did not finish within ${ms}ms (a cox child blocked?)`)), ms);
  });
  try {
    return await Promise.race([p, dead]);
  } finally {
    clearTimeout(timer);
  }
}

test("real cox: leader first turn drains a real wake, precompact writes the leader checkpoint, restart injects it", { skip: bin ? false : "COX_REAL_BIN not set (tests/pi-extension/run.sh builds it)" }, async () => {
  const root = realpathSync(mkdtempSync(join(tmpdir(), "coxpi-real-")));
  const home = join(root, "home");
  const repo = join(root, "repo");
  const ws = join(root, "ws");
  mkdirSync(home);
  mkdirSync(repo);
  const clean: NodeJS.ProcessEnv = { ...process.env, HOME: home };
  for (const k of ["COX_EPIC", "COX_STORY", "COX_ROLE", "COX_BUSY_GEN", "COX_PLANE", "ORCA_RUN_ID", "ORCA_TERMINAL_HANDLE", "COX_ROOTS"]) delete clean[k];
  const sh = (cmd: string, args: string[], cwd = root) =>
    execFileSync(cmd, args, { cwd, env: clean, stdio: ["ignore", "pipe", "pipe"], timeout: 30000 }).toString();

  sh("git", ["-C", repo, "init", "-q", "-b", "main"]);
  sh("git", ["-C", repo, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "--allow-empty", "-m", "init"]);
  sh(bin, ["workspace", "init", "--root", ws, "--repo", `a=${repo}:main`]);
  const ext = join(ws, ".pi", "extensions", "coxswain", "index.ts");
  assert.ok(existsSync(ext), "cox workspace init installs the unbound Pi leader extension");

  // One active epic with a real wake: a live watch.pid (this process) and a fresh beacon, so the stop-rewake guard sees
  // a healthy watcher and never tries to launch one.
  const epic = join(ws, "proj", "epics", "e1");
  mkdirSync(join(epic, "stories"), { recursive: true });
  writeFileSync(join(epic, "stories", "s1.md"), "---\nid: s1\n---\nbody\n");
  sh(bin, ["story", "report", "status", "--epic", epic, "--story", "s1", "--note", "REAL-WAKE"]);
  mkdirSync(join(epic, ".cox", "watch"), { recursive: true });
  writeFileSync(join(epic, ".cox", "watch.pid"), String(process.pid));
  writeFileSync(join(epic, ".cox", "watch", "lasttick"), new Date().toISOString());

  const prevEnv = { ...process.env };
  const prevCwd = process.cwd();
  for (const k of Object.keys(process.env)) delete process.env[k];
  Object.assign(process.env, clean, { COX_BIN: bin });
  process.chdir(ws); // Pi runs in the workspace; the unbound leader's hooks resolve it from the cwd
  try {
    const { default: makeExtension } = await import(ext);
    const { __resetProcessSingleton } = await import(join(ws, ".pi", "extensions", "coxswain", "cox-supervisor.ts"));

    // Session 1: the launch prompt's turn gets the real wake, with the epic dir, in its context.
    __resetProcessSingleton();
    const s1 = new FakePiSession();
    makeExtension(s1.api as never);
    await s1.emit("session_start", { reason: "startup" });
    await withDeadline(s1.prompt("you are the leader"), 20000, "the first leader turn");
    await withDeadline(s1.idle(), 20000, "settling the first turn");
    const ctx1 = s1.messages.find((m) => m.role === "custom")?.text ?? "";
    assert.match(ctx1, /REAL-WAKE/, "the real prompt-drain injected the real wake");
    assert.ok(ctx1.includes(`--epic ${epic}`), `the wake header names the epic dir (F-8): ${ctx1}`);
    assert.deepEqual(s1.runtimeErrors, [], "no rejected prompt");

    // Compaction: the real precompact persists the per-epic leader checkpoint.
    await withDeadline(s1.emit("session_before_compact", {}), 20000, "precompact");
    const ckpt = join(epic, "handoffs", "_leader.md");
    assert.ok(existsSync(ckpt), "precompact wrote the leader checkpoint");
    assert.match(readFileSync(ckpt, "utf8"), /story: _leader/);
    await s1.emit("session_shutdown", {});

    // Session 2 (restart): the saved checkpoint reaches the model inside the launch turn (L3).
    __resetProcessSingleton();
    const s2 = new FakePiSession();
    makeExtension(s2.api as never);
    await s2.emit("session_start", { reason: "startup" });
    await new Promise((r) => setTimeout(r, 1000)); // the real session-start hook has answered
    await withDeadline(s2.prompt("you are the leader again"), 20000, "the restarted leader turn");
    await withDeadline(s2.idle(), 20000, "settling the restarted turn");
    // Count the checkpoint header itself: the session-start digest's wake queue may also name _leader wakes.
    assert.equal(s2.texts().filter((t) => t.includes("Checkpoint for story _leader")).length, 1, `the leader checkpoint is injected once: ${JSON.stringify(s2.texts())}`);
    assert.deepEqual(s2.runtimeErrors, []);
    await s2.emit("session_shutdown", {});
  } finally {
    process.chdir(prevCwd);
    for (const k of Object.keys(process.env)) delete process.env[k];
    Object.assign(process.env, prevEnv);
    rmSync(root, { recursive: true, force: true });
  }
});
