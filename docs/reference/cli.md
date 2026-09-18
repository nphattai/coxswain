<span id="cli-reference" aria-hidden="true"></span>

# CLI map

Use this page to find the command family for a task. `cox --help` is the top-level index. Each command's usage output
and implementation file under `cmd/cox/` own its exact flags, validation, and exit behavior.

## Setup and diagnosis

| Task | Command family | Owner |
|---|---|---|
| Initialize workspace files | `cox workspace` | `cmd/cox/workspace.go` |
| Check installation, drift, and epic health | `cox doctor` | `cmd/cox/doctor.go`, `internal/doctor/` |
| Migrate a v1 epic | `cox migrate` | `cmd/cox/migrate.go`, `internal/migrate/` |

## Design and decompose

| Task | Command family | Owner |
|---|---|---|
| Create, sign, or close an epic | `cox epic` | `cmd/cox/epic.go`, `internal/epic/` |
| Run adversarial design review | `cox epic arena`, `cox arena` | `cmd/cox/arena.go`, `internal/arena/` |
| Render or compare plan artifacts | `cox plan`, `cox artifact` | `cmd/cox/plan.go`, `cmd/cox/artifact.go` |

## Dispatch and supervise

| Task | Command family | Owner |
|---|---|---|
| Dispatch, park, resume, or terminate a story | `cox story` | `cmd/cox/story.go` |
| Send durable guidance | `cox steer` | `cmd/cox/steer.go`, `internal/protocol/inbox/` |
| Apply a bounded control verb | `cox control` | `cmd/cox/control.go`, `internal/protocol/control/` |
| Report progress or completion | `cox story report`, `cox status` | `cmd/cox/report.go`, `cmd/cox/status.go` |
| Ask, wait, and reply | `cox question`, `cox reply` | `cmd/cox/question.go`, `cmd/cox/reply.go` |
| Capture or inject resume context | `cox checkpoint` | `cmd/cox/checkpoint.go`, `internal/protocol/checkpoint/` |
| Drain, acknowledge, or wait for wakes | `cox wake`, `cox watch` | `cmd/cox/wake.go`, `cmd/cox/watch.go` |

## Observe and recover

| Task | Command family | Owner |
|---|---|---|
| Resolve current story or fleet state | `cox state` | `cmd/cox/state.go`, `internal/state/resolve.go` |
| Recover an unconfirmed transition | `cox reconcile` | `cmd/cox/reconcile.go`, `internal/reconcile/` |
| Inspect captain dashboard | `cox board` | `cmd/cox/board.go` |
| Inspect scorecard or baseline evidence | `cox scorecard`, `cox baseline` | `cmd/cox/scorecard.go`, `cmd/cox/baseline.go` |
| Inspect quota or route a story | `cox quota`, `cox route` | `cmd/cox/quota.go`, `cmd/cox/route.go` |
| Run a policy experiment | `cox lab` | `cmd/cox/lab.go`, `internal/lab/` |

## Audit and release

| Task | Command family | Owner |
|---|---|---|
| Audit a pull request | `cox audit` | `cmd/cox/audit.go`, `internal/verdict/` |
| Gather release facts | `cox ship facts` | `cmd/cox/ship.go`, `internal/verdict/ship.go` |
| Open or poll visual review | `cox review` | `cmd/cox/review.go`, `internal/adapter/review/` |
| Manage story-owned services | `cox env` | `cmd/cox/env.go`, `internal/env/` |

## Authority notes

- Read-only fact commands may report `unknown`; they do not manufacture a pass.
- Commands do not grant merge or release authority. The captain keeps those decisions.
- Outward-facing review sharing and destructive cleanup require explicit confirmation at the owning command.
- Do not hand-edit `.cox` records to imitate a command.

Start with [Operations](../operations/index.md) for task sequencing or [Handoff](../handoff.md) for channel semantics.
