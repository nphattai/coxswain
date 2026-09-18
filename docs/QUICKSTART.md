<span id="quickstart" aria-hidden="true"></span>

# Run your first epic

This route gets one story running, supervised, and closed. It assumes the default Orca backend and a Claude leader.
Use [Install](getting-started/install.md) first, then confirm `cox doctor` succeeds.

`cox --help` indexes the command families; each command's usage output and implementation file under `cmd/cox/` own
its exact flags. The flow below is intentionally narrow.

## 1. Initialize a workspace

```bash
cox workspace init --root ~/Work/my-workspace
```

Edit `~/Work/my-workspace/cox/workspace.json` to register the project and repository alias used below. See
[Configuration](reference/configuration.md) for precedence and authority.

## 2. Create the epic and its story

```bash
cox epic new my-project my-first-epic \
  --repo app=/path/to/your/repo \
  --root ~/Work/my-workspace

cox epic stories \
  --epic ~/Work/my-workspace/my-project/epics/my-first-epic
```

Edit `stories/my-first-epic-app.md`. The story is the worker's bounded contract, so state the outcome, owned files,
proof required, and anything it must not change.

<span id="6-dispatch-the-story" aria-hidden="true"></span>

## 3. Dispatch and wait for a wake

```bash
cox story dispatch my-first-epic-app \
  --epic ~/Work/my-workspace/my-project/epics/my-first-epic

cox wake wait --max 15m \
  --epic ~/Work/my-workspace/my-project/epics/my-first-epic
```

The worker runs in its own worktree. A wake is a durable notification, not the source of story state. Inspect the
resolved view when needed:

```bash
cox state my-first-epic-app \
  --epic ~/Work/my-workspace/my-project/epics/my-first-epic
```

## 4. Supervise, do not type into the worker

Send a durable steer:

```bash
cox steer my-first-epic-app "keep the diff scoped to the accepted design" \
  --epic ~/Work/my-workspace/my-project/epics/my-first-epic
```

If work must pause, park it only after a fresh checkpoint, then resume from that checkpoint:

```bash
cox story park my-first-epic-app --epic ~/Work/my-workspace/my-project/epics/my-first-epic
cox story resume my-first-epic-app --epic ~/Work/my-workspace/my-project/epics/my-first-epic
```

[Handoff](handoff.md) explains channel ownership, acknowledgement, and recovery. [Operations](operations/index.md)
routes monitoring and failure cases.

## 5. Review and close

After the worker reports completion, audit its output. The captain reviews and merges the change. Record the merge and
close the epic only after every story is resolved:

```bash
cox story done my-first-epic-app \
  --merge <merge-sha> \
  --close-worktree \
  --epic ~/Work/my-workspace/my-project/epics/my-first-epic

cox epic close \
  --epic ~/Work/my-workspace/my-project/epics/my-first-epic \
  --yes
```

Coxswain may remove a worktree, but it never deletes the branch.

## Next

- [Core concepts](getting-started/concepts.md) for vocabulary and lifecycle.
- [Operations](operations/index.md) for the full captain and leader route.
- [Architecture](ARCHITECTURE.md) for boundaries and failure-model rationale.
