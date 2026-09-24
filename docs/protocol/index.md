# Protocol model

Protocols are the durable records that let an epic survive agent restarts and backend changes. JSON Schema files own
the serializable shape where one exists. Go types and tests own behavior. These pages preserve purpose, invariants, and
compatibility policy rather than copying mutable field inventories.

## Published contracts

| Contract | Purpose | Machine authority |
|---|---|---|
| [`coxswain.event.v1`](event.v1.md) | Append-only story transitions | [`schema/event.v1.json`](schema/event.v1.json), `internal/state/event.go` |
| [`coxswain.checkpoint.v1`](checkpoint.v1.md) | Resume context bound to an attempt and head | [`schema/checkpoint.v1.json`](schema/checkpoint.v1.json), `internal/protocol/checkpoint/` |
| [`coxswain.inbox.v1`](inbox.v1.md) | Durable leader-to-worker steer | [`schema/inbox.v1.json`](schema/inbox.v1.json), `internal/protocol/inbox/` |
| [`coxswain.wake.v1`](wake.v1.md) | Generation-based leader notification | [`schema/wake.v1.json`](schema/wake.v1.json), `internal/wake/` |
| [`coxswain.fleet.v1`](fleet.v1.md) | Resolved story state plus sourced observations | [`schema/fleet.v1.json`](schema/fleet.v1.json), `internal/state/resolve.go` |
| [`coxswain.quota.v1`](quota.v1.md) | Provider-neutral quota projection | [`schema/quota.v1.json`](schema/quota.v1.json), `internal/quota/quota.go` |

## Contracts without a published JSON Schema

Six active contracts do not yet have a schema file. This is a documentation gap, not permission to invent a shape:

- [`busy.v1`](busy.v1.md) is owned by `internal/protocol/busy/` and its port suite; the control verbs that read it are
  described in [Control verbs](control.md).
- `coxswain.question.v1` is owned by `internal/protocol/question/question.go` and its tests.
- `coxswain.artifact.v1` is owned by `internal/artifact/artifact.go` and its tests.
- `coxswain.board.v1` is owned by `cmd/cox/board.go` and the `/data.json` board endpoint tests.
- `coxswain.scorecard.v1` is owned by `internal/scorecard/scorecard.go` and its tests.
- `coxswain.lab.v1` is owned by `internal/lab/lab.go` and its tests.

Consumers must use those executable owners until a schema is deliberately added and tested. The review workflow is
documented in [Visual review](../review.md); question semantics are documented in [Handoff](../handoff.md).

## Compatibility rule

Additive optional data may remain within `v1` only when older readers can safely ignore it. Removing, renaming, or
making an optional field required needs a new major schema id. Unknown top-level data must not crash a tolerant reader,
and unknown external evidence must not be converted into a known result.

## For operators and contributors

- Operators should use CLI commands, not edit records by hand.
- Contributors should change the executable owner, schema, tests, and its thin protocol page together.
- Historical examples and version-specific observations belong in [Evidence](../evidence/index.md), not in the current
  contract.
