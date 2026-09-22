<h1 align="center">coxswain</h1>
<p align="center">
  <a href="https://github.com/nphattai/coxswain"
    ><img
      alt="Platform"
      src="https://img.shields.io/badge/platform-macOS%20%7C%20Linux-blue?style=flat-square"
  /></a>
</p>

<h3 align="center">Run multi-agent epics from one terminal. The captain merges everything.</h3>

## What it is

You can run one coding agent easily.
The moment you want several changes across your repos done in parallel - a fix here, an investigation there, a refactor in a third - you become a tab-juggler: babysitting sessions, copy-pasting context between checkouts, losing track of which terminal held the failing test.

Coxswain flips the model.
You talk to a single **leader** agent, and it runs a crew for you: it turns a signed design into repo-scoped **stories**, dispatches **workers** into isolated git worktrees, supervises them through durable on-disk state, and hands you finished PRs to review.
You are the **captain** - you approve the design, and you are the only one who merges.

Coxswain is not a model, not a harness, not a skill, and not an MCP server.
It is a `cox` binary plus a set of leader skills and hooks that any terminal coding agent - Claude Code or Codex - can follow.
`cox workspace init` writes those skills and hooks beside your code, so there is no service to run and nothing hidden from `git`.

Coxswain fits a single repo, a monorepo, or many repos at once.
The workspace is a folder that sits next to your code, so a monorepo is a first-class case: register it as one repo in a sibling ops workspace.
It is not a wrapper for a single prompt, and it never replaces human merge authority.

## Features

Every guarantee below is enforced by the `cox` binary and its tests, not by convention.

- **Isolated worktrees, never a deleted branch** - each story runs in its own Orca git worktree, one worker per repo, so parallel work never collides. Coxswain never deletes a git branch.
- **State survives the session and the machine** - the event log, steering, and checkpoints live on disk; the signed contract lives in `ledger.jsonl`, which is committed, so a re-attached epic on a fresh clone is still signed without replaying anything.
- **`unknown` is never faked** - a fact that could not be observed is reported as `unknown`, never as a fabricated pass or failure. `cox doctor` exits `3` when any check is `unknown`.
- **The captain merges everything** - Coxswain never merges and never pushes a default branch. Design approval, releases, and every merge are the human's call.
- **Arena design review that can block** - an adversary in a *different* harness reads a blinded pack and raises machine-checked, verified claims against a design before it is signed.
- **Event-driven, zero-token supervision** - a watcher sleeps on the fleet and wakes the leader only when something changes: push harnesses through hooks, pull harnesses through `cox wake wait`.
- **No leader turn ends blind** - before the leader waits, the Stop and session-start hooks check that every led epic with an open story has a live, fresh watcher and restart a dead one, or reopen the turn with the exact `cox watch --replace` repair line (bounded by a per-turn block budget so a broken watcher can never wedge the leader). A watcher whose epic or binary has vanished evicts itself, and an unreachable leader raises a `stuck` wake, a `cox doctor` ISSUE, and - when `policy.alerts.channel` is set - one rate-limited out-of-band notification.
- **Harness-owned idle/busy** - whether a worker is idle or busy is a fact the harness reports through a per-story record, not something a backend guesses from a TUI. Each capability card names the sources it trusts, so a record another harness wrote never classifies a story; a stale incarnation is rejected; and a story that stays busy too long nudges the leader without interrupting it.
- **Workspace-level hooks** - leader hooks belong to the workspace, not to an epic, so one leader terminal drains and rewakes for every active epic under it.
- **Orca backend** - worktrees and terminals come from an Orca backend through a thin adapter, keeping the core independent of any one backend.

Full detail on the boundaries behind these guarantees lives in [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md).

## Quick Start

### Requirements

- A terminal coding agent as the harness: **Claude Code** or **Codex**.
- **Orca**, the backend that owns worktrees and terminals.
- **Git** and the **GitHub CLI**, authenticated through `gh auth login`.

`cox doctor` checks all of this and prints a fix hint for anything missing.

### Install

Authenticate GitHub, clone the repo, and install the `cox` binary:

```sh
gh auth login
git clone https://github.com/nphattai/coxswain && cd coxswain
make install
```

