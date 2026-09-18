# Orca CLI 1.4.197 compatibility evidence

- **Observed:** 2026-09-15 and 2026-09-16
- **Environment:** local Orca CLI 1.4.197
- **Method:** `orca agent-context --json`, live JSON calls, `tests/e2e/dispatch-live.sh`, and terminal-plane smoke stories

## Results

- `worker-start` acknowledged input but did not return a dispatch id. The orchestration adapter therefore resolves the
  dispatch through the run-scoped worker list.
- `orchestration check` replayed a delivery until explicit acknowledgement; `orchestration inbox` was a listing and did
  not provide the same delivery acknowledgement contract.
- Orca worktree creation mangled requested branch names. Coxswain restored and reverified the canonical branch. Reusing
  an existing branch required refusing any reset that could discard commits.
- A dispatched Orca worker could not dispatch another supervised worker because the maximum dispatch depth was one.
- Orca accepted one `worker_done` capability per orchestration dispatch. Follow-up completion required the compatible
  status convention owned by `internal/wake/classify.go`.
- Terminal-plane `worktree ps` populated `agents[]` for both Claude and Codex in the observed smoke.
- A terminal created by Coxswain remained present after 30 minutes idle. Its agent state became `done`.
- `terminal show` still returned an old record after `terminal close`; stop confirmation could not rely on that call
  alone.
- An orchestration coordinator terminal was fenced to one run at a time, so mutations needed scoped rebinding and
  restoration.

## Executable evidence

- `internal/adapter/backend/orca/orca.go`
- `internal/adapter/backend/orca/terminal.go`
- `internal/adapter/backend/orca/orca_test.go`
- `internal/adapter/backend/orca/testdata/worktree-ps.json`
- `tests/e2e/dispatch-live.sh`
- `tests/integration/terminal_plane_rules_test.go`

The current support contract is [Orca backend adapter](../../adapters/orca.md). Re-run the live smoke before relying on
these observations for another Orca release.
