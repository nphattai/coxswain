# quota-axi quota adapter

The automatic source for `coxswain.quota.v1`. It shells out to a pinned `quota-axi` binary, parses its schema-5 JSON,
and projects each provider's per-scope effective availability into a `Reading`. It is an **optional** adapter: cox runs
without it (every reading is unknown, dispatch still works, ADR 0001). Source: `internal/quota/quotaaxi.go`. quota-axi
itself is data-only - it never routes, and it renders every gap as unknown rather than 0.

## Capability card

| Capability | quota-axi adapter | Notes |
|---|---|---|
| verified version | `0.1.37` (schema 5) | `quota-axi --version`; the adapter parses `schemaVersion == 5` only, any other version is unknown |
| invocation | `quota-axi --provider claude,codex --json` | installed binary by default (`quota.binary` overrides PATH); npx only via explicit `quota.npx` opt-in with an exact version |
| environment | clean | only `PATH` (Node shebang) and `HOME` (codex auth file + Keychain context) are passed; no other inherited variable |
| timeout | 60s | Node cold-start latency; a timeout is unknown, not an error |
| shell | never | `exec` with an argv, no `sh -c` |
| stderr | discarded | never captured, never cached (no diagnostic text can reach disk) |
| keychain | claude needs a one-time grant | `quota-axi --allow-keychain-prompt`; before it, claude reads unknown with that remedy command |
| cache | projection only | `<workspace>/cox/.cache/quota.json`, `0600`, atomic, `O_NOFOLLOW`, 60s TTL, unknown past 15m |

## Fields used

Per provider: `state.status` (fresh required), `state.stale`, `state.error`/`state.reason`/`state.remedyCommand` (for
the unknown reason), and each window's `id`/`resetsAt`. Per `quotaSemantics.effectiveAvailability` scope:
`scope`, `status`, `effectivePercentRemaining`, `boundedBy` (-> `window_ids`), `limitingWindowIds` (-> `resets_at`),
`runway.status` (-> `runway`), and `runway.usableRunwaySeconds` (-> `usable_runway_seconds`, `-1` when absent, e.g. for
`through_reset` and `unknown`).

Scope-to-model: `all_models` -> `model: ""` (account-wide); `model:<slug>` -> `model: <slug>`. A codex model window is a
provider codename (e.g. `model:codex_bengalfox`) that a caller's model id will not match, so codex `Pick` falls back to
the account-wide reading - conservative by design; `window_ids` makes any mismatch visible.

## Unknown, never a fabricated fact

Any of these yields `known:false` for the affected harness, with a reason: binary not found, timeout or run error,
non-JSON output, `schemaVersion != 5`, `state.status != fresh`, `state.stale`, or a scope quota-axi itself reports as
unknown. Unknown never becomes 0 or "full".

## PII limits

Provider normalized output can carry `email` and `account` identifiers. The adapter parses only the quota fields above
into `Reading`s and persists **only** that projection - raw stdout, stderr, credential paths, and identity fields never
touch disk. A test asserts the cache file contains none of `email`, `account`, `token`, or `stderr`.

## Live check (tech lead, post-merge)

`cox quota` on the captain machine (real readings); rename the binary to force unknown + a `quota_health` wake;
`cox quota set` then a dispatch; `cox story resume --harness codex` on a smoke story. Keychain: run
`quota-axi --allow-keychain-prompt` once and "Always Allow".
