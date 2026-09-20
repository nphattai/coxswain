# Create a workspace

A **workspace** is the folder a leader works from. It is not a repo: it sits next to your repos and holds the registry
(`cox/workspace.json`), the behavioral policy (`cox/policy.json`), the leader hooks, and the skills the leader reads. One
command creates all of it.

## One command

```bash
cox workspace init \
  --root ~/Work/acme-ops \
  --repo api=~/Work/acme-api
```

`--repo` is `<alias>=<absolute-path>[:<production-branch>]` and repeatable. When you omit `:production`, init reads the
checkout's default branch (`origin/HEAD`, else `main`). With no `--repo`, init refuses rather than write a placeholder
you would have to hand-edit.

Add a repo later with `cox workspace add-repo api=~/Work/acme-api`; re-running `cox workspace init` is idempotent - it
reports what already exists and creates only what is missing, and never rewrites `workspace.json`, `policy.json`, or an
edited `AGENTS.md`.

## What it wrote, and why

| Path | Why |
|---|---|
| `cox/workspace.json` | The registry: your repos, their production branches, and (optionally) projects, services, hosts. See [`workspace.json`](../reference/workspace-json.md). |
| `cox/policy.json` | Behavioral defaults with a recorded reason each (waves, context budgets, harness options, delivery style). See [`policy.json`](../reference/policy-json.md). |
| `cox/services/` | Where a project service adapter (`<alias>.sh`) goes when an epic runs a backend. |
| `.gitignore` | Ignores the machine-bound paths: `cox/workspace.json`, `cox/.cache/`, every `**/.cox/` and `**/.cox.closed/`, and one `**/epics/*/<alias>` rule per repo (the worktree symlinks). |
| `AGENTS.md` | The leader instruction skeleton (points at the leader skills and the every-turn inbox-drain rule) that Codex reads. |
| `.agents/skills/` | Embedded copies of the `cox-*` leader skills, so Claude and Codex read the same set. |
| `.claude/settings.json` | Claude leader hooks: the four groups (`UserPromptSubmit`, `Stop`, `PreCompact`, `SessionStart`) as `cox hook` commands, merged with any existing entries. |
| `.codex/hooks.json` | The same hook groups for a Codex leader. |

Hooks belong to the **workspace**, not to an epic: one leader terminal per project drains and rewakes for every active
epic under the workspace. Re-run just the hooks with `cox workspace hooks --harness claude|codex`.

Verify the result any time:

```bash
cox doctor --root ~/Work/acme-ops
```

## Two workspace shapes

### Shape A - a company ops workspace over several repos

Your product spans several repos. The workspace is a sibling ops folder; each repo is a registry entry addressed by
alias. From an empty folder to a dispatched story:

```bash
cox workspace init \
  --root ~/Work/acme-ops \
  --repo api=~/Work/acme-api \
  --repo web=~/Work/acme-web \
  --repo infra=~/Work/acme-infra

cox epic new acme checkout \
  --repo api --repo web \
  --root ~/Work/acme-ops

cox epic stories --epic ~/Work/acme-ops/acme/epics/checkout
# edit stories/checkout-api.md (goal, scope, owned files, proof), then:
cox story dispatch checkout-api --epic ~/Work/acme-ops/acme/epics/checkout
```

### Shape B - a personal monorepo (sibling ops folder)

You have one monorepo (`~/Work/henrylab`) with products under `apps/`. The workspace is a **sibling** ops folder,
`~/Work/henrylab-ops`, whose single repo entry points at the monorepo. Epics are named `<product>-<slug>`, and each
story pins its product by scoping `apps/<product>` in its text:

```bash
cox workspace init \
  --root ~/Work/henrylab-ops \
  --repo henrylab=~/Work/henrylab

cox epic new henrylab notes-sync \
  --repo henrylab \
  --root ~/Work/henrylab-ops

cox epic stories --epic ~/Work/henrylab-ops/henrylab/epics/notes-sync
# in stories/notes-sync-henrylab.md, scope the work to apps/notes, then:
cox story dispatch notes-sync-henrylab --epic ~/Work/henrylab-ops/henrylab/epics/notes-sync
```

### Do not put the workspace inside a monorepo

The in-repo layout - a `cox/` and epic tree inside the monorepo working tree - is **unsupported**. The epic tree,
`.cox/` state, and worktree symlinks would land in the repo's working tree (polluting its status and diffs), and the
worktrees would nest under the repo they are cut from. Keep the workspace in a sibling `<repo>-ops` folder, as shape B
shows.

Next: [First epic](first-epic.md) explains each command above in full.
