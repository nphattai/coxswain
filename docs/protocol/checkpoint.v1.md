# `coxswain.checkpoint.v1`

A checkpoint carries the minimum durable context a future worker session needs to continue one story safely. It is
bound to the story, attempt, and git head so stale progress cannot be injected into a different execution context.

## Invariants

- Identity and freshness metadata are machine-checked before injection.
- Human sections preserve intent, constraints, decisions, failed attempts, verified outcomes, current state,
  outstanding steering, next action, and open questions.
- A checkpoint is resume context, not story state and not proof that an external effect completed.
- Parking requires a fresh checkpoint because stopping a worker without recoverable intent would discard work.

The exact frontmatter contract is [`schema/checkpoint.v1.json`](schema/checkpoint.v1.json). The executable owner is
`internal/protocol/checkpoint/`; the document template is `templates/checkpoint.md`.

## Example

The JSON below represents the checkpoint frontmatter that Coxswain validates:

```json
{
  "schema": "coxswain.checkpoint.v1",
  "story": "pipo-admin-cicd",
  "attempt": 2,
  "head": "a1b2c3d",
  "base": "origin/epic/pipo-admin@9f8e7d6",
  "written_at": "2026-09-15T02:10:00Z",
  "reason": "park"
}
```

## Compatibility

Optional additive metadata may remain in `v1` when older readers can ignore it safely. Renaming, removing, or requiring
a previously optional field needs a new major schema id. Changes must update the schema, parser, freshness tests, and
this rationale together.

See [Handoff](../handoff.md) for checkpoint direction and acknowledgement.
