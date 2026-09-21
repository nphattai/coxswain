# Adapter model

Adapters isolate external systems from Coxswain's state and handoff model. The core depends on narrow interfaces and
tests them with fakes. An adapter may expose a reduced mode, but it must never silently weaken a guarantee.

## Choose a contract

| Boundary | Implementations | Executable authority |
|---|---|---|
| Backend | [Orca](orca.md), [herdr](herdr.md) | `internal/adapter/backend/backend.go` |
| Harness | [Claude](claude.md), [Codex](codex.md), [Pi](pi.md) | `internal/adapter/harness/harness.go` and each `Card()` |
| Forge | [GitHub](github.md) | `internal/adapter/forge/forge.go` |
| Service | [Project service](service.md) | `internal/adapter/service/service.go` |
| Review surface | [lavish-axi](lavish.md) | `internal/adapter/review/lavish/` |
| Quota source | [quota-axi](quota-axi.md) | `internal/quota/quotaaxi.go` |

Capability claims should be verified against the implementation and its contract tests. Dated tool-version results
belong under [Evidence](../evidence/index.md).

## Stable rules

- Backend adapters provide worktrees, terminals, liveness, and bounded control. Coxswain owns semantic handoff.
- Harness adapters declare roles, wake delivery, checkpoint behavior, sandbox behavior, and optional telemetry.
- External read failures become unknown facts with reasons.
- Destructive or outward-facing operations require explicit authority at their owning boundary.
- An optional adapter cannot become a hidden runtime requirement for core orchestration.

To add or change an integration, use [Author an adapter](../contributing/adapters.md).
