# First epic

You have a workspace ([Create a workspace](workspace.md)). This page takes one epic from creation to a dispatched story,
then points at Operate for supervision, audit, and release. Every command in a fenced block below is exercised by the
onboarding end-to-end test, so you can copy it with confidence.

## 1. Create the epic

```bash
cox epic new acme checkout \
  --repo api --repo web \
  --root ~/Work/acme-ops
```

`cox epic new <project> <slug>` creates the epic directory (`<root>/<project>/epics/<slug>` with `DESIGN.md`, `repos`,
and the `.cox/` state tree), one Orca worktree per repo on branch `epic/<slug>` (cut from each repo's production branch),
and the alias symlinks into the epic dir. It announces each outward effect before doing it:

- it **pushes** `epic/<slug>` to origin - pass `--no-push` to keep it local;
- it writes a trust entry to `~/.claude.json` so the harness may run in the new worktrees.

An epic with no backend gets an `epic.env` carrying only `EPIC` and `PROJECT` (no ports allocated).

## 2. Render the stories

```bash
cox epic stories --epic ~/Work/acme-ops/acme/epics/checkout
```

This renders one story per repo from `templates/story.md`, resolving the project policy once (delivery style, context
budgets, harness and model), and stamps `policy_source` with the file and sha. Open `stories/checkout-api.md` and fill
its Goal, Scope, Acceptance criteria, Verification table, and owned files - the story is the worker's whole contract.

The frontmatter block controls dispatch. `agent` (or `harness`) picks the worker harness, `model` pins the model, `repo`
is the alias it works in, `delivery` is the resolved style. See the [Story frontmatter reference](../reference/story-frontmatter.md)
for every field.

## 3. Dispatch a story

```bash
cox story dispatch checkout-api --epic ~/Work/acme-ops/acme/epics/checkout
```

The worker runs in its own worktree on `story/<id>`. `agent: auto` routes the harness from policy; a fixed harness skips
routing. That is the setup goal reached: a dispatched story.

## 4. The leader wake loop

A leader supervises through **wakes**, never by typing into the worker's terminal. How a wake reaches the leader depends
on the harness:

- **Push (Claude Code):** leader hooks open a turn automatically when a wake is queued - installed by
  `cox workspace init`, nothing to run.
- **Pull (Codex, and any harness without hooks):** the leader drains at the start of each turn and blocks when idle:

  ```bash
  cox wake wait --max 15m --epic ~/Work/acme-ops/acme/epics/checkout
  ```

A wake is a durable notification, not the source of truth; the event log and `cox state` own story state.

## 5. Supervise, audit, and close (Operate)

Once a story is working, supervision moves to [Operate](../operations/index.md) and [Handoff](../handoff.md):

- Steer within scope with `cox steer`, and answer a worker's question with `cox reply`.
- When the worker reports a PR, audit it with `cox audit pr <story> --epic <dir>`.
- The **captain** merges the PR (Coxswain never merges and never deletes a branch), then records it with
  `cox story done <story> --merge <sha> --close-worktree --epic <dir>`.
- Release a story's services (backend epics only) with `cox env release <story> --epic <dir>`.

See [Dispatch, supervise, recover](../handoff.md) for the channel and acknowledgement rules and
[Operating map](../operations/index.md) for the full captain and leader routes.

## Attach the epic on a second machine

An epic directory (with `DESIGN.md`, `repos`, and stories) that has been committed and pushed can be re-attached on a
fresh clone or a second machine - it recreates `.cox/`, the worktrees on the existing `epic/<slug>` branch (fetched,
never recreated), and the alias symlinks:

```bash
cox epic attach --epic ~/Work/acme-ops/acme/epics/checkout
```

`attach` refuses if a `.cox/` already exists or a worktree is dirty, so it never clobbers live state.
