// Tests for the pure cox command builders + epic resolver. Run with node --test.
import { test } from "node:test";
import assert from "node:assert/strict";
import { coxArgs, resolveEpic } from "./cox-commands.ts";

test("precompact persists via `cox hook precompact`, not `cox checkpoint facts`", () => {
  const args = coxArgs.precompact("/e", "s1", "/wt");
  // The fix: use the checkpoint-WRITING hook. `cox checkpoint facts` only prints and would be discarded.
  assert.deepEqual(args.slice(0, 2), ["hook", "precompact"]);
  assert.ok(args.includes("--epic") && args[args.indexOf("--epic") + 1] === "/e");
  assert.ok(args.includes("--story") && args[args.indexOf("--story") + 1] === "s1");
  assert.ok(args.includes("--worktree") && args[args.indexOf("--worktree") + 1] === "/wt");
  assert.notDeepEqual(args.slice(0, 2), ["checkpoint", "facts"]);
});

test("sessionStart routes injection through the hook with the worktree (for HEAD freshness)", () => {
  const inj = coxArgs.sessionStart("/e", "s1", "/wt");
  // The fix: inject via `cox hook session-start` (computes HEAD -> CHECKPOINT STALE works), not `cox checkpoint inject`.
  assert.deepEqual(inj.slice(0, 2), ["hook", "session-start"]);
  assert.ok(inj.includes("--worktree") && inj[inj.indexOf("--worktree") + 1] === "/wt");
  assert.notDeepEqual(inj.slice(0, 2), ["checkpoint", "inject"]);
});

// DESIGN item 4: the leader drives the Go-owned leader hooks. Unbound (no epic) = workspace mode; the Go side then
// resolves every active epic (leaderEpics/activeEpics) and stays the single owner. These FAIL on 1149669 (no builders).
test("promptDrain: unbound omits --epic (workspace mode), bound narrows to one epic", () => {
  assert.deepEqual(coxArgs.promptDrain(""), ["hook", "prompt-drain"]);
  assert.deepEqual(coxArgs.promptDrain("/e"), ["hook", "prompt-drain", "--epic", "/e"]);
});

test("stopRewake: --harness claude (exit-2 reopen), no --max (Go owns timing), unbound omits --epic", () => {
  const unbound = coxArgs.stopRewake("");
  assert.deepEqual(unbound, ["hook", "stop-rewake", "--harness", "claude"]);
  assert.ok(!unbound.includes("--max"), "stop-rewake must not pass --max; the Go side owns batch/tick timing");
  assert.ok(!unbound.includes("--epic"), "an unbound leader waits on every active epic");
  assert.deepEqual(coxArgs.stopRewake("/e"), ["hook", "stop-rewake", "--harness", "claude", "--epic", "/e"]);
});

test("precompact/sessionStart unbound omit --epic and --story (workspace leader-checkpoint path)", () => {
  const pc = coxArgs.precompact("", "", "/wt");
  assert.deepEqual(pc, ["hook", "precompact", "--worktree", "/wt"]);
  const ss = coxArgs.sessionStart("", "", "/wt");
  assert.deepEqual(ss, ["hook", "session-start", "--worktree", "/wt"]);
});

test("resolveEpic prefers COX_EPIC, falls back to the installed marker, else empty", () => {
  assert.equal(resolveEpic("/env/epic", "/marker/epic"), "/env/epic");
  assert.equal(resolveEpic("", "/marker/epic"), "/marker/epic");
  assert.equal(resolveEpic(undefined, "/marker/epic\n"), "/marker/epic");
  assert.equal(resolveEpic("  ", "  "), "");
  assert.equal(resolveEpic(undefined, undefined), "");
});
