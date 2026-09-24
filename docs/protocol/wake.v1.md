# `coxswain.wake.v1`

The wake queue tells the leader that durable epic state needs attention. It is a notification channel, not story state
and not a substitute for the underlying event, question, or report.

## Invariants

- Generations increase monotonically so a leader can acknowledge a handled prefix.
- The watcher writes a wake durably before acknowledging an external delivery.
- Urgent and routine classifications affect delivery timing, not authority.
- A failed liveness probe creates an unknown-probe signal rather than a false gone state.
- Progress status is not completion. Completion classification is explicit and tested.

The exact record contract is [`schema/wake.v1.json`](schema/wake.v1.json). Queue behavior is owned by
`internal/wake/queue.go`; classification is owned by `internal/wake/classify.go`; delivery policy is owned by
`internal/watch/` and the harness hooks.

## Queue robustness and presentation

- One unusable row (unparseable, or no positive `gen`) never wedges the queue. `Load` skips it, and the next
  non-peek drain retires it and reports it on stderr.
- A row with no `schema` tag is a legacy row and is adopted as `v1`. A row with another explicit schema is kept but not
  loaded.
- The drain collapses only obvious duplicates: same story and kind, where the later payload equals the earlier one or
  extends it past a word boundary. Every distinct unread report still surfaces.
- An acknowledgement that consumes nothing names the current wake and the exact `cox wake ack-through` command.
- Every drain also prints the status sections in [Status lines and open decisions](decision.v1.md), and a
  `WATCHER DOWN` banner when stories are in flight and no live watcher holds the epic.

Superseded by cox-supervision-port wave 2 (firstmate `1e0e773` translated):

- The free-text status regex ported from v1 `bin/watch.sh` (`blocked` or `need a decision` anywhere in the text) is
  replaced by the firstmate status-line grammar. Prose never raises a wake.
- `stale` and `unknown_probe` are urgent, not routine (leader ruling 2026-09-24).
- A corrupt queue line is retired instead of failing every drain, and a schema-less row is adopted instead of
  skipped.

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

## Compatibility

Optional fields and new wake kinds may remain in `v1` only when older readers can safely surface or ignore them.
Removing or renaming a field, or making an optional field required, needs a new major schema id.

See [Handoff](../handoff.md) for the leader loop and [Adapters](../adapters/index.md) for push and pull delivery.
