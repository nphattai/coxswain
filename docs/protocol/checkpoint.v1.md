# coxswain.checkpoint.v1

## Purpose

A checkpoint is what a worker leaves for its own future self so that a restart or compaction is a non-event: the fresh
session reads it and continues without losing constraints, repeating failed attempts, or repeating a side effect that
already happened. It lives at `<epic>/handoffs/<id>.md` as a markdown file with YAML frontmatter. The frontmatter is the
machine contract validated here; the markdown body carries the human-readable sections.

`cox checkpoint facts` fills the computable part (head, dirty files, unhandled inbox, PR, CI). `cox checkpoint inject`
prints the checkpoint and warns when `head` differs from the current HEAD or `attempt` does not match, so a stale or
wrong-attempt checkpoint is never injected silently (F07).

## Fields (frontmatter)

| Field | Type | Required | Meaning |
|---|---|---|---|
| `schema` | string | yes | Always `coxswain.checkpoint.v1`. |
| `story` | string | yes | Story id this checkpoint belongs to. |
| `attempt` | integer | yes | Attempt that wrote it (>= 1); resume rejects a mismatch. |
| `head` | string | yes | HEAD sha at write time; resume warns if it differs from current HEAD. |
| `base` | string | yes | Base branch and sha, e.g. `origin/epic/<slug>@<sha>`. |
| `written_at` | string | yes | RFC 3339 UTC timestamp. |
| `reason` | string | yes | Why it was written: `phase-end`, `plan-compact`, `compact-now`, `park`, `precompact-auto`. |

Body sections (not schema-validated, but required by the template): Intent, Constraints, Decisions, Failed attempts,
Verified outcomes, Current state, Outstanding steering, Next action, Open questions + chosen assumptions.

## Example

The validated object is the frontmatter:

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

As it appears on disk:

```markdown
---
schema: coxswain.checkpoint.v1
story: pipo-admin-cicd
attempt: 2
head: a1b2c3d
base: origin/epic/pipo-admin@9f8e7d6
written_at: 2026-09-15T02:10:00Z
reason: park
---
## Intent
...verbatim from the story, not re-interpreted...
## Next action
Rerun the failing migration test, then open the PR.
```

## Versioning

- Schema id `coxswain.checkpoint.v1`. JSON Schema: [`schema/checkpoint.v1.json`](./schema/checkpoint.v1.json) (draft 2020-12).
- Adding an optional frontmatter field or a new `reason` value keeps `v1`.
- The top-level object is `additionalProperties: true`: a reader ignores unknown top-level fields, so a field a newer
  producer adds never makes an older reader reject the record.
- Removing/renaming a field or making an optional one required increments the major (`coxswain.checkpoint.v2`).
- Changes are recorded in this file's history, never applied silently.
