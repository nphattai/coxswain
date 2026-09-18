# Migrating a v1 epic to coxswain v2

`cox migrate` converts a v1 epic control file (`<epic>/.run`) into the v2 control tree: an
`events.jsonl` the fold replays, `.cox/sessions/<story>.json` for each dispatch still alive,
`.cox/env/<story>.json` for the machine-layer allocations, wrapped checkpoint handoffs, and
`.cox/run`. It renames `.run` to `.run.migrated` so no live v1 state sits beside the v2 tree
(what `cox doctor` flags). It never touches git, worktrees, or Orca dispatches.

**Never run migrate on a live epic from a worker.** Migrating is the epic leader's job under the
captain's schedule (ADR 0009). A worker dispatched into an epic must not migrate it.

## What migrate does

- **Handoffs.** Every `handoffs/<story>.md` without `schema: coxswain.checkpoint.v1` frontmatter is
  backed up to `<story>.md.v1` and rewritten with a `migrated-v1` checkpoint header (attempt 1, head
  from the story worktree if it still exists else `unknown`, base `unknown`). The body is preserved
  byte for byte, so `cox story resume` can inject the checkpoint.
- **Allocations.** Each story's `port|db|redis|sim|env` (last value wins, empties dropped) is written
  to `.cox/env/<story>.json` as `migrated-unverified`. `cox env reconcile` probes and releases them.
- **Sessions.** A story keeps a live session only when Orca's `worker-list` reports its dispatch as
  `ready` or `running`. A working story whose dispatch has settled (`succeeded|failed|abandoned`, or
  gone) gets no session and lands in `pending_external` (`intended_to: working`) for `cox reconcile`
  to finish; the last observed dispatch state is recorded in the event evidence.
- **Idempotency.** A second `--apply` is refused once `.cox/events.jsonl` exists.

## Dry run: the go/no-go checklist

`cox migrate --epic <dir> [--root <clone>]` writes nothing and prints a checklist: the run id, each
live worker (dispatch, terminal, composer), the allocations to write with a live-port probe, the
handoffs to wrap, whether `<root>/.claude/settings.json` still points at `bin/hook-*`, and a `GO` or
`NO-GO` verdict. `--apply` refuses while a live worker's composer is `busy` or `pending` unless
`--force`; an `unknown` composer only warns.

## Runbook (after M9 is merged and `make install`)

Migrate the least risky epics first; the epic with a live worker last.

1. **epay-effective-date** (4 stories, all done). Open the leader terminal at the clone root, then:
   - `cox migrate --epic <dir> --root <clone>` and read the checklist.
   - `cox migrate --epic <dir> --apply`.
   - `cox doctor`, `cox state --epic <dir>`, `cox reconcile --epic <dir>`, `cox scorecard --epic <dir>`.
   - `cox env reconcile --epic <dir> --apply` to release the dead v1 ports.
   - `cox workspace hooks --root <clone> --epic <dir>` to move the leader session onto cox hooks.
   - Relaunch the leader's Claude Code session.
   Roll back if any command diverges from what the dry run said.
2. **pipo-admin** (13 stories, all done). As above. This clone runs an older crewkit; the dry run
   prints any unusual `.run` keys.
3. **example-app-mobile** (15 stories, all done). As above. The base clone hosts two epics; the leader's
   `COX_EPIC` points at the epic it is actively leading.
4. **example-app-mobile-polish** (1 story working). The captain chooses:
   - **(a) wait**: let the working story finish (leader replies "released", worker `worker_done`, PR
     merged), then migrate as in 1-3. Cleanest.
   - **(b) migrate while idle**: the dry run confirms the composer is idle, `--apply` writes a live
     session for the working dispatch, wraps the handoff, and moves the hooks; steer and finish the
     story with `cox steer` / `cox story done`. The worker need not relaunch.

Stop conditions at every step: the dry run and the apply diverge; `cox state` reports a working story
whose dispatch Orca says is dead (let `cox reconcile` decide, do not hand-edit); any command tries to
delete a branch.

## Rollback (per epic)

Migrate changes no v1 file except renaming `.run`, so a rollback is local and lossless:

1. `mv <epic>/.run.migrated <epic>/.run`
2. `rm -rf <epic>/.cox`
3. Restore hooks: `mv <clone>/.claude/settings.json.v1 <clone>/.claude/settings.json`
4. Handoffs: each wrapped `handoffs/<story>.md` has its original at `handoffs/<story>.md.v1`.
