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
| `model` | render, editable | The model id (or alias: `opus` → the policy's `harness.worker.models.claude`, e.g. `claude-opus-5-5`). Blank lets policy pick the harness default. |
| `title` | render, editable | One-line story title; also the H1 of the body. |
| `policy_source` | render | The `cox/policy.json` file and short sha the story's delivery/context/harness values were resolved from. Audit stamp; do not hand-edit. |
| `delivery` | render | The resolved delivery style (`default` \| `pipo`), copied from policy so the worker's rules are fixed at render time. |
| `mode` | render, editable | The resolved delivery mode (`no-mistakes` \| `direct-PR` \| `local-only`), from policy `delivery.mode` (item 8). The brief prints `Delivery contract: mode=<mode> yolo=<on\|off>`; `cox story done --merge <sha>` enforces the sha is landed on the branch the mode requires. Overridable per story. |
| `route` | editable | The routing match a model's judgment wrote (item 10): `rule=<n>` (1-based, the `routing.rules` entry that applies) or `override` (skip the rules, use `routing.default_profiles`). Blank means no match; `cox route`/`cox story dispatch` then use the default array or the baseline ladder. The rule's profile array is resolved by code (gates + `spendPriority`); a pin the matched rule forbids is refused. |
| `effort` | render, editable | The reasoning-effort class for routing's floor gate (`low` \| `medium` \| `high` \| `xhigh` \| `max`; item 10). Blank uses the `routing.effort` default for the story's kind (`scout` xhigh, `ship` low, `arena` high). |
| `kind` | render, editable | The story kind (`ship` \| `scout`, default `ship`; item 9). A `scout` reports only (no PR): its deliverable is `<epic>/reports/<id>.md`, `cox story done` refuses to complete it without that report, and `cox audit pr` / `cox state` skip the forge for it. `cox epic stories --story id=repo:scout` renders one; `cox story promote <id>` flips it back to `ship`. |

## Example

```yaml
---
id: acme-checkout-api
repo: api
depends: []
device: false
host:
agent: claude
model: claude-opus-5-5
title: Checkout API - idempotent order creation
policy_source: cox/policy.json@0ebbf6ece9a4
delivery: default
mode: direct-PR
kind: ship
---
```

`agent: auto` routes the harness at dispatch from policy; a fixed `agent`/`harness` skips routing. See
[First epic](../getting-started/first-epic.md) for how a story is dispatched and
[`policy.json`](policy-json.md) for the delivery and harness policy the render resolves.
