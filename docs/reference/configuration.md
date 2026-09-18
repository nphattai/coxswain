# Configuration

Two files under `cox/` configure a workspace.

## `workspace.json`

The registry of projects and repos.

| Field | Meaning |
|---|---|
| `projects` | Named projects grouping repos. |
| `repos[]` | Each repo: `alias`, local `path`, `production` branch, optional `staging` branch. |
| `services[]` | Long-running services an epic backend may start. |
| `hosts[]` | Named hosts a worker can run on. |

Generate it with `cox workspace init` on each machine (it holds absolute paths, so it is per-machine and not shared).

## `policy.json`

Harness, model, and workflow defaults (optionally overridden per project at `<project>/cox/policy.json`).

| Field | Meaning |
|---|---|
| `harness.leader` / `harness.worker` | Allowed harnesses and the default (`claude`, `codex`, ...). |
| `harness.worker.models` | Per-harness worker model, e.g. `claude` -> a Claude model, `codex` -> a Codex model. |
| `harness.launch` | Launch flags per harness (approval/sandbox for Codex, permission mode for Claude). |
| `harness.arena` | Adversary and reviewer selection rules (adversary must differ from the leader). |
| `quota` | Quota thresholds and the `quota-axi` adapter opt-in. |

`cox doctor` reports policy drift when a workspace policy predates newer keys.
