<span id="migrating-a-v1-epic-to-coxswain-v2" aria-hidden="true"></span>

# Migrate from v1

`cox migrate` converts one v1 epic control surface to the current durable state model. It is an operator action owned
by the epic leader under the captain's schedule. A worker must never migrate its own epic.

The implementation and exact generated records are owned by `cmd/cox/migrate.go`, `internal/migrate/`, and their tests.
This page owns the go/no-go and rollback contract.

## Go/no-go contract

1. Install the target Coxswain build and preserve the existing epic directory.
2. Run `cox migrate --epic <dir>` without `--apply`.
3. Review every live worker, allocation, handoff, hook warning, and the final verdict.
4. Stop if the dry run cannot establish a safe plan.
5. Apply only when the captain accepts that plan and every live worker is in an allowed state.
6. Compare the applied result with the dry run, then run `cox doctor`, `cox state`, and dry-run `cox reconcile`.

A mismatch between dry run and apply is a stop condition. A working story whose external worker appears gone is a
reconciliation case, not permission to edit state. Any attempted branch deletion is also a stop condition.

## What migration preserves

- Existing handoff bodies remain recoverable.
- Unverified allocations remain visible until environment reconciliation proves their state.
- Live sessions are retained only when the backend evidence supports them.
- Ambiguous working state remains pending until reconciliation can prove an outcome.
- Applying twice is refused rather than merging two histories.

These properties are verified in `internal/migrate/*_test.go`, `internal/migrate/e2e_test.go`, and
`tests/fixtures/migrate/`.

## Rollback contract

Rollback is per epic. Restore the renamed v1 control file and backed-up hooks or handoffs, then remove only the newly
created v2 control tree after verifying the target paths. Migration does not authorize deleting worktrees, branches,
or backend dispatches.

Use the exact backup paths reported by the dry run and apply output. Do not rely on a copied file list in this page.

## Historical rollout evidence

The original named rollout order and its then-current story counts are intentionally separated from this evergreen
runbook. See [Named v1 rollout evidence](evidence/migrations/v1-rollout.md).

## Next

- [Operations](operations/index.md) for recovery routes.
- [Architecture](ARCHITECTURE.md) for the state and confirmation model.
