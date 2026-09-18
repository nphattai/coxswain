# coxswain

coxswain is a Go CLI (`cox`) for multi-agent epic orchestration. A captain designs and scopes an epic by chatting
with a leader session; the leader dispatches one AI worker per repo into its own isolated git worktree, supervises
them over a durable event log, and reports back. The captain reviews and merges every change - coxswain never merges
or deletes a branch on its own. It is built for a captain or leader running multi-repo epics with AI workers, not for
a single-repo, single-agent workflow.

## Requirements

- A **backend** that can create worktrees, spawn workers, and probe liveness: **Orca** (default) or **herdr**
  (optional; runs in a documented reduced mode - see `docs/adapters/herdr.md`).
- A **harness** for the leader and each worker: **claude** (Claude Code, wake delivered by hooks) or **codex** (wake
  delivered by polling `cox wake wait`). See `docs/adapters/claude.md` and `docs/adapters/codex.md`.

There is no tmux backend; only Orca and herdr adapters exist today. The `Backend` interface
(`internal/adapter/backend/backend.go`) is small enough that a community adapter could add one later.

## Install

coxswain ships two things:

- The **coxswain Claude Code plugin** - this repo, installed from a plugin marketplace:

  ```
  claude plugin marketplace add nphattai/coxswain
  claude plugin install coxswain@coxswain
  ```

  The first command registers this repo's marketplace; the second installs the `coxswain` plugin from it. For codex,
  the harness reads `AGENTS.md` and `skills/` directly, so no plugin install is needed.
- The **`cox` binary** - published as goreleaser release archives (`cox_<version>_<os>_<arch>.tar.gz` for darwin and
  linux, amd64 and arm64; see `.goreleaser.yaml`). Extract the archive and put `cox` on your `PATH`. The archive also
  carries `AGENTS.md`, `hooks/`, `templates/`, and `skills/` alongside the binary for a binary-only install.

Run `cox doctor` after installing to confirm the binary is on `PATH` and to list every coxswain installation and
epic control directory it finds.

## `cox workspace init`

```
cox workspace init [--from-repos-md <path>] [--root <dir>]
```

- `--root <dir>` - workspace root to scaffold into (default `.`).
- `--from-repos-md <path>` - seed `workspace.json`'s `repos[]` from a v1 `docs/repos.md` table.

It creates `cox/workspace.json`, `cox/policy.json`, and `cox/services/` under the root, leaving any file that already
exists untouched. `workspace.json` is the registry of projects, repos, services, and hosts:

```json
{
  "projects": [{ "name": "example-project", "path": "example-project" }],
  "repos": [
    { "alias": "backend", "name": "org/backend-repo", "production": "master", "staging": "release" },
    { "alias": "web", "name": "org/web-repo", "production": "main", "staging": "" }
  ],
  "services": [{ "alias": "backend", "script": "services/backend.sh" }],
  "hosts": [{ "name": "local" }]
}
```

`policy.json` holds topology and thresholds that have a reason and a review condition: workers-per-repo, wave
ordering, context-compaction budgets, the arena trigger, delivery style, and the harness options/default for leader,
worker, and arena roles (`templates/policy.json` is the shipped default).

## Your first story

```
cox workspace init --root ~/Work/my-workspace
# edit ~/Work/my-workspace/cox/workspace.json to register your project and repo alias

cox epic new my-project my-first-epic --repo app=/path/to/repo --root ~/Work/my-workspace
cox epic stories --epic ~/Work/my-workspace/my-project/epics/my-first-epic
cox story dispatch my-first-epic-app --epic ~/Work/my-workspace/my-project/epics/my-first-epic
cox state --epic ~/Work/my-workspace/my-project/epics/my-first-epic --json
cox story done my-first-epic-app --epic ~/Work/my-workspace/my-project/epics/my-first-epic --close-worktree
cox epic close --epic ~/Work/my-workspace/my-project/epics/my-first-epic --yes
```

`epic new` creates the epic directory, one worktree per repo on branch `epic/<slug>`, and the alias symlinks. `epic
stories` renders one story per repo alias from the shared template. `story dispatch` builds the brief, creates the
story worktree, and spawns the worker. `state` folds the epic's append-only event log into the current view of every
story. `story done` records completion (add `--merge <sha>` once the captain has merged the PR); `epic close` tears
down workers, services, resources, and worktrees in a confirmed order and archives `.cox`.

See `docs/QUICKSTART.md` for a full ten-step walkthrough that includes steering, parking, and resuming a worker.

## More docs

- `docs/QUICKSTART.md` - clean machine to one story running end to end.
- `docs/ARCHITECTURE.md` - the leader/worker model, event log, worktrees, arena, and harness policy.
- `plans/260914-crewkit-v2-architecture/architecture.html` - the visual version of the architecture (self-contained,
  open in a browser).
- `docs/handoff.md` - how a leader and a worker hand work to each other (the five channels: brief, steer, control,
  status, checkpoint).

## Rules that never bend

The captain owns the epic lifecycle and every merge; design is never delegated. No tool deletes a git branch, ever -
worktree removal always detaches first. The leader audits every worker's output before the captain sees it. Workers
never create ports, databases, containers, or devices themselves; environment ownership goes through `cox env` and
the project's service adapter.