Prefer a prebuilt binary? Download a `coxswain_<version>_<os>_<arch>.tar.gz` archive from [GitHub Releases](https://github.com/nphattai/coxswain/releases), extract it, and put `cox` on your `PATH`; the archive also carries `AGENTS.md`, `templates/`, and `skills/` for a binary-only install. Then verify:

```sh
cox doctor
```

The harness needs no plugin: `cox workspace init` (next step) writes the leader hooks and skills into your workspace for both Claude Code and Codex. The Claude plugin is an optional, skills-only alternative - see [Install](docs/getting-started/install.md).

### Create a workspace

A workspace is a folder beside your code - a sibling `<repo>-ops` folder, never inside the repo. One command creates it:

```sh
cox workspace init --root "$HOME/Work/acme-ops" --repo api=$HOME/Work/acme-api
```

`--repo` is `<alias>=<absolute-path>[:<production-branch>]` and repeatable. For a monorepo, point one repo entry at it (shape B in [Create a workspace](docs/getting-started/workspace.md)). Re-running `init` is idempotent.

### First epic

Create an epic, render its stories, fill one in, and dispatch it:

```sh
cox epic new acme checkout --repo api --root "$HOME/Work/acme-ops"
cox epic stories --epic "$HOME/Work/acme-ops/acme/epics/checkout"
# edit stories/checkout-api.md - goal, scope, owned files, and proof - then:
cox story dispatch checkout-api --epic "$HOME/Work/acme-ops/acme/epics/checkout"
```

The worker runs in its own worktree on `story/<id>`. That is the setup goal reached: a dispatched story.
[First epic](docs/getting-started/first-epic.md) explains each command in full and the leader wake loop, and every command in the fences above is exercised by the onboarding end-to-end test, so you can copy it with confidence.

### Talk to it

You steer the whole crew by chatting with the leader; it escalates only real decisions.

```
> new epic: add checkout to api and web

  designed the epic and rendered two stories. the design is ready for your
  signature, captain.
> sign it and dispatch

  you signed the design; both stories are dispatched to isolated worktrees.
  i'll wake you when there's a decision to make.

  a worker asks: reuse the existing PaymentClient, or add a new one?
> reuse PaymentClient

  done. checkout-api: PR #128, audited, CI green, risk low - ready for your merge.
> merged it

  recorded the merge sha, released the worker, and closed the worktree.
```

### Close an epic

When an epic is done, `cox epic close --epic <dir>` tears it down and only archives its runtime once every step is
verified - it refuses or repairs rather than printing `ok` over unverified state:

```bash
cox epic close --epic <epic-dir>            # dry run: print the plan, change nothing
cox epic close --epic <epic-dir> --yes      # execute (from the leader terminal)
```

- **Landed vs kept.** A worktree is removed only when its work has **landed**: no uncommitted tracked changes, and its
  branch is contained in `origin/<branch>` (fetched first) or a production branch - so a merged branch with no upstream
  is still removed, and a backend's own untracked artifact (Orca's `.orca/` screenshots) never makes it look dirty.
  Anything not landed is **kept** with a reason; `--force` removes it anyway. A branch is never deleted, and a removal
  that did not actually take is caught and the epic is not archived.
- **`--captain`.** Close is refused from a terminal that is not the epic's leader (it prints who owns the epic); the
  captain runs it with `--captain`.

