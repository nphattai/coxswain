# coxswain.inbox.v1

## Purpose

The steer channel carries leader instructions to a running worker as durable records under `<epic>/inbox/<story>/NNN.msg`.
Sequence numbers are allocated under a per-inbox lock and files are published by atomic rename from a unique temp name, so
concurrent writers never lose or overwrite an instruction while reporting success (F06). The worker acknowledges by moving
the file into `handled/`; the move is the ack. The steer budget (5 per story by default) is checked inside the same
locked section as sequence allocation.

The v1 on-disk record here supersedes the v1 kit format (`schema=crewkit-inbox.v1`, `at=`, `urgency=`, `--`, body). The
schema id is renamed to `coxswain.inbox.v1` and the fields are made explicit for machine reading.

## Fields

| Field | Type | Required | Meaning |
|---|---|---|---|
| `schema` | string | yes | Always `coxswain.inbox.v1`. |
| `seq` | integer | yes | The `NNN` sequence (>= 1), allocated under the per-inbox lock; never reused. |
| `story` | string | yes | Story id the steer is for. |
| `at` | string | yes | RFC 3339 UTC timestamp. |
| `urgency` | string | yes | `steer` (counts against the budget, may trigger a re-ring) or `fyi` (never interrupts). |
| `override` | string | no | Reason, present only when the leader overrode the steer budget. |
| `body` | string | yes | The verbatim instruction text. |

## Example

```json
{
  "schema": "coxswain.inbox.v1",
  "seq": 3,
  "story": "pipo-admin-cicd",
  "at": "2026-09-15T02:20:11Z",
  "urgency": "steer",
  "body": "Rebase onto epic/pipo-admin first, then open the PR. Do not squash the migration commit."
}
```

## Versioning

- Schema id `coxswain.inbox.v1`. JSON Schema: [`schema/inbox.v1.json`](./schema/inbox.v1.json) (draft 2020-12).
- Adding an optional field or a new `urgency` value keeps `v1`.
- The top-level object is `additionalProperties: true`: a reader ignores unknown top-level fields, so a field a newer
  producer adds never makes an older reader reject the record.
- Removing/renaming a field or making an optional one required increments the major (`coxswain.inbox.v2`).
- Changes are recorded in this file's history, never applied silently.
