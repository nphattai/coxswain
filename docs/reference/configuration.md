<span id="configuration" aria-hidden="true"></span>

# Configuration authority

Coxswain separates workspace registry facts from justified behavioral policy. The shipped templates and Go loaders are
the authoritative shape. This page explains where a decision belongs and how precedence works.

## Authority map

| Surface | Purpose | Machine authority |
|---|---|---|
| `cox/workspace.json` | Projects, repositories, services, hosts, and backend worktree location | `templates/workspace.json`, `internal/workspace/workspace.go` |
| `cox/policy.json` | Workspace-wide behavioral defaults and their rationale | `templates/policy.json`, `internal/workspace/policy.go` |
| `<project>/cox/policy.json` | Project-specific replacement of selected policy sections | `internal/workspace/policy.go:Resolve` and tests |
| Story frontmatter | Per-story harness, model, repo, ownership, and resolved delivery facts | `templates/story.md`, `cmd/cox/story.go`, `internal/epic/stories.go` |
| Process environment | Narrow operational overrides documented by the owning command | `cmd/cox/` and adapter launch code |

Do not copy the template into documentation. Inspect the current template before editing a workspace, and run
`cox doctor` to detect drift.

## Resolution rules

Workspace policy loads first. A project policy may replace only the top-level sections it declares. Replacement is
section-granular, not a deep merge, so an overridden justified section must carry its own `why` and `review_when`.
The merged result is validated before use.

A story may pin a value that the relevant command explicitly allows, such as harness or model. Command-line overrides
are deliberate one-operation choices. Environment variables are not a general policy layer; only named variables read
by a command have authority.

## Decide where a change belongs

- Registry identity or location belongs in `workspace.json`.
- A workspace-wide behavioral default belongs in workspace policy.
- A project-specific behavioral exception belongs in that project's policy and must replace the complete section.
- A one-story choice belongs in story frontmatter or an explicit command flag.
- A compatibility fact about an external tool belongs in [Evidence](../evidence/index.md), not policy.

## Safety properties

- Missing or invalid required configuration is an error, not an empty default registry.
- Justified policy sections cannot omit their reason or review condition.
- An explicit empty launch-flag list is a captain opt-out, distinct from an absent entry.
- Optional newer sections retain code defaults so older workspace policy can still load.
- Project policy cannot silently inherit a rationale that no longer matches its replacement value.

For exact keys and defaults, read `templates/workspace.json`, `templates/policy.json`, and the types in
`internal/workspace/`. For operational commands, use [CLI map](cli.md).
