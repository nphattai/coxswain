# `coxswain.quota.v1`

The quota contract is a provider-neutral projection used by operator surfaces, wake policy, and the exhausted-now
dispatch gate. It is evidence, not an automatic routing decision.

## Invariants

- A reading is known only when a fresh source supports it.
- Missing, stale, drifted, expired, or authorization-denied data is unknown with a reason.
- Automatic evidence wins over a manual declaration only when it is fresh and known.
- A manual percentage cannot prove through-reset runway.
- Raw provider output and identity data are not part of the persisted projection.
- Routing may display the reading but does not silently change its choice because of it.

The exact contract is [`schema/quota.v1.json`](schema/quota.v1.json). Selection, precedence, and cache behavior are owned
by `internal/quota/` and its tests. Provider compatibility is isolated in
[`quota-axi`](../adapters/quota-axi.md).

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

## Compatibility

Optional additive fields or enum values may remain in `v1` when unknown values are handled safely. Removing or renaming
a field, or making an optional field required, needs a new major schema id.

See [Quota](../quota.md) for the operating model.
