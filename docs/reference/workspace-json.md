# `workspace.json` reference

`cox/workspace.json` is the workspace registry: the projects, repositories, services, and hosts a leader resolves
against. `cox workspace init` writes it; you edit it by hand or with `cox workspace add-repo`. The authoritative shape
is `internal/workspace/workspace.go` and the seed is `templates/workspace.json`. This page is kept in parity with the Go
type by `internal/workspace/docs_parity_test.go`, which fails if a field is added or renamed in Go without a matching
row here.

Loading validates the file: every repo needs a unique `alias`, a `path` or a `name`, an absolute `path` when set, and a
non-empty `production`. A validation error names the field and the file.

## Top-level object

| Field | Type | Required | Meaning |
|---|---|---|---|
| `repos` | array of [Repo](#repo) | yes | Every repository the workspace tracks. Dispatch addresses a repo by its alias. |
| `projects` | array of [Project](#project) | no | Product folders under the workspace root. Informational; `cox epic new` addresses repos, not projects. |
| `services` | array of [Service](#service) | no | Project service adapters (`cox/services/<alias>.sh`) for epics that run a backend. |
| `hosts` | array of [Host](#host) | no | Machines a worker can run on. `local` is the machine the leader runs on. |
| `worktree_base` | string (path) | no | Where a backend that owns its own git worktrees (herdr) creates them. Orca manages its own paths and ignores this. |

## Repo

One repository entry. A repo carries either a `path` (absolute checkout on this machine) or a `name` (a
backend-registered name); an absolute `path` wins when both are set.

| Field | Type | Required | Meaning |
|---|---|---|---|
| `alias` | string | yes | Short handle used everywhere (`--repo <alias>`, the worktree symlink). A single path-safe component: `[A-Za-z0-9._-]`, never `.`, `..`, or containing a separator. |
| `path` | string (absolute) | one of path/name | Absolute checkout path on this machine. |
| `name` | string | one of path/name | Backend-registered repository name (Orca) when the repo has no local checkout path. |
| `production` | string (branch) | yes | The branch epics cut from. `cox workspace init` defaults it to the checkout's `origin/HEAD`, else `main`. |
| `staging` | string (branch) | no | The integration branch for a two-track release flow. |

## Project

| Field | Type | Required | Meaning |
|---|---|---|---|
| `name` | string | yes | Project (product) name. |
| `path` | string | yes | Path to the project folder, relative to the workspace root. |

## Service

Names a project service adapter script with the four verbs `preflight | start | health | stop`.

| Field | Type | Required | Meaning |
|---|---|---|---|
| `alias` | string | yes | Service handle, matched to an epic's backend. |
| `script` | string | yes | Path to the adapter script, e.g. `services/backend.sh`. |

## Host

| Field | Type | Required | Meaning |
|---|---|---|---|
| `name` | string | yes | Machine name. `local` is the leader's machine. |
| `ssh` | string | no | SSH target for a remote host. |

## Example

Two repos addressed by absolute path, one host:

```json
{
  "repos": [
    { "alias": "api", "path": "/Users/you/Work/acme-api", "production": "main" },
    { "alias": "web", "path": "/Users/you/Work/acme-web", "production": "main", "staging": "release" }
  ],
  "projects": [],
  "services": [],
  "hosts": [{ "name": "local" }]
}
```

See [Create a workspace](../getting-started/workspace.md) for the three workspace shapes and a worked example of each, and
[Configuration authority](configuration.md) for how workspace facts relate to policy.
