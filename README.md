# coxswain

Coxswain is the control plane for multi-agent epics. A human captain works with one leader agent. The leader turns an
approved design into repo-scoped stories, dispatches workers into isolated git worktrees, and supervises them through
durable state. The captain reviews and merges every change.

Coxswain is designed for work that crosses repositories or needs durable agent supervision. It is not a wrapper for a
single prompt or a replacement for human merge authority.

## Start here

1. [Install the binary and harness integration](docs/getting-started/install.md).
2. [Run a first epic](docs/QUICKSTART.md).
3. [Learn the operating model](docs/getting-started/concepts.md).

The published manual is at <https://nphattai.github.io/coxswain/>.

## Choose your route

| You are | Go to |
|---|---|
| Captain deciding scope, design, and merge readiness | [Operations](docs/operations/index.md) |
| Leader supervising workers and recovering failures | [Handoff](docs/handoff.md) |
| Contributor changing Coxswain | [Contributing](docs/contributing.md) |
| Adapter author integrating a backend, harness, or service | [Author an adapter](docs/contributing/adapters.md) |
| AI collaborator looking for executable authority | [Architecture](docs/ARCHITECTURE.md), then the source and tests it links |

## The contract in one minute

- State survives sessions because the event log and handoff records live on disk.
- A failed observation becomes `unknown`, never a fabricated success or failure.
- Every worker gets an isolated worktree. Coxswain never deletes a git branch.
- External effects are confirmed before ownership is cleared.
- The captain owns design approval, release decisions, and every merge.

See [Architecture](docs/ARCHITECTURE.md) for the reasons behind these constraints and [Architecture decisions](docs/decisions/index.md)
for the decision record. Mutable command families are indexed by `cox --help`, while exact flags and configuration
are owned by `cmd/cox/`, `templates/workspace.json`, and `templates/policy.json`; the [CLI map](docs/reference/cli.md) and
[configuration guide](docs/reference/configuration.md) point to them by task.
