# coxswain.event.v1

## Purpose

The event log is the source of truth. Every story state transition and every externally-visible side effect is one
append-only JSON object on its own line in `<epic>/.cox/events.jsonl`. Current state is *derived* by folding the log
(`snapshot.json` is a cache that must rebuild identically from the log). This replaces the v1 `.run` "last value wins"
file, whose reads could disagree with reality (F02, F05, and the parked-story rearm bug).

A transition that depends on an external side effect (worker-stop, database drop, worktree removal) records `to` only
after the adapter confirms it. Until then the event carries `to: "pending_external"` and `external_confirmed: false`,
and ownership is not cleared. This is the shared root fix for F03, F04, F05.

## Fields

| Field | Type | Required | Meaning |
|---|---|---|---|
| `schema` | string | yes | Always `coxswain.event.v1`. |
| `ts` | string | yes | RFC 3339 UTC timestamp of the event. |
| `epic` | string | yes | Epic slug. |
| `story` | string | yes | Story id the event is about. |
| `attempt` | integer | yes | Attempt number (>= 1); increments on resume from `parked`. |
| `actor` | string | yes | Who caused the transition: `captain`, `leader`, `worker`, `watcher`. |
| `from` | string | yes | State before (see the state enum below). |
| `to` | string | yes | State after; `pending_external` while an external effect is unconfirmed. |
| `evidence` | object | no | Correlation ids: `dispatch`, `pr`, `head`, `reply`. Additional keys allowed. `intended_to` names the state a `pending_external` transition is trying to reach, so a process that dies mid-effect still records what it was waiting for (control channel, M2). |
| `external_confirmed` | boolean | yes | `true` only when the adapter confirmed the external effect; `false` keeps ownership. |

State enum: `submitted`, `working`, `input_required`, `parked`, `completed`, `failed`, `canceled`, `pending_external`.

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

## More examples

A `pending_external` transition that names where it is heading (required by the schema; `state.Append` refuses one
without `intended_to`):

```json
{ "schema": "coxswain.event.v1", "ts": "2026-09-15T02:14:07Z", "epic": "pipo-admin", "story": "pipo-admin-cicd",
  "attempt": 2, "actor": "leader", "from": "working", "to": "pending_external",
  "evidence": { "intended_to": "parked", "verb": "park" }, "external_confirmed": false }
```

A story failed (the harness could not complete it) and canceled (dropped from scope); both carry `evidence.reason`:

```json
{ "schema": "coxswain.event.v1", "ts": "2026-09-15T03:01:00Z", "epic": "pipo-admin", "story": "pipo-admin-cicd",
  "attempt": 2, "actor": "leader", "from": "working", "to": "failed",
  "evidence": { "reason": "harness crashed twice, no reproducible fix" }, "external_confirmed": true }
```

```json
{ "schema": "coxswain.event.v1", "ts": "2026-09-15T03:05:00Z", "epic": "pipo-admin", "story": "pipo-admin-docs",
  "attempt": 1, "actor": "leader", "from": "parked", "to": "canceled",
  "evidence": { "reason": "captain dropped the story from scope" }, "external_confirmed": true }
```

## Reconcile rule

A story left in `pending_external` by a crash between the two appends is finished by reading `evidence.intended_to` and
probing the saved session, never by repeating the side effect:

- `intended_to: working` + probe **alive** -> `working` (the worker is adopted); + probe **gone** -> `failed` (the
  dispatch never established).
- `intended_to: parked|completed|canceled|failed` + probe **gone** -> that state (the effect took).
- probe **unknown**, or an alive worker that cannot confirm the intended effect -> the story is **kept** in
  `pending_external` (F08: never inferred gone).

The reconcile event is `pending_external -> <to>` with `external_confirmed: true`, actor `watcher`, and a `note`
recording the probe. `cox reconcile` (dry-run by default, `--apply` to write) and the watcher's periodic pass both
apply only the confirmable cases.

## Versioning

- Schema id is `coxswain.event.v1`. JSON Schema: [`schema/event.v1.json`](./schema/event.v1.json) (draft 2020-12).
- Adding an optional field keeps `v1`. Adding a new value to `actor` or the state enum keeps `v1` (readers ignore
  unknown states as `unknown` rather than crashing).
- The top-level object is `additionalProperties: true`: a reader ignores unknown top-level fields, so a field a newer
  producer adds never makes an older reader reject the record.
- Removing or renaming a field, or making an optional field required, is a breaking change and increments the major
  (`coxswain.event.v2`). Both versions may then appear in one log; the resolver dispatches on `schema`.
- Every change is recorded in this file's history; the spec is never edited silently (plan.md 10, risk 3).
