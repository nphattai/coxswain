# Coxswain

**Multi-agent epic orchestration for Claude Code and Codex.** `cox` is a single Go binary plus a plugin that dispatches
stories to isolated worktrees, runs adversarial arena design review, and tracks the whole story lifecycle on a durable
event log - so a leader agent and its worker agents can carry a cross-repo epic from design to merged PR without losing
state.

## Why

- **Leader/worker, not one giant prompt.** A leader designs and reviews; workers implement in isolated worktrees, one per repo.
- **Durable, harness-agnostic.** State lives in an append-only event log; the same skills drive Claude Code and Codex.
- **Design review that bites.** Arena runs an adversary in a different harness with machine-checked, verified claims.
- **Recoverable.** Checkpoints + a wake protocol mean a leader or worker can be parked, resumed, or restarted.

## Start here

- [Install](getting-started/install.md) - the binary and the plugin.
- [Quickstart](QUICKSTART.md) - your first epic, end to end.
- [Core concepts](getting-started/concepts.md) - leader, worker, epic, story, arena, wake.
- [CLI reference](reference/cli.md) and [Configuration](reference/configuration.md).

Run `cox doctor` after installing to confirm the binary and list every coxswain installation.
