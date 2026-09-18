# Documentation map

as of <repo> @ <sha> · verified by <name> <date>

Rule: before stating a fact about the system, find its topic below and verify against the owning doc's code
anchors. If your doc is not the owner, link; never restate. Drift goes to `_stale-report.md`.

## Ground-truth anchors (verify against code, not against other docs)
- <topic>: `<path/to/file.ts>`

## Owners
| Topic | Owning doc |
|---|---|
| Architecture, layers, request pipeline | `reference/architecture.md` |
| Tables, schemas, invariants | `reference/data-model.md` |
| HTTP/MCP surface, auth, errors | `reference/api.md` (narrative) + generated OpenAPI |
| Conventions (config, testing, migrations, lint) | `reference/conventions.md` |
| Run, migrate, deploy, debug | `how-to/*.md` |
| Why things are the way they are | `decisions/*.md` |
| Known drift and gaps | `_stale-report.md` |

## Not current
`history/` - dated plans, specs, generated scans. Frozen; correct as history only.
