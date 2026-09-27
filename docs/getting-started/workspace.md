# Create a workspace

A **workspace** is the folder a leader works from. It is not a repo: it sits next to your repos and holds the registry
(`cox/workspace.json`), the behavioral policy (`cox/policy.json`), the leader hooks, and the skills the leader reads. One
command creates all of it.

## One command

```bash
cox workspace init \
  --root $HOME/Work/acme-ops \
  --repo api=$HOME/Work/acme-api
```

`--repo` is `<alias>=<absolute-path>[:<production-branch>]` and repeatable. When you omit `:production`, init reads the
checkout's default branch (`origin/HEAD`, else `main`). With no `--repo`, init refuses rather than write a placeholder
you would have to hand-edit.

Add a repo later with `cox workspace add-repo api=$HOME/Work/acme-api --root $HOME/Work/acme-ops`; re-running `cox workspace init` is idempotent - it
reports what already exists and creates only what is missing, and never rewrites `workspace.json`, `policy.json`, or an
edited `AGENTS.md`.

## What it wrote, and why

| Path | Why |
|---|---|
| `cox/workspace.json` | The registry: your repos, their production branches, and (optionally) projects, services, hosts. See [`workspace.json`](../reference/workspace-json.md). |
| `cox/policy.json` | Behavioral defaults with a recorded reason each (waves, context budgets, harness options, delivery style). See [`policy.json`](../reference/policy-json.md). |
| `cox/services/` | Where a project service adapter (`<alias>.sh`) goes when an epic runs a backend. |
| `.gitignore` | Ignores the machine-bound paths: `cox/workspace.json`, `cox/.cache/`, every `**/.cox/` and `**/.cox.closed/`, `.pi/extensions/` (the per-machine Pi leader extension), and one `**/epics/*/<alias>` rule per repo (the worktree symlinks). |
| `AGENTS.md` | The leader instruction skeleton (points at the leader skills and the every-turn inbox-drain rule) that Codex reads. |
| `.agents/skills/` | Embedded copies of the `cox-*` leader skills, so Claude and Codex read the same set. |
| `.claude/settings.json` | Claude leader hooks: the four groups (`UserPromptSubmit`, `Stop`, `PreCompact`, `SessionStart`) as `cox hook` commands, merged with any existing entries. |
| `.codex/hooks.json` | The same hook groups for a Codex leader. |
| `.pi/extensions/` | The Pi leader extension (installed when `pi` is a leader option): a per-machine, gitignored, hash-verifiable extension that drives the same four `cox hook` behaviours for a Pi leader. See the [Pi adapter](../adapters/pi.md). |

Hooks belong to the **workspace**, not to an epic: one leader terminal per project drains and rewakes for every active
epic under the workspace. Re-run just the hooks with `cox workspace hooks --harness claude|codex|pi`.

Verify the result any time:

```bash
cox doctor --root $HOME/Work/acme-ops
```

## Three workspace shapes

### Shape A - a company ops workspace over several repos

Your product spans several repos. The workspace is a sibling ops folder; each repo is a registry entry addressed by
alias. From an empty folder to a dispatched story:

```bash
cox workspace init \
  --root $HOME/Work/acme-ops \
  --repo api=$HOME/Work/acme-api \
  --repo web=$HOME/Work/acme-web \
  --repo infra=$HOME/Work/acme-infra

cox epic new acme checkout \
  --repo api --repo web \
  --root $HOME/Work/acme-ops

cox epic stories --epic $HOME/Work/acme-ops/acme/epics/checkout
# edit stories/checkout-api.md (goal, scope, owned files, proof), then:
cox story dispatch checkout-api --epic $HOME/Work/acme-ops/acme/epics/checkout
```

### Shape B - a personal monorepo (sibling ops folder)

You have one monorepo (`$HOME/Work/henrylab`) with products under `apps/`. The workspace is a **sibling** ops folder,
`$HOME/Work/henrylab-ops`, whose single repo entry points at the monorepo. Epics are named `<product>-<slug>`, and each
story pins its product by scoping `apps/<product>` in its text:

```bash
cox workspace init \
  --root $HOME/Work/henrylab-ops \
  --repo henrylab=$HOME/Work/henrylab

cox epic new henrylab notes-sync \
  --repo henrylab \
  --root $HOME/Work/henrylab-ops

cox epic stories --epic $HOME/Work/henrylab-ops/henrylab/epics/notes-sync
# in stories/notes-sync-henrylab.md, scope the work to apps/notes, then:
cox story dispatch notes-sync-henrylab --epic $HOME/Work/henrylab-ops/henrylab/epics/notes-sync
```

### Shape C - one repo, in-repo workspace (Orca only)

The workspace root **is** the product repo checkout: `cox/`, the hooks, and the epic tree live in the repo itself, so
DESIGN, stories, reports and `ledger.jsonl` become ordinary tracked docs of the product. It runs on the Orca backend
only: Orca places every worktree under its own directory (`~/orca/workspaces/<repo>/<name>`), outside the repo. That
placement is Orca's convention, not a cox check, so worktrees never nest under the checkout they are cut from.

Preflight first. Init's ignore rules (`**/.cox/`, `**/epics/*/<alias>`) are repo-wide, so they would silently hide any
product path that already matches them. This must print nothing:

```bash
git -C $HOME/Work/myrepo ls-files | grep -E '(^|/)\.cox/|/epics/'
```

Then init with the repo registered at the root, and put epics under `ops/` (`project=ops`, so an epic is
`$REPO/ops/epics/<slug>`):

```bash
cox workspace init --root $HOME/Work/myrepo --repo myrepo=$HOME/Work/myrepo

# bootstrap PR: commit every path init generated, the captain merges it to main
#   cox/policy.json .claude/settings.json .codex/hooks.json .agents/skills/ .gitignore AGENTS.md

cox epic new ops feature-x --repo myrepo --root $HOME/Work/myrepo
cox epic stories --epic $HOME/Work/myrepo/ops/epics/feature-x
cox story dispatch feature-x-myrepo --epic $HOME/Work/myrepo/ops/epics/feature-x
```

Rules that come with the shape:

- **Bootstrap commit.** `cox epic new` cuts `epic/<slug>` from the production branch, not from the leader's working
  tree, so workers see the generated files only once they are on `main`. Commit every path init generated in one PR
  and have the captain merge it before the first `cox epic new`.
- **`ops/**` only.** The leader keeps writing the epic tree on the root checkout's default branch. It commits and
  pushes `ops/**` and nothing else there; every product change goes through a story branch and a captain-merged PR.
- **Develop in a worktree.** Every agent session opened in the repo root runs the leader hooks and counts as a leader
  terminal (`cox doctor` reports two as an ISSUE). While an epic is active, do other development in an Orca worktree
  of the repo, not in the root checkout.
- **One `AGENTS.md`, two roles.** The leader and every worker read the same file. `cox/workspace.json` exists only in
  the leader checkout (it is gitignored), and `COX_STORY` is set only in a worker; the leader-only wake hooks skip a
  worker session on that variable.

Next: [First epic](first-epic.md) explains each command above in full.
