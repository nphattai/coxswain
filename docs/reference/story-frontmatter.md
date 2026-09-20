# Story frontmatter reference

A story file (`<epic>/stories/<id>.md`) opens with a YAML frontmatter block. `cox epic stories` renders it from
`templates/story.md` with the project policy resolved once; `cox story dispatch` reads `repo`, `agent`/`harness`, and
`model` from it (`cmd/cox/story.go`). The authoritative shape is the template plus that reader, and this page is kept in
parity with them by `internal/workspace/docs_parity_test.go`, which fails if the template gains a frontmatter key that is
not documented here.

## Fields

| Field | Set by | Meaning |
|---|---|---|
| `id` | render | The story id, unique within the epic. It names the worktree branch `story/<id>` and the inbox/handoff paths. |
| `repo` | render, editable | The repo alias (from `workspace.json`) this story works in. Dispatch resolves the worktree from it. |
| `depends` | render, editable | Story ids this story depends on (a list). A client story depends on the backend story that lands the contract. |
| `device` | render, editable | Whether the story needs a physical device (`true`/`false`). |
| `host` | render, editable | The host the worker runs on (`workspace.json` `hosts`); blank means `local`. |
| `agent` | render, editable | The worker harness (`claude` \| `codex` \| `auto`). Resolved from policy at render time. |
| `harness` | editable | Accepted as a synonym for `agent` by the dispatch reader; set either one. An explicit `--harness` flag overrides it. |
| `model` | render, editable | The model id (or alias, e.g. `opus` → `claude-opus-4-8`). Blank lets policy pick the harness default. |
| `title` | render, editable | One-line story title; also the H1 of the body. |
| `policy_source` | render | The `cox/policy.json` file and short sha the story's delivery/context/harness values were resolved from. Audit stamp; do not hand-edit. |
| `delivery` | render | The resolved delivery style (`default` \| `pipo`), copied from policy so the worker's rules are fixed at render time. |

## Example

```yaml
---
id: acme-checkout-api
repo: api
depends: []
device: false
host:
agent: claude
model: claude-opus-4-8
title: Checkout API - idempotent order creation
policy_source: cox/policy.json@0ebbf6ece9a4
delivery: default
---
```

`agent: auto` routes the harness at dispatch from policy; a fixed `agent`/`harness` skips routing. See
[First epic](../getting-started/first-epic.md) for how a story is dispatched and
[`policy.json`](policy-json.md) for the delivery and harness policy the render resolves.
