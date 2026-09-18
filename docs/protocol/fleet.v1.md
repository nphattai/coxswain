# coxswain.fleet.v1

## Purpose

`cox state --json` emits one fleet view: the derived state of every story in an epic, merged by the resolver from the
event log, backend liveness, harness semantic-busy, git, and forge, in that priority order. Every observation carries its
`source` and `observed_at` so a stale or failed probe is visible rather than silently treated as truth. A probe that
fails resolves to `unknown`; it is never inferred as `gone` (P5, F08). This is the shape the board and scorecard read
later, so it is fixed here.

## Fields

| Field | Type | Required | Meaning |
|---|---|---|---|
| `schema` | string | yes | Always `coxswain.fleet.v1`. |
| `generated_at` | string | yes | RFC 3339 UTC timestamp of this resolve. |
| `epic` | string | yes | Epic slug. |
| `stories` | array | yes | One `story` object per story (see below). |

`story` object:

| Field | Type | Required | Meaning |
|---|---|---|---|
| `id` | string | yes | Story id. |
| `state` | string | yes | Derived state (same enum as `event.v1`). |
| `attempt` | integer | yes | Current attempt (>= 1). |
| `observations` | object | yes | Named observations, each an `observation`. Known keys: `liveness`, `semantic`, `git`, `forge`, `composer`. |

The `forge` observation carries the GitHub pull-request facts `cox state` reads for a story (source `forge`). Its
`value` is `"unknown"` (with an `error`) when there is no PR, gh is missing, or a probe failed - never a guessed pass
(P5, F12). Otherwise `value` is an object:

| Field | Type | Meaning |
|---|---|---|
| `pr` | integer or null | PR number, or `null` when none. |
| `checks` | string | CI verdict aggregated over the PR head: `pass` \| `fail` \| `unknown` (pending or no checks). |
| `checks_count` | integer | Number of check runs the verdict was computed over. |
| `merged` | string | Three-state: `"true"` \| `"false"` \| `"unknown"`. |

The probe is skipped with `cox state --no-forge` (the observation stays `"unknown"`) and cached per head within one
run so gh is not called twice for the same branch. A slow or failed gh resolves to `"unknown"`; `cox state` still exits
0.

`observation` object:

| Field | Type | Required | Meaning |
|---|---|---|---|
| `value` | any | yes | The observed value (string, object, ...); may be `"unknown"`. |
| `source` | string | yes | `event`, `backend`, `hook`, `git`, or `forge`. |
| `observed_at` | string | yes | RFC 3339 UTC timestamp; the probe time, so staleness is visible. |
| `error` | string | no | Probe failure text when `value` is `"unknown"` because the probe errored, so the reason is not lost; omitted on success. |

## Example

```json
{
  "schema": "coxswain.fleet.v1",
  "generated_at": "2026-09-15T02:40:00Z",
  "epic": "pipo-admin",
  "stories": [
    {
      "id": "pipo-admin-cicd",
      "state": "working",
      "attempt": 2,
      "observations": {
        "liveness": { "value": "alive", "source": "backend", "observed_at": "2026-09-15T02:39:58Z" },
        "git": { "value": { "head": "a1b2c3d", "dirty": false, "ahead": 3 }, "source": "git", "observed_at": "2026-09-15T02:39:59Z" },
        "forge": { "value": { "pr": 42, "checks": "pass", "checks_count": 5, "merged": "false" }, "source": "forge", "observed_at": "2026-09-15T02:39:40Z" }
      }
    }
  ]
}
```

## Versioning

- Schema id `coxswain.fleet.v1`. JSON Schema: [`schema/fleet.v1.json`](./schema/fleet.v1.json) (draft 2020-12).
- Adding an optional field, a new observation key, or a new `source` value keeps `v1`.
- The top-level object is `additionalProperties: true`: a reader ignores unknown top-level fields, so a field a newer
  producer adds never makes an older reader reject the record.
- Removing/renaming a field or making an optional one required increments the major (`coxswain.fleet.v2`).
- Changes are recorded in this file's history, never applied silently.
