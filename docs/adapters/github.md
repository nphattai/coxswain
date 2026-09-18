# GitHub forge adapter

The forge seam to GitHub, over the `gh` CLI. The core (`internal/verdict`, and `cox state` / `cox scorecard` /
`cox ship`) depends only on the `forge.Forge` interface, never on `gh`, so every audit, state, scorecard, and ship
computation is unit-tested against a fixture-replaying fake. Every method returns a real Go error on any retrieval
failure; the verdict and observation layers turn that error into `unknown`, never a guessed pass (P5, F12). Source:
`internal/adapter/forge/`, adapter `internal/adapter/forge/github/`, fake `internal/adapter/forge/fake/`.

## Capability card

| Capability | GitHub | Notes |
|---|---|---|
| PR | yes | `gh pr view <head> --json number,headRefOid,headRefName,baseRefName,state`; a head with no PR is a real error, not an empty PR |
| Diff | yes | `gh pr diff <n>`; unified diff text, parsed for shape and the credential scan |
| Checks | yes | `gh pr checks <n> --json name,state,bucket,startedAt,completedAt`; bucket -> status/conclusion, timestamps drive `ci_wall_incl_queue_s` (a zero gh timestamp is dropped to unset) |
| Comments | yes | `gh pr view <n> --json comments,reviews`; a `CHANGES_REQUESTED` review is surfaced as a "Changes Requested" comment for the reviewer verdict |
| Merged | yes | derived from the PR `state` (`merged`) resolved by `PR` |

## Limits (ceilings)

- **Needs `gh` logged in.** Every call shells out to `gh` in the story worktree. An unauthenticated or missing `gh`,
  a rate limit, or a network error is a retrieval error, so the observation/verdict is `unknown` (never `fail` or a
  guessed pass), and `cox state` / `cox ship facts` still exit 0. Log in with `gh auth login`.
- **No PR is `unknown`, not `fail`.** A branch with no open PR is a missing fact: `cox state` reports the forge
  observation as `unknown`, and `cox ship facts` says "no PR for `epic/<slug>`".
- **Verdicts are bound to the PR head sha.** A later push changes the head, so a stored verdict is stale relative to
  the new head; readers re-check against the current head (F12).
- **Read-only.** The adapter reads PR facts; it never opens, comments on, or merges a PR (delivery and merge authority
  stay with the leader/captain, not a fact-gathering command).
- **`gh` timeouts.** A hung `gh` blocks the calling command; `cox state --no-forge`, `cox scorecard --no-forge`, and
  `cox ship facts --no-forge` skip the probe entirely. `cox state` caches the probe per head within one run so `gh` is
  not called twice for the same branch.

## Where it is used

- `cox audit pr <story>` - the full three-state PR audit bundle (shape, CI, reviewer, credential scan) bound to head.
- `cox state` - the `forge` observation (`pr`, `checks`, `checks_count`, `merged`) in `coxswain.fleet.v1`.
- `cox scorecard` - `ci_wall_incl_queue_s`, from the earliest check start to the latest check completion.
- `cox ship facts` - per-repo forge readiness (PR, checks verdict, merged) bound to head, beside the git facts.
