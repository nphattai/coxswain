<!-- Parent: ../AGENTS.md -->
# INTERNAL/ADAPTER - THE SEAMS

## OVERVIEW

Each seam is an interface in its parent package with a `fake/` for tests and one or more concrete adapters:
`backend` (Orca default, herdr optional; ADR 0002), `harness` (Claude, Codex, Pi; ADR 0008), `forge` (GitHub via `gh`),
`review` (lavish), `service` (a project's `cox/services/<alias>.sh`). Read
[Author an adapter](../../docs/contributing/adapters.md) before changing any of them.

## WHERE TO LOOK

| Task | Location | Notes |
|---|---|---|
| Backend contract | `backend/backend.go` (`type Backend interface`) | method comments are the contract |
| Orca | `backend/orca` | terminal plane vs orchestration plane |
| Harness contract + capability card | `harness/`, `harness/registry` | the registry enforces the card at dispatch |
| Claude / Codex / Pi | `harness/{claude,codex,pi}` | cards documented in `docs/adapters/<name>.md` |
| GitHub | `forge/github` | fixtures replay in `forge/fake` |

## CONVENTIONS

- A new method lands on the interface, every concrete adapter, and the fake in the same change.
- Calls to the external tool are bounded (`internal/boundexec`), so a hung CLI cannot stall the watcher.
- `WorktreeRemove` never deletes a branch (F01); `Probe` returns Unknown on doubt, never Settled; `Send` reports
  `rang=true` only when the nudge reached a live terminal.
- A capability a harness lacks is declared in its card as a reduced mode, not faked.

## ANTI-PATTERNS

- Testing an adapter against the live tool in `go test`: live runs belong in `tests/e2e/` behind `COX_E2E=1`.
