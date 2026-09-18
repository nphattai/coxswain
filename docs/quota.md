# Quota (observe-only)

cox reads per-harness rate-limit headroom so the captain sees quota next to fleet state and gets one wake before a
window runs dry, instead of finding out from a 429 or an idle worker. M11 is **observe-only**: quota feeds surfaces,
wakes, a dispatch refusal, and a manual reroute. It never changes which harness routing picks (ADR 0011 stays in force);
that automatic safety valve is deferred to a separate ADR (captain ruling 2026-09-16, option a).

Design of record: `epics/m11-quota-routing/DESIGN.md` (signed after two arena rounds). Contract:
[`docs/protocol/quota.v1.md`](protocol/quota.v1.md).

## The contract: coxswain.quota.v1

`internal/quota` owns one type - a `Reading` for a `(harness, model)` - and cox owns it, not the provider read. Routing,
the watcher, `cox state`, and the board read only this contract. A `Reading` carries `known`, `percent_remaining`,
`resets_at`, `runway` (`exhausted_now` | `projected_exhaustion` | `through_reset` | `unknown`),
`usable_runway_seconds` (`-1` when the source states none), `source` (`quota-axi` | `manual` | `none`), `observed_at`,
`reason`, and `window_ids`. When no source can read a harness the reading is `known:false` and every surface renders it
as unknown with the reason, never as a quota fact (F11). A Go-native reader can be added later behind the same contract.

## Two adapters

- **quota-axi** (automatic): a pinned `quota-axi` binary read through the projection cache. Installed binary by default
  (`quota.binary` overrides the PATH lookup); npx only via an explicit `quota.npx` opt-in with an exact version. It runs
  `quota-axi --provider claude,codex --json` with a clean environment, a 60s timeout, and no shell, parses
  `schemaVersion 5` only, and treats any drift, non-fresh, or stale provider as unknown with the remedy command. Each
  fresh provider projects one reading per scope (`all_models` and each `model:*`) from `effectiveAvailability`. See
  [`docs/adapters/quota-axi.md`](adapters/quota-axi.md).
- **manual** (captain-declared): `cox quota set <harness> <percent> --until <RFC3339> [--model m]` writes
  `<epic>/.cox/quota-manual/<harness>.json` (owner-only, atomic, no symlink following) with actor, timestamp, and a
  mandatory expiry. A manual reading is always `runway: unknown` (a bare percentage cannot prove reset-and-pace), expires
  at `until`, and is leader-owned - a worker (`COX_STORY` other than `_leader`) is refused. `cox quota unset <harness>`
  clears it. Both record a `quota_manual_set` audit event.

`Merge` combines the two: a fresh automatic reading always wins for a `(harness, model)`; a manual reading is used only
where the automatic source is unknown or absent. `Pick` selects the reading a `(harness, model)` runs under: exact model,
then a model-family match, then the account-wide reading, then unknown.

When the current automatic reading is unknown, the human `cox quota` table also shows `last known <pct>% at
<observed_at>` if the projection cache contains a Known reading from the previous 15 minutes. This is context only: the
current value stays unknown, and JSON, routing, dispatch gates, and wakes never substitute the cached percentage.

### Cache and privacy

The quota-axi adapter projects to the contract in memory and persists **only** the projection at
`<workspace>/cox/.cache/quota.json` (owner-only `0600`, atomic, `O_NOFOLLOW`, 60s TTL, unknown past 15 minutes). Raw
stdout, stderr, and any identity fields (provider output can carry email and account ids) never touch disk.

## Surfaces

- `cox quota [--json]` - the merged readings table (percent, runway, resets, source, observed, reason).
- `cox state` - a `quota` column for each in-flight story (the harness/model it runs on), as a fleet.v1 observation.
- `cox board` - a read-only Quota panel.
- `cox doctor` - a quota-axi line (found/path/version/keychain, keychain derived from a live claude reading) and any
  active manual readings.
- `cox route` - a `quota: <reading>` info line for the chosen harness/model. It never changes the Choice.

## Wakes

The watcher polls quota every 5 minutes through the 60s cache, under a cross-process lock, with +-30s jitter and
exponential backoff to 30 minutes on error; every poll writes one line to `<epic>/.cox/quota-calls.log` so call volume
is measurable before the interval is tuned. For the leader harness (policy `harness.leader.default`) and every working
story's harness it raises:

- `quota_low` **urgent** on `exhausted_now` or a known percent below `quota.low_percent` (a manual low fires here too);
  **routine** on `projected_exhaustion` with a usable runway under `quota.min_runway_hours`. Once per `(harness, resetsAt)`.
- `quota_health` **routine**, after the automatic source is unknown for two consecutive polls, including when the first
  observation is unknown. It fires at most once per harness within `quota.health_debounce_minutes` (default 60). A
  Known reading resets the consecutive-unknown count but does not bypass the debounce if the source flaps back to
  unknown.

## Dispatch gate

`cox story dispatch` reads the quota for the chosen `(harness, model)` after resolving it. `exhausted_now` refuses with
exit 1 and the reading printed (`--force-quota` overrides); a known percent below `low_percent` warns and still
dispatches; unknown never blocks. The gate never changes the harness.

## Manual reroute (no automatic routing)

cox never switches a harness on quota by itself (ADR 0011: no non-default pick before the 12-row baseline). When a
harness runs low the leader reroutes by hand: `cox story park <id>` then `cox story resume <id> --harness <other>
[--model m]`. Resume runs attempt N+1 on the other harness with the harness-neutral checkpoint injected, resolves the new
harness's model (the cross-harness model guard, M10c, still applies), and records `evidence.reroute {from,to,reason}`.
Switching the leader is also manual (open a codex session, set `harness.leader.default`); `quota_low` for the leader
harness is a wake the captain reads.

### Why no automatic routing

Both arena roles and the captain chose observe-only. Item 5 of the original proposal (rung 3 picks a non-default harness
as a safety valve) conflicts with ADR 0011, which forbids a non-default pick before a 12-row baseline table exists. A
default harness at `projected_exhaustion` still dispatches with only a warning - a deliberate tradeoff, not a gap. An
account-wide window (codex weekly) also makes a fallback to the leader's harness drain the leader's own budget, so any
future valve must never target the leader harness unless it is `through_reset`.

## Policy

```json
"quota": {
  "binary": "", "npx": null,
  "low_percent": 10, "ok_percent": 25, "min_runway_hours": 24, "poll_minutes": 5,
  "health_debounce_minutes": 60,
  "why": "...", "review_when": "after one month of readings"
}
```

Thresholds are first guesses; they live in policy with a `review_when` after a month of readings. `ok_percent` is
reserved for the deferred safety valve and is unused in observe-only.
