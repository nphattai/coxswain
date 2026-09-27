# Coxswain docs

Start here by task. Each page owns one topic; follow its links for depth. Contributors start at
[CONTRIBUTING.md](../CONTRIBUTING.md).

## Start here by task

| I want to | Read |
|---|---|
| Install cox and run a first epic | [Install](getting-started/install.md), [Create a workspace](getting-started/workspace.md), [First epic](getting-started/first-epic.md), [Quickstart](QUICKSTART.md) |
| Understand the model (leader, worker, story, wake) | [Core concepts](getting-started/concepts.md), [Architecture](ARCHITECTURE.md) |
| Fix a problem | [FAQ & troubleshooting](faq.md), [Operations](operations/index.md) |
| Look up a command or a config key | [CLI map](reference/cli.md), [Configuration authority](reference/configuration.md), [`policy.json`](reference/policy-json.md), [`workspace.json`](reference/workspace-json.md), [Story frontmatter](reference/story-frontmatter.md) |
| Find the code for something | [Code map](codemap.md) (generated), then the "Where to look" table in [CONTRIBUTING.md](../CONTRIBUTING.md#where-to-look) |
| Know why it was built this way | [Architecture decisions](decisions/index.md), [Evidence](evidence/index.md) |

## Features

| Page | Covers |
|---|---|
| [Arena](arena.md) | adversarial design review before signing a hard epic |
| [Handoff](handoff.md) | how a worker's context survives a restart or compaction |
| [Routing](routing.md) | choosing a harness and model per story |
| [Quota](quota.md) | observe-only usage limits routing and the board read |
| [Board](board.md) | the read-only captain board |
| [Visual review](review.md) | rendered review pages and captain feedback |
| [Lab](lab.md) | policy experiments scored against a metric |

## Contracts

| Page | Covers |
|---|---|
| [Protocol model](protocol/index.md) | the versioned records between leader, worker, and watcher |
| [Adapter model](adapters/index.md) | backends, harnesses, forge, review, quota, and service adapters |
| [Author an adapter](contributing/adapters.md) | adding or changing an external boundary |

## Design record

| Folder | Holds |
|---|---|
| `features/` | shipped features, one `YYYY-MM-DD-<slug>.md` per epic |
| `bugs/` | shipped fixes, e.g. [doctor-shape-c](bugs/2026-09-27-doctor-shape-c.md) |
| `backlog/` | designs published before they are built |
| `archive/` | superseded or dropped designs |
| [decisions/](decisions/index.md) | ADRs: lasting architectural choices |

The lifecycle and the template are in [CONTRIBUTING.md](../CONTRIBUTING.md#where-docs-go). Known code-vs-doc drift is
logged in [_stale-report.md](_stale-report.md).

## Historical

[Migrate from v1](migrate.md) covers the frozen v1 bash tool (`bin/`); read it only when moving an old workspace.
