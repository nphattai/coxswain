# Decisions and evidence

Evergreen documentation explains the current contract. This section holds dated observations that may change when an
external tool, rollout, or environment changes.

## Compatibility evidence

- [Orca CLI 1.4.197 terminal-plane observations](compatibility/orca-1.4.197.md)
- [Codex CLI 0.154 hooks and sandbox observations](compatibility/codex-cli-0.154.md)
- [Claude CLI 2.1.272 launch and arena observations](compatibility/claude-cli-2.1.272.md)
- [herdr 0.8.2 command-surface observations](compatibility/herdr-0.8.2.md)
- [Optional adapter observations](compatibility/optional-adapters.md)

## Migration evidence

- [Named v1 rollout record](migrations/v1-rollout.md)

These records are evidence, not current API guarantees. For current behavior, follow their executable evidence paths
and the relevant adapter or migration contract.

## Durable decisions

[Architecture decisions](../decisions/index.md) hold rationale that remains authoritative until superseded. Evidence
may trigger review of a decision, but it does not silently replace one.
