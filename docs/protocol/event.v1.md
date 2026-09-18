# `coxswain.event.v1`

The event log is the authoritative history of story transitions. Current state is a fold of append-only events, not a
mutable status file.

## Invariants

- Epic, story, attempt, actor, transition, and evidence remain explicit.
- An external side effect is represented by durable intent before it is represented as confirmed completion.
- Entering `pending_external` requires the intended target so recovery has an unambiguous goal.
- Reconciliation never repeats an effect merely because the process restarted.
- Unknown state or probe data cannot be collapsed into a known terminal state.

The exact record contract is [`schema/event.v1.json`](schema/event.v1.json). Types and transition behavior are owned by
`internal/state/event.go`, `internal/state/append.go`, `internal/state/fold.go`, and their tests. Recovery is owned by
`internal/reconcile/`.

## Example

```json
{
  "schema": "coxswain.event.v1",
  "ts": "2026-09-15T02:14:07Z",
  "epic": "pipo-admin",
  "story": "pipo-admin-cicd",
  "attempt": 2,
  "actor": "leader",
  "from": "working",
  "to": "parked",
  "evidence": { "dispatch": "ctx_8f2", "head": "a1b2c3d" },
  "external_confirmed": true
}
```

## Compatibility

Readers tolerate additive top-level data and preserve unfamiliar state as unknown. Removing or renaming a field, or
making an optional field required, needs a new major schema id. Mixed-version support must be implemented and tested
before a new major record is written into an existing log.

See [Architecture](../ARCHITECTURE.md) for the state and failure model.
