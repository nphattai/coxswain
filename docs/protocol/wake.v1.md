# coxswain.wake.v1

## Purpose

The wake queue is how the zero-token watcher hands work to the leader without polling. Each classified event is one line
in `<epic>/.cox/wake.jsonl` with a monotonically increasing `gen`. The leader drains the queue at the start of a turn,
handles the entries, then calls `cox wake ack-through <gen>` to mark everything up to that generation read. Idle rearm
counts only stories the resolver reports as `working` or `input_required`, never historical keys (this is the parked-story
rearm bug, fixed in v1's `hook-stop-rewake.sh` and carried into the resolver here).

## Fields

| Field | Type | Required | Meaning |
|---|---|---|---|
| `schema` | string | yes | Always `coxswain.wake.v1`. |
| `gen` | integer | yes | Generation (>= 1), increasing; `ack-through <gen>` marks all `<= gen` read. |
| `ts` | string | yes | RFC 3339 UTC timestamp. |
| `epic` | string | yes | Epic slug. |
| `story` | string | yes | Story the wake is about. |
| `kind` | string | yes | Classification (enum below). |
| `note` | string | no | Short human-readable summary. |
| `evidence` | object | no | Correlation ids (dispatch, pr, sha, ...). |
| `acked` | boolean | no | Set true when acked through its generation. |

`kind` enum: `question`, `input_required`, `pr_ready`, `worker_done`, `stuck`, `runaway`, `stale`, `unknown_probe`, `status`, `idle_no_done`, `quota_low`, `quota_health`.
`quota_low` (M11) is raised when a harness is at or near quota exhaustion for the leader harness or a working story's harness;
its urgency varies per case (urgent on `exhausted_now` or below `low_percent`, routine on a projected shortfall under
`min_runway_hours`), and the watcher rings the leader doorbell directly for the urgent case. It fires once per
(harness, `resetsAt`) window. `quota_health` (M11/M13b) is a routine wake raised after the automatic quota source
(quota-axi) is unknown for two consecutive polls, including a cold-start unknown. It fires at most once per harness
within `quota.health_debounce_minutes` (default 60); a Known reading resets the streak but not the debounce.
`unknown_probe` is emitted when a liveness probe failed; it is never silently downgraded to "gone" (F08). `idle_no_done` is
emitted when a working story sits idle (composer empty, last message over five minutes old) with an unanswered steer and no
`worker_done` since it, so a re-run that ended with only a `status` cannot leave the story silently stuck; one per steer.
The urgent kinds (`question`, `input_required`, `pr_ready`, `worker_done`, `stuck`, `runaway`, `idle_no_done`) start a
leader turn at once; the rest (`stale`, `unknown_probe`, `status`) batch up to `WAKE_BATCH` so several routine wakes cost
one turn, not one each.
`status` is a non-actionable worker progress note (added M2, additive to v1).

## Example

```json
{
  "schema": "coxswain.wake.v1",
  "gen": 42,
  "ts": "2026-09-15T02:31:00Z",
  "epic": "pipo-admin",
  "story": "pipo-admin-cicd",
  "kind": "pr_ready",
  "note": "PR #128 ready for review",
  "evidence": { "pr": "https://github.com/ExampleOrg/example-repo/pull/1", "head": "a1b2c3d" }
}
```

## Versioning

- Schema id `coxswain.wake.v1`. JSON Schema: [`schema/wake.v1.json`](./schema/wake.v1.json) (draft 2020-12).
- Adding an optional field or a new `kind` value keeps `v1`.
- The top-level object is `additionalProperties: true`: a reader ignores unknown top-level fields, so a field a newer
  producer adds never makes an older reader reject the record.
- Removing/renaming a field or making an optional one required increments the major (`coxswain.wake.v2`).
- Changes are recorded in this file's history, never applied silently.