[Close an epic](docs/operations/index.md#close-an-epic) covers landed-vs-kept, verified removal, and re-attaching a
cloned epic in full.

## How It Works

```
        you (the captain)
              |  chat: design, decisions, "merge it"
              v
 +-----------------------------------+
 | leader   (one terminal, workspace)|
 | turns a signed design into        |
 | repo-scoped stories, supervises   |
 +--+----------------+---------------+
    | dispatch       ^ wakes (watcher)
    v                |
 +--------+   +--------+   +--------+
 |worker 1|   |worker 2| . |worker N|   one per repo, Orca worktree
 +---+----+   +---+----+   +---+----+
     |            |            |
     v            v            v
  story/<id> branch  ->  PR  ->  the captain reviews and merges
```

You chat with the leader.
It routes each story to a worker in its own Orca worktree, supervises the fleet with a zero-token event-driven watcher, and brings you PRs to review.
State lives on disk - the event log, steering, checkpoints, and the signed `ledger.jsonl` - so a restart or a move to another machine reconciles from those files and carries on.
Full architecture - the supervision engine, worktree isolation, the adapter boundary, and the wake protocol - is in [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md).

## Built-in skills

Coxswain ships these leader skills. `cox workspace init` writes them into `.agents/skills/` in your workspace, so a Claude Code and a Codex leader read the same set.

| Skill           | What it does                                                                                                  |
| --------------- | ------------------------------------------------------------------------------------------------------------ |
| `cox-epic`      | Start a cross-repo epic: the worktrees on `epic/<slug>`, the epic dir and `DESIGN.md`, then the captain-direct design and per-repo stories. |
| `cox-arena`     | Adversarial design review before signing: a blinded pack, roles on a different harness, machine-checked and verified claims, synthesis, then the signature. |
| `cox-dispatch`  | Dispatch the stories as workers, supervise them through wakes, audit each PR before the captain sees it, release, then done. |
| `cox-ship`      | Open the `[PROD]` PR per repo, its body carrying the before/after go-live preparation.                        |
| `cox-visualize` | Turn a design, plan, or arena synthesis into a visual review page and collect the captain's feedback.        |

The Claude plugin (`claude plugin install`) is an optional way to get the skills, but do not install it in a workspace that already has `.claude/settings.json` hooks from `cox workspace init`, or every hook fires twice. See [Install](docs/getting-started/install.md).

## Documentation

Start here, in order:

- [Install](docs/getting-started/install.md) - Orca, the `cox` binary, your harness, and `cox doctor`.
- [Create a workspace](docs/getting-started/workspace.md) - one `cox workspace init`, and both workspace shapes.
- [First epic](docs/getting-started/first-epic.md) - `epic new`, `epic stories`, `story dispatch`, and the leader wake loop.

Then:

- [Core concepts](docs/getting-started/concepts.md) - captain, leader, worker, epic, story, arena, and the event log.
- [Operating map](docs/operations/index.md) - the full captain and leader routes end to end.
- [Design and sign](docs/arena.md) - running an arena review before a signature.
- [Dispatch, supervise, recover](docs/handoff.md) - the leader/worker channels and acknowledgement rules.
- [Monitor an epic](docs/board.md) - the board view over an epic's stories.
- [Troubleshooting](docs/faq.md) - common failures and the command that fixes each.
- [Worker routing](docs/routing.md) - which harness handles which story.
- [Quota](docs/quota.md) - reading and budgeting harness quota.
- [Policy experiments](docs/lab.md) - trying policy changes safely.
- [Review visual artifacts](docs/review.md) - the visual-review flow.
- [Migrate from v1](docs/migrate.md) - moving off the frozen v1 kit.

Reference:

- [CLI map](docs/reference/cli.md) - every command family by task.
- [`workspace.json`](docs/reference/workspace-json.md) - the registry schema.
- [`policy.json`](docs/reference/policy-json.md) - the behavioral defaults schema.
- [Story frontmatter](docs/reference/story-frontmatter.md) - every field that steers dispatch.
- [Configuration](docs/reference/configuration.md) - environment and file configuration.

Understand and extend:

- [Architecture](docs/ARCHITECTURE.md) - the system boundaries and why they hold.
- [Adapter model](docs/adapters/index.md) - backends, harnesses, forges, and services.
- [Protocol model](docs/protocol/index.md) - the on-disk event, fleet, inbox, checkpoint, wake, and quota formats.
- [Contributing](docs/contributing.md) - the dev and test workflow.
- [Author an adapter](docs/contributing/adapters.md) - integrating a backend, harness, or service.
- [Architecture decisions](docs/decisions/index.md) - the decision record.
- [Verification evidence](docs/evidence/index.md) - the compatibility and migration evidence.

## Contributing

Contributions are welcome - see [Contributing](docs/contributing.md) for the workflow, repo conventions, and how to run the tests.

## License

MIT - see [LICENSE](LICENSE).
