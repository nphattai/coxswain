# Quickstart

Ten steps from a clean machine to one story dispatched, steered, parked, resumed, and closed. This mirrors the real
end-to-end lifecycle in `tests/e2e/dispatch-live.sh`.

coxswain needs a **backend** (Orca by default, or herdr - see `docs/adapters/orca.md` / `docs/adapters/herdr.md`)
and a **harness** for the leader and each worker (claude or codex - see `docs/adapters/claude.md` /
`docs/adapters/codex.md`). This walkthrough assumes Orca and the claude harness, the defaults.

## 1. Confirm prerequisites

Install and authenticate Orca (the default backend), and install the `claude` CLI (or `codex`, if you are running
codex as leader or worker). coxswain does not manage either of these; `cox doctor` only checks for its own
installation and epic control directories, not for Orca or the harness CLI itself.

## 2. Install `cox` and the coxswain plugin

Download the goreleaser release archive for your OS/arch (`cox_<version>_<os>_<arch>.tar.gz`) and put the `cox`
binary on your `PATH`. If you are working inside Claude Code, also install the plugin:

```
claude plugin marketplace add nphattai/coxswain
claude plugin install coxswain@coxswain
```

The first command registers this repo's marketplace; the second installs the `coxswain` plugin (its hooks, skills,
and agents). For codex, the harness reads `AGENTS.md` and the `skills/` directory directly; no plugin install step is
needed.

Verify with:

```
cox version
cox doctor
```

## 3. Initialize a workspace

```
cox workspace init --root ~/Work/my-workspace
```

This scaffolds `cox/workspace.json`, `cox/policy.json`, and `cox/services/` under the root (leaving any file that
already exists untouched). Edit `cox/workspace.json` to register your project and at least one repo alias.

## 4. Create the epic

```
cox epic new my-project my-first-epic --repo app=/path/to/your/repo --root ~/Work/my-workspace
```

`--repo alias=ref` registers the repo ad hoc if it is not already in `workspace.json` (`ref` is an absolute path or a
backend-registered repo name). This creates `<root>/my-project/epics/my-first-epic`, one worktree per repo on branch
`epic/my-first-epic`, and the alias symlinks.

## 5. Render or write the story

```
cox epic stories --epic ~/Work/my-workspace/my-project/epics/my-first-epic
```

With no `--story id=repo` flags, this renders one story skeleton per repo alias (id `<slug>-<alias>`, so
`my-first-epic-app` for the `app` alias) from the shared template into `stories/`. Edit the generated
`stories/my-first-epic-app.md` with the real prompt for the worker, or pass `--story id=repo` to name your own ids.

## 6. Dispatch the story

```
cox story dispatch my-first-epic-app --epic ~/Work/my-workspace/my-project/epics/my-first-epic
```

This builds the brief, creates the story's worktree (`story/my-first-epic-app`), spawns the worker (creating an Orca
run first if none is recorded yet), and starts the background watcher. Pass `--harness claude|codex` and `--model
<id>` to override the story frontmatter's defaults.

## 7. Wait for the worker and check state

```
cox wake wait --max 15m --epic ~/Work/my-workspace/my-project/epics/my-first-epic
cox state my-first-epic-app --epic ~/Work/my-workspace/my-project/epics/my-first-epic --json
```

`wake wait` blocks until a wake (`worker_done`, `question`, `pr_ready`, `stuck`, ...) arrives or the deadline passes
(exit 3 on timeout). `state` folds the event log plus a live liveness probe into the current view of the story.

## 8. Steer the worker

```
cox steer my-first-epic-app "keep the diff small" --epic ~/Work/my-workspace/my-project/epics/my-first-epic
```

This writes a durable steer record to the worker's inbox and knocks on its terminal. Add `--fyi` for a note that
never interrupts a running turn, or `--override "<why>"` to exceed the per-story steer budget.

## 9. Park and resume

```
cox story park my-first-epic-app --epic ~/Work/my-workspace/my-project/epics/my-first-epic
cox story resume my-first-epic-app --epic ~/Work/my-workspace/my-project/epics/my-first-epic
```

`park` refuses to park blind: the worker must have written a fresh checkpoint first, or `park` waits for one (bounded
by `--park-wait`, default from `COX_PARK_WAIT`). `resume` relaunches the worker in the same worktree at the next
attempt, injecting the checkpoint.

## 10. Complete the story and close the epic

```
cox story done my-first-epic-app --epic ~/Work/my-workspace/my-project/epics/my-first-epic --merge <sha> --close-worktree
cox epic close --epic ~/Work/my-workspace/my-project/epics/my-first-epic --yes
```

`story done` records completion (`--merge <sha>` is the merge commit the captain produced; `--close-worktree` detaches
and removes the story worktree, keeping the branch). `epic close` tears down remaining workers, services, resources,
and worktrees in a confirmed order, then archives `.cox` to `.cox.closed`. Branches are never deleted by either
command.

## Next

- `docs/ARCHITECTURE.md` for the model behind these commands.
- `docs/handoff.md` for the five leader/worker channels (brief, steer, control, status, checkpoint).
- `docs/adapters/` for what each backend and harness actually supports.
