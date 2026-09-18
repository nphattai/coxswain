# Named v1 rollout evidence

- **Recorded:** 2026-09-18 during the v1 to v2 migration work
- **Scope:** the named downstream epics used to order rollout
- **Method:** migration dry-run planning against the existing v1 `.run`, handoff, allocation, and Orca state

## Planned order at the time

The rollout record named `epay-effective-date`, `pipo-admin`, `example-app-mobile`, and
`example-app-mobile-polish`. Completed epics were ordered before the epic that still had a working story. For the live
story, the captain retained the choice to wait for completion or migrate only after the dry run proved the worker idle.

This list is historical evidence, not the current state of those projects. Do not use it to infer whether any epic is
still active or migrated.

## Durable rules extracted from the rollout

- The leader migrates under the captain's schedule. A worker never migrates its own epic.
- Dry run is the go/no-go gate.
- A mismatch between dry run and apply is a stop condition.
- Reconciliation, not hand editing, resolves a working story whose backend session is gone.
- Any attempt to delete a branch is a stop condition.

## Executable evidence

- `cmd/cox/migrate.go`
- `internal/migrate/`
- `internal/migrate/e2e_test.go`
- `tests/fixtures/migrate/`

The current operator contract is [Migrate from v1](../../migrate.md).
