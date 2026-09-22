# 0015 - Close and attach are fail-closed

- Status: Accepted (captain, 2026-09-21)
- Date: 2026-09-21

## Context

The infina cleanup on 2026-09-21 (BACKLOG rows B-38, B-39, B-21, B-40, B-16) showed `cox epic close` and `cox epic
attach` printing `ok` over state they had not verified, matching the disciplines the firstmate deep-dive names
(`research/reports/coxswain-vs-firstmate.md` §1, §2.3; the firstmate teardown `bin/fm-teardown.sh:1-100` refuses before
any record naming what survived is removed, and judges landed work by containment in a remote or the default branch, not
by an upstream that a squash-merge-then-delete flow never has):

- **B-38** - close on a v1-migrated epic with no `.cox/` failed at the archive rename (`rename .cox -> .cox.closed: no
  such file`) and could not even record the failure; two epics were marked by hand.
- **B-39** - close printed `ok: remove-worktrees` but left every clean worktree in place, and an Orca-owned untracked
  `.orca/drops/` screenshot made a worktree look dirty so close kept it.
- **B-21** - close kept a merged epic worktree as "unpushed" because its local branch had no upstream, though its tip
  was already contained in `origin/<branch>`.
- **B-40** - attach on a repo whose epic branch was already checked out elsewhere let Orca create a second worktree and
  a renamed `<user>/epic-<slug>` local branch instead of adopting the existing worktree.
- **B-16** - `orca worktree rm` can delete the local branch; cox must never call it for a branch not on origin.

## Decision

Close and attach refuse or repair instead of reporting an unverified `ok`.

1. **No runtime is already stopped.** When an epic has no `.cox/`, close's five teardown steps are vacuous
   (`ok: <step> (no runtime)`) and it writes `.cox.closed/closed.json` with `no_runtime: true`, rather than failing at
   the archive rename (B-38).
2. **Removal is verified.** After `WorktreeRemove`, close re-reads the repo's `git worktree list` and fails the step
   (`FAILED at remove-worktrees: <path> still registered`) when the path is still registered, writing
   `close.incomplete.json` and never archiving over an incomplete teardown (B-39).
3. **Landed replaces dirty-or-unpushed.** `landed(path)` decides removability: uncommitted tracked changes are not
   landed; an untracked path under a backend-owned prefix (Orca's `.orca/`, from the adapter's `OwnedPaths()`) is
   ignored; a branch is landed when its tip is contained in `origin/<branch>` (fetched first) or in a production
   branch, so a branch with no upstream but contained in `origin/<branch>` is landed (B-21). Uncertainty (a failed
   fetch) keeps the worktree, never removes it.
4. **Only the leader closes.** `cox epic close` from a terminal that is not the epic's recorded leader
   (`.cox/leader` vs `ORCA_TERMINAL_HANDLE`) is refused unless `--captain`, and prints who owns the epic.
5. **Attach adopts, never duplicates.** Before asking the backend, attach looks the epic branch up in the alias
   checkout's `git worktree list` and adopts a clean match (writing the symlink), even when no alias symlink points at
   it; a dirty match refuses. Only when no worktree carries the branch does it ask the backend, then verifies the
   result is on `epic/<slug>`; a backend that renamed the branch has its worktree removed again and the renamed local
   branch deleted only when it carries no unique commits and is not on origin (B-40).
6. **The rm guard is in the adapter.** Orca's `WorktreeRemove` refuses a branch not present on origin
   (`git ls-remote --heads`) unless the caller passes an explicit force, so a caller cannot strand unpushed work
   through the Orca command that deletes the local branch (B-16). Close passes the worktree's branch so the guard can
   fire; a detached worktree has no branch to strand.

## Consequences

- A close over a v1 epic, a no-op removal, a backend-owned artifact, a no-upstream merged branch, a foreign-owned
  attach, and a rename each produce a refusal or a repair, never a silent `ok` (DESIGN AC 3).
- `landed()` fetches `origin/<branch>` before judging containment; a fully offline close with an unreachable origin
  keeps worktrees rather than removing them, which is the safe direction.
- Squash-merge containment (branch commits collapsed into main, so no literal ancestry) is NOT recognized as landed
  here; only literal containment in `origin/<branch>` or a production branch is. The firstmate teardown's patch-id and
  merge-tree coverage for the squash case is out of scope for this story; such a worktree is kept and removed with
  `--force`.
- `backend.Worktree` gains a `Force` field so a caller that has proven removal safe (close's `landed()` decision or an
  operator `--force`) can authorize the adapter guard; adapters that never delete branches ignore it.

## References

- `research/reports/coxswain-vs-firstmate.md` §1, §2.3 (rows "Landed-work test", "Teardown postconditions",
  "Attach / reuse").
- firstmate `bin/fm-teardown.sh:1-100` (landed test and endpoint-close refusal), `bin/fm-spawn.sh:218-225` (clean
  worktree required before a fresh spawn).
- BACKLOG rows B-38, B-39, B-21, B-40, B-16.
