# coxswain.quota.v1

## Purpose

`coxswain.quota.v1` is the only quota type routing, the watcher, `cox state`, and the board read (Option C,
`epics/m11-quota-routing/DESIGN.md`, signed after two arena rounds). cox owns the contract, not the provider read: two
adapters fill it - a pinned `quota-axi` binary (schema 5 only, unknown on any drift) and a captain-declared manual file -
and a Go-native reader may be added later behind the same contract without touching a consumer. M11 is observe-only: the
contract feeds surfaces, quota wakes, a dispatch refusal on `exhausted_now`, and a manual `cox story resume --harness`
reroute. It never changes the routing Choice (ADR 0011 stays in force).

A `Reading` is never "quota exhausted" or "quota full" unless a fresh source said so. When a source is absent, drifted,
stale, auth-denied, or an expired manual entry, the reading is `known: false` and every consumer renders it as unknown
with the reason, never as a fact (F11). The claude status line is never scraped (ADR 0011); `quota-axi` reads
first-party endpoints.

## Fields

| Field | Type | Required | Meaning |
|---|---|---|---|
| `harness` | string | yes | The harness this reading is for (`claude`, `codex`). |
| `model` | string | no | The window family (`fable`, `opus`, ...) the reading is scoped to, or empty for the account-wide (`all_models`) scope. |
| `known` | boolean | yes | `false` whenever the value is not machine-readable; the other value fields are meaningful only when `true`. |
| `percent_remaining` | integer | when known | Effective percent remaining across the bounding windows (quota-axi `effectivePercentRemaining`). |
| `resets_at` | string | no | RFC 3339 reset of the binding window (`limitingWindowIds[0]`). |
| `runway` | string | yes | One of `exhausted_now`, `projected_exhaustion`, `through_reset`, `unknown`. |
| `usable_runway_seconds` | integer | yes | Seconds of usable runway for a finite result; `-1` when the source states none (`through_reset`, `unknown`, or a schema without the field). The wake horizon treats `-1` as unknown, never as "0 seconds left". |
| `source` | string | yes | One of `quota-axi`, `manual`, `none`. |
| `observed_at` | string | no | RFC 3339 time the projection was read. |
| `reason` | string | no | Why the reading is unknown (empty when known), or a health note (e.g. a remedy command). |
| `window_ids` | array | no | The bounding window set the reading was computed from, so a wrong model-to-window match is visible. |

## Precedence and selection

- **Merge** combines the automatic source with the manual one: a fresh automatic reading always wins for a
  `(harness, model)`; a manual reading is used only where the automatic source is unknown or absent. A manual reading
  can never assert `through_reset` - a bare percentage cannot prove a reset-and-pace runway.
- **Pick** selects the reading a `(harness, model)` runs under: an exact `(harness, model)` match, else a model-family
  match (the reading's `model` token appears in the requested model, e.g. `fable` in `claude-fable-5-1`), else the
  account-wide `(harness, "")` reading, else an unknown `source: none` reading.

## Cache and privacy

The quota-axi adapter projects raw provider JSON to this contract in memory and persists **only** the projection at
`<workspace>/cox/.cache/quota.json` (owner-only `0600`, atomic write, `O_NOFOLLOW`, TTL 60s, unknown after 15 minutes).
Raw stdout, stderr, `email`, `account`, and credential paths are never written to disk (provider output can carry
identity fields; cox needs only quota fields).

## Example

```json
{
  "harness": "claude",
  "model": "fable",
  "known": true,
  "percent_remaining": 35,
  "resets_at": "2026-09-20T04:00:00Z",
  "runway": "projected_exhaustion",
  "usable_runway_seconds": 138309,
  "source": "quota-axi",
  "observed_at": "2026-09-16T03:21:01Z",
  "window_ids": ["five_hour", "seven_day", "model:fable"]
}
```

## Versioning

- Schema id `coxswain.quota.v1`. JSON Schema: [`schema/quota.v1.json`](./schema/quota.v1.json) (draft 2020-12).
- Adding an optional field or a new enum value keeps `v1`.
- The top-level object is `additionalProperties: true`: a reader ignores unknown fields.
- Removing/renaming a field or making an optional one required increments the major (`coxswain.quota.v2`).
