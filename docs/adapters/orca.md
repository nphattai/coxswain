# Orca backend adapter

Orca is the default backend. The current default is the terminal plane: Orca provides worktrees and terminals while
Coxswain owns semantic handoff and story state. A compatibility orchestration plane remains selectable by policy.

## Stable contract

| Capability | Terminal plane | Orchestration plane |
|---|---|---|
| Worktrees | Orca create/remove plus Coxswain verification | Same |
| Spawn | Terminal creation and typed harness launch | Task and worker dispatch |
| Liveness | Terminal and structured agent observations | Worker dispatch observation |
| Send | Doorbell to a terminal | Doorbell or orchestration message |
| Stop | Terminal close, then confirmed probe | Worker stop confirmation |
| Mailbox | Reduced; Coxswain records own handoff | Run-scoped delivery with explicit acknowledgement |
| Completion | `cox story report` | Orchestration completion compatibility |

Both planes preserve the same core guarantees:

- Worktree identity and branch are reverified. Failure never falls back to a shared checkout.
- Removal detaches first. If detach fails, removal is skipped because preserving the branch wins.
- Missing, unreadable, or unrecognized liveness is unknown.
- A doorbell is not the durable steer channel.
- Stop clears ownership only after confirmation.

`backend.orca.plane` selects the plane. The code default is owned by `internal/workspace/policy.go`, not this page.
Current behavior lives in `internal/adapter/backend/orca/`, with contract coverage in `orca_test.go` and
`tests/integration/terminal_plane_rules_test.go`.

## Known ceilings

- Structured agent state depends on Orca recognizing the harness. An absent entry remains unknown.
- The compatibility orchestration plane is constrained by Orca run binding and completion semantics.
- Branches left behind by safe worktree recovery are not automatically deleted. Manual cleanup remains a captain or
  operator decision.

Version-specific command shapes and live discoveries are in
[Orca CLI 1.4.197 compatibility evidence](../evidence/compatibility/orca-1.4.197.md).
