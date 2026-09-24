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
| busy_record | true |
| busy_sources | claude-hook, dispatch, interrupt, recovery |
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
- **Harness-owned busy state.** Idle/busy is a fact the harness reports, not something a backend infers from a TUI
  (DESIGN wave-2 item 6). Dispatch/resume/relaunch arm a per-story record at `<epic>/.cox/sessions/<story>.busy.json` and
  thread its incarnation gen to the worker as `COX_BUSY_GEN`, and write worker hooks into the worktree's
  `.claude/settings.local.json` (Claude Code merges local settings and honours hooks there; the `.local` file is the
  per-checkout, not-committed settings slot, so cox's runtime hooks never touch a repo's tracked `.claude/settings.json`,
  and cox excludes the file via the worktree's `info/exclude` when it is not already gitignored, keeping the story
  worktree clean): `UserPromptSubmit` Applies `busy`; `Stop`, `StopFailure` (an API-error turn end that fires no `Stop`)
  and `SessionEnd` Apply `idle` (superseded 2026-09-24, firstmate fm-busy-adapter-wiring: `SessionEnd` used to retire the record)
  (`${COX_BIN:-cox} busy apply|retire`, `source=claude-hook`, `--gen "$COX_BUSY_GEN"`, each ending `|| true` so a refused
  Apply never breaks the turn). The card's `busy_sources` is the trust table: an Apply from a source it does not list is
  rejected, and a record written by an untrusted source reads as `unknown`, so a record the harness did not write never
  classifies a story. A stale gen (a hook that outlived its incarnation after a re-arm) is rejected, so a late event
  cannot clobber a newer incarnation. A relaunch or harness reroute retires the prior incarnation's busy hooks after the
  prior agent settled and before the replacement is armed (firstmate fm-control-relaunch); a failed removal refuses the
  relaunch. A backend consults this record first and falls back to its own signal only on
  `unknown`/absent.

The executable owners are `internal/adapter/harness/claude/`, `cmd/cox/hook.go`, `internal/adapter/backend/launch.go`,
and their tests. Dated CLI observations are in [Claude CLI 2.1.272 compatibility evidence](../evidence/compatibility/claude-cli-2.1.272.md).

## Next

- [Handoff](../handoff.md) for push wake and checkpoint semantics.
- [Configuration](../reference/configuration.md) for launch-policy authority.
