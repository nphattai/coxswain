# `coxswain.inbox.v1`

The inbox is the durable leader-to-worker steer channel. A terminal knock only announces the record; the record itself
owns instruction delivery.

## Invariants

- Sequence allocation and budget enforcement happen under the same per-inbox lock.
- A record is published atomically and never overwrites a prior steer.
- The body is preserved verbatim.
- Moving a record to `handled/` is the worker's acknowledgement.
- An FYI is durable but does not consume steer budget or interrupt a running turn.

The exact record contract is [`schema/inbox.v1.json`](schema/inbox.v1.json). Write, acknowledgement, and ring behavior
are owned by `internal/protocol/inbox/` and its concurrency tests.

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

## Compatibility

Optional additive fields or urgency values may remain in `v1` only when an older worker can safely ignore them. A
breaking shape change needs a new major schema id and an explicit mixed-version handling rule.

See [Handoff](../handoff.md) for directionality and delivery semantics.
