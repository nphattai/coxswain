<!-- Parent: ../AGENTS.md -->
# INTERNAL - THE ENGINE

## OVERVIEW

Every package here is listed with its purpose in [docs/codemap.md](../docs/codemap.md). The spine: `state` is the
source of truth (append-only event log + pure fold), `watch` turns backend mail and file channels into `wake` records
for the leader, `epic` and `workspace` own the on-disk layout, and `adapter/*` are the only packages that talk to the
outside world.

## WHERE TO LOOK

| Task | Location | Notes |
|---|---|---|
| Lifecycle facts, snapshots | `state` | append events; never edit the log |
| Wake classification and queue | `watch`, `wake` | `wake/port_*_test.go` are firstmate ports |
| Supervision needs (stuck, stale, idle) | `supervision`, `watch` | |
| Epic layout, stories, close | `epic` | |
| `cox/workspace.json`, `cox/policy.json` | `workspace` | |
| Session start digest | `bearings` | |
| Doctor inventory | `doctor` | |
| Crash recovery of half-applied side effects | `reconcile` | `pending_external` events |
| Worker identity binding | `identity` | by attempt id + canonical path, never by name |
| Story env files and resources | `env` | atomic publish, ownership in `.cox/resources.json` |
| External commands | `boundexec` | process-group kill at the bound |
| Arena | `arena/*` | `pack`, `roles`, `cite`, `check`, `verify`, `synth`, `report` |
| Harness choice | `routing`, `quota` | |
| Metrics and experiments | `scorecard`, `lab`, `baseline` | |

## CONVENTIONS

- Depend on `adapter/*` interfaces, never a concrete backend (`tests/integration/import_boundary_test.go`).
- Files other processes read are written tmp + rename.
- Results that can be unknown are three-state (`verdict`): pass, fail, unknown.
- F-numbered comments (F01, F02, ...) cite the v1 failures each guard prevents; keep the guard and the comment together.

## ANTI-PATTERNS

- Inferring worker state from a terminal UI: busy/idle is harness-reported (`protocol/busy`, ADR 0016).
- A call to an external tool that can hang (backend, forge, harness CLI) without a bound: use `boundexec`.
