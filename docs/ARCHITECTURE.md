---
hide:
  - toc
---

# Architecture

Coxswain separates human authority, agent work, durable state, and external systems. This page records the boundaries
and failure-model decisions. Source packages and tests own the mechanics.

## Topology

The captain gives an approved outcome to one leader session. The leader calls `cox`. Coxswain writes durable records
and talks to external systems through adapters. Workers run one story each in isolated worktrees and report into the
same epic state. Workers do not coordinate with each other.

<figure class="cox-diagram">
  <div class="cox-diagram__surface">
    <picture>
      <source media="(max-width: 640px)" srcset="assets/diagrams/orchestration-map-mobile.svg">
      <img src="assets/diagrams/orchestration-map.svg" alt="Captain authority flows through a leader and Coxswain to isolated workers and external adapters.">
    </picture>
  </div>
  <figcaption>Authority moves from captain to leader; execution fans out through Coxswain without creating worker-to-worker coordination. <a href="assets/diagrams/orchestration-map.svg">Open full size</a></figcaption>
</figure>

Dependencies point inward:

1. instructions and hooks call the CLI;
2. `cmd/cox/` coordinates domain packages;
3. domain packages depend on adapter interfaces;
4. adapter implementations call Orca, herdr, GitHub, or optional review and quota tools.

This direction keeps the orchestration model independent of any harness or backend. Import boundaries are enforced by
`tests/integration/import_boundary_test.go`.

## Authority boundaries

| Authority | Owns | Does not own |
|---|---|---|
| Captain | scope, design acceptance, risk, release, merge | worker implementation details |
| Leader | decomposition, dispatch, supervision, audit | merging or silently changing approved scope |
| Worker | one story in one worktree | design, sibling stories, release |
| `cox` | durable state transitions, handoff, recovery | business approval |
| Backend | worktrees and terminal lifecycle | semantic handoff |
| Harness | one agent session | epic state |

The distinction is deliberate. Backends are replaceable infrastructure, while report, question/reply, wake, and
checkpoint semantics must survive a backend change. [ADR 0012](decisions/0012-handoff-belongs-to-cox-backends-provide-terminals.md)
records that decision.

## Invariants

### Durable state beats conversation memory

An agent session may restart or compact without becoming the source of truth. The append-only event log owns story
transitions; checkpoints, inbox records, questions, and wakes preserve the handoff needed to continue. The executable
owners are `internal/state/`, `internal/protocol/`, `internal/wake/`, and `internal/watch/`.

### Unknown stays unknown

A missing or failed probe is not evidence that a worker is gone, CI passed, or quota is healthy. Coxswain preserves a
three-state result wherever external facts can be unavailable. This prevents infrastructure failure from being
misread as permission to continue. See `internal/state/resolve.go`, `internal/verdict/`, and
`tests/integration/probe_unknown_test.go`.

> Superseded in part (2026-09-24, epic cox-supervision-port, firstmate fm-watch-triage): unknown is never proof, but a
> quiet worker whose state stays unknown past a bound is surfaced as an urgent `stale` / `unknown_probe` wake and then
> escalates on the wedge ladder (`internal/watch/triage.go`); it is never a skipped check.

### External effects require confirmation

A transition that depends on a spawn, stop, or similar external effect records intent before the call and completion
only after confirmation. A process crash can therefore be reconciled from durable intent without blindly repeating
the effect. `internal/reconcile/` owns the algorithm and `tests/integration/reconcile_test.go` owns the contract.

### Worktree isolation is never traded for availability

If an isolated worktree cannot be created or verified, dispatch fails. Coxswain never falls back to a shared checkout.
Removal detaches the worktree before asking a backend to remove it, because preserving the branch is more important
than automatic cleanup. `internal/worktree/ensure.go` and `tests/integration/worktree_fail_test.go` are the executable
owners.

### Capability gaps are explicit

Every harness and backend exposes a capability boundary. A required capability either exists, produces a documented
reduced mode, or blocks the operation. Silent substitution would make guarantees depend on which external tool happened
to run. Start at [Adapter model](adapters/index.md); implementations live under `internal/adapter/`.

<span id="the-event-log-state-model" aria-hidden="true"></span>

## State and recovery model

The event log is authoritative. Snapshots and resolved fleet views are projections. Live observations such as backend
liveness, git state, and forge state are layered on top with their source and observation time, so stale or failed
evidence remains visible.

`pending_external` means Coxswain recorded an intended transition but has not confirmed the external effect. Recovery
probes the saved session and completes only what can be proved. Operators use `cox reconcile` rather than editing the
event log. The state types live in `internal/state/event.go`; transition and recovery behavior is covered by
`internal/state/*_test.go` and `tests/integration/reconcile_test.go`.

## Adapter model

The core has separate seams for backends, harnesses, forge facts, services, quota, and review surfaces. This avoids a
single broad integration interface and keeps each failure boundary testable with a fake.

- Backend contracts: `internal/adapter/backend/backend.go`
- Harness cards and registry: `internal/adapter/harness/`
- Forge facts: `internal/adapter/forge/forge.go`
- Project services: `internal/adapter/service/service.go`
- Optional review surface: `internal/adapter/review/`
- Quota projection: `internal/quota/`

[Author an adapter](contributing/adapters.md) explains the extension rules. Dated compatibility observations are kept
under [Evidence](evidence/index.md), not in this architecture contract.

## Why one Go binary

The v1 shell system made typed state, exact error propagation, and fake-backed boundary tests difficult. A single Go
binary gives Coxswain one command surface, one state model, and a release artifact without a runtime dependency. The
decision and its trade-offs are preserved in [ADR 0001](decisions/0001-go-single-binary.md).

## Related authority

- [Handoff](handoff.md) for channel semantics and acknowledgement.
- [Protocol model](protocol/index.md) for machine contracts and known gaps.
- [Operations](operations/index.md) for day-to-day use and recovery.
- [Architecture decisions](decisions/index.md) for durable rationale.
