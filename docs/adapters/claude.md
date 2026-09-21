<span id="harness-adapter-claude" aria-hidden="true"></span>

# Claude harness adapter

Claude supports leader and worker roles. Hooks provide push wake delivery and automatic checkpoint capture. Session-log
telemetry is optional evidence: an absent or unreadable log remains unknown.

## Capability card

This table is checked against `internal/adapter/harness/claude.Harness.Card()` by
`tests/integration/harness_card_docs_test.go`.

| Field | Value |
|---|---|
| name | claude |
| roles | leader, worker |
| wake | push |
| checkpoint | auto |
| doorbell | true |
| interrupt | true |
| telemetry | true |
| sandbox | false |
| unsandboxed_ack | true |
| instructions | plugin skills + AGENTS.md |

## Support contract

- Claude reads the harness-neutral job description through `CLAUDE.md`; plugin skills carry the role workflows.
- Leader hooks belong to the workspace, not an epic: `cox workspace init` (or `cox workspace hooks --harness claude`)
  writes `.claude/settings.json`, and each hook walks up from the cwd to the workspace and acts on every epic with a live
  watcher - draining wakes and rearming an idle leader, and refreshing or injecting the leader checkpoint around
  compaction or resume.
- Telemetry reads the newest matching session log. No usable log is unknown, never zero usage.
- Worker launch permissions and models come from resolved policy. Non-interactive permission modes transfer risk to the
  captain and can be tightened or removed in policy.
- Arena launch permissions are separate from worker permissions so a review role never inherits a worker's broad
  autonomy by accident.

The executable owners are `internal/adapter/harness/claude/`, `cmd/cox/hook.go`, `internal/adapter/backend/launch.go`,
and their tests. Dated CLI observations are in [Claude CLI 2.1.272 compatibility evidence](../evidence/compatibility/claude-cli-2.1.272.md).

## Next

- [Handoff](../handoff.md) for push wake and checkpoint semantics.
- [Configuration](../reference/configuration.md) for launch-policy authority.
