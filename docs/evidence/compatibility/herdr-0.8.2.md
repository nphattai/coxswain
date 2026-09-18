# herdr 0.8.2 compatibility evidence

- **Observed:** 2026-09-18 during the M10 adapter work
- **Version:** herdr 0.8.2
- **Method:** the herdr CLI surface, firstmate's documented integration, a fake CLI that recorded commands, and a real
  temporary git repository for worktree behavior

## Results

- Workspace, pane, and agent commands exposed the identifiers required for spawn, send, interrupt, stop, and probe.
- A registered `idle` agent still represented a live process. Missing or unreadable state could not be treated as gone.
- Coxswain could create and remove plain git worktrees around herdr panes while preserving the branch.
- The available evidence did not justify reproducing firstmate's UI-specific composer classifier or run-scoped worker
  listing. Those capabilities remained reduced.
- Stopping a pane could leave an empty herdr workspace. Coxswain did not automate workspace cleanup because herdr owns
  that presentation layer.

## Open live confirmation

The server was not running during the adapter work, so a full live spawn smoke was left open. Re-run it before changing
the support contract for a newer herdr release.

## Executable evidence

- `internal/adapter/backend/herdr/herdr.go`
- `internal/adapter/backend/herdr/herdr_test.go`

The current support contract is [herdr backend adapter](../../adapters/herdr.md).
