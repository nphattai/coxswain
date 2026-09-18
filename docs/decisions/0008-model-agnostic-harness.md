# 0008 - Model-agnostic: leader and worker pick a harness from policy

- Status: Accepted
- Date: 2026-09-15

## Context

v1 assumes the leader is claude. Tying leader or worker to one harness makes the design brittle and blocks using codex
or a future harness. firstmate shows nine harnesses behind adapters. Different harnesses have different capabilities
(hooks vs no hooks, auto vs manual checkpoint, telemetry, sandbox), so the core must not assume any of them.

## Decision

Both leader and worker choose a harness from `policy.yaml` (options plus a default, extensible later). The core is
model-agnostic and never names a harness. Each harness is an adapter with a capability card declaring roles,
instructions delivery, wake (push/pull), checkpoint (auto/manual), doorbell, interrupt, telemetry, and sandbox. A role
that needs a capability its harness lacks either runs a documented reduced mode or is refused by `cox doctor` /
`cox story dispatch` (P9). One AGENTS.md and skill set serve every harness; only packaging differs.

## Consequences

- The leader can be claude or codex; adding a harness is adding an adapter and a policy entry, not editing the core.
- Wake has two paths (push via hooks, pull via `cox wake wait` + doorbell) selected by the capability card.
- Missing capabilities produce an explicit reduced mode or a refusal, never a silent degradation.

## Review when

Reconsider a specific default when telemetry shows one harness clearly outperforms for a role, or if a new harness needs
a capability the card does not yet express.
