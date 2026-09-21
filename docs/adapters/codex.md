<span id="harness-adapter-codex" aria-hidden="true"></span>

# Codex harness adapter

Codex supports leader and worker roles. The base capability card uses pull wake delivery and manual checkpoints. A
workspace may install project hooks to enhance leader delivery, but the base contract remains usable without them.

## Capability card

This table is checked against `internal/adapter/harness/codex.Harness.Card()` by
`tests/integration/harness_card_docs_test.go`.

| Field | Value |
|---|---|
| name | codex |
| roles | leader, worker |
| wake | pull |
| checkpoint | manual |
| doorbell | true |
| interrupt | true |
| telemetry | false |
| sandbox | true |
| unsandboxed_ack | false |
| instructions | AGENTS.md + markdown skills |

## Support contract

- Codex reads `AGENTS.md` and Markdown skills directly.
- Without trusted project hooks, a leader drains wakes at turn start and ends idle work with `cox wake wait`. The
  backend doorbell is a safety net, not durable delivery.
- Workers write checkpoints at phase boundaries. Relaunch injects the saved checkpoint through the brief.
- Session telemetry is unknown because the adapter has no authoritative session log.
- Worker launch flags, model selection, network access, and additional writable roots are composed by
  `internal/adapter/backend/launch.go`. These permissions are captain-owned policy.
- Project hooks are installed only in the project layer. Coxswain does not modify the user's Codex configuration or
  trust decision. `cox workspace init` (or `cox workspace hooks --harness codex`) writes the workspace `.codex/hooks.json`
  with the four leader hooks from `hooks/hooks.json`; each hook resolves the workspace from the cwd and acts on every
  epic with a live watcher, so leader hooks belong to the workspace, not an epic.

The executable owners are `internal/adapter/harness/codex/`, `internal/adapter/backend/launch.go`,
`cmd/cox/workspace.go`, `cmd/cox/hook.go`, and their tests. Dated hook and sandbox findings are in
[Codex CLI 0.154 compatibility evidence](../evidence/compatibility/codex-cli-0.154.md).

## Next

- [Handoff](../handoff.md) for pull wake and checkpoint semantics.
- [Configuration](../reference/configuration.md) for launch-policy authority.
