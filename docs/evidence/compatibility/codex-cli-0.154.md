# Codex CLI 0.154 compatibility evidence

- **Observed:** 2026-09-15 and 2026-09-16
- **Environment:** Codex CLI 0.154 and 0.154.0 on the captain machine
- **Method:** CLI help and binary inspection, live terminal-plane smoke stories, hook fixtures, and unit tests

## Results

- A dispatched Codex worker needed non-interactive approval flags. A bare launch stopped at a local confirmation prompt.
- `workspace-write` needed additional writable roots for the epic directory, Go build cache, and linked worktree git
  common directory.
- The sandbox blocked loopback binds until network access was enabled. The available setting granted broader network
  access, so this remained captain-owned risk rather than a loopback-only capability.
- The CLI exposed project hooks for `Stop`, `UserPromptSubmit`, `SessionStart`, and `PreCompact`. Project hooks required
  repository trust and were installed without editing the user-level hook file.
- Codex hook blocking used a stdout JSON decision rather than Claude's exit-code signal.
- Orca's structured agent state identified a Codex approval wait more reliably than terminal text.

## Open live confirmation

Fixtures cover hook payloads and decision translation. The complete sequence where an asynchronous `Stop` hook sleeps
and a wake opens a fresh leader turn still needs confirmation for each deployed Codex build. The pull path and backend
doorbell remain the fallback.

## Executable evidence

- `internal/adapter/harness/codex/`
- `internal/adapter/backend/launch.go`
- `cmd/cox/hook.go`
- `cmd/cox/workspace_hooks_test.go`
- `tests/e2e/codex-terminal-steer.sh`
- `tests/hooks/hooks.bats`

The current support contract is [Codex harness adapter](../../adapters/codex.md).
