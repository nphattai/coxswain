// Tests for the pure cox command builders + epic resolver. Run with node --test.
import { test } from "node:test";
import assert from "node:assert/strict";
import { coxArgs, resolveEpic } from "./cox-commands.ts";

test("precompact persists via `cox hook precompact`, not `cox checkpoint facts`", () => {
  const args = coxArgs.precompact("/e", "s1", "/wt");
  // The fix: use the checkpoint-WRITING hook. `cox checkpoint facts` only prints and would be discarded.
  assert.deepEqual(args.slice(0, 2), ["hook", "precompact"]);
  assert.ok(args.includes("--story") && args[args.indexOf("--story") + 1] === "s1");
  assert.ok(args.includes("--worktree") && args[args.indexOf("--worktree") + 1] === "/wt");
  assert.notDeepEqual(args.slice(0, 2), ["checkpoint", "facts"]);
});

test("wakeWait carries epic; sessionStart routes injection through the hook with the worktree (for HEAD freshness)", () => {
  assert.deepEqual(coxArgs.wakeWait("/e", "25m"), ["wake", "wait", "--max", "25m", "--epic", "/e"]);
  const inj = coxArgs.sessionStart("/e", "s1", "/wt");
  // The fix: inject via `cox hook session-start` (computes HEAD -> CHECKPOINT STALE works), not `cox checkpoint inject`.
  assert.deepEqual(inj.slice(0, 2), ["hook", "session-start"]);
  assert.ok(inj.includes("--worktree") && inj[inj.indexOf("--worktree") + 1] === "/wt");
  assert.notDeepEqual(inj.slice(0, 2), ["checkpoint", "inject"]);
});

test("resolveEpic prefers COX_EPIC, falls back to the installed marker, else empty", () => {
  assert.equal(resolveEpic("/env/epic", "/marker/epic"), "/env/epic");
  assert.equal(resolveEpic("", "/marker/epic"), "/marker/epic");
  assert.equal(resolveEpic(undefined, "/marker/epic\n"), "/marker/epic");
  assert.equal(resolveEpic("  ", "  "), "");
  assert.equal(resolveEpic(undefined, undefined), "");
});
