# Claude CLI 2.1.272 compatibility evidence

- **Observed:** 2026-09-15 and 2026-09-16
- **Environment:** Claude CLI 2.1.272 on the captain machine
- **Method:** CLI help, live worker launch, hook fixtures, and arena command tests

## Results

- A dispatched worker needed a non-interactive permission mode to avoid a local prompt it could not answer. The chosen
  default transfers that risk to the captain's policy.
- Plan mode was read-only even when an extra directory was granted. A headless arena role could therefore return text
  for Coxswain to record, but a terminal arena role needed a mode that could write its report in its disposable
  worktree.
- Hook-based wake and checkpoint delivery worked through the Coxswain hook shims. Doorbell prompts contained leading
  whitespace, so suppression needed normalized input.

## Executable evidence

- `internal/adapter/harness/claude/`
- `internal/arena/`
- `cmd/cox/hook.go`
- `tests/hooks/hooks.bats`
- `tests/e2e/arena-live.sh`

The current support contract is [Claude harness adapter](../../adapters/claude.md).
