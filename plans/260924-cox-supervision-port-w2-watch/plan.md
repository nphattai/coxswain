---
title: "cox-supervision-port-w2-watch - port firstmate's watcher triage into internal/watch"
status: pending
branch: story/cox-supervision-port-w2-watch
base: epic/cox-supervision-port @ 22dbf3c
---

# Plan: w2-watch (wave 2 implementation)

Spec = the red port cases. Contract = DESIGN "Captain ruling 2026-09-24", "Wave 2 - implementation contract", "Wave 2
stories". Firstmate pinned 1e0e773, read only. Each phase reads the firstmate script/doc its cases cite
(`bin/fm-watch.sh`, `bin/fm-crew-state.sh`, `bin/fm-classify-lib.sh`, `bin/fm-busy-lib.sh`,
`bin/fm-pending-reply-lib.sh`, `docs/wedge-alarm.md`, `docs/watcher-continuity.md`,
`.agents/skills/stuck-crewmate-recovery/SKILL.md`) before code, and ports thresholds and texts verbatim.

## Baseline (HEAD 22dbf3c, `go test -tags port -json ./internal/watch/`)

98 red / 55 green leaves. Of the 98: 4 lifecycle (R16 x2 belong to w2-hooks, R17, R18), 94 triage. Of the 94:
decision fold 10 in this package (stay red, wave 2b), status classification ~8 whose only gap is `wake.Classify`
(w2-wake's package, see Q3), the rest (~76) are mine.

## Phases (one phase = one commit `Phase: N/7`, pushed at the end of each)

| # | Phase | Red groups turned green | Status |
|---|---|---|---|
| 1 | [Watcher lifecycle and robustness](phase-01-lifecycle.md) | R17 self-evict, R18 beacon temp+rename, R12/R13/R19 watch-side API, unreadable status source (2), markTick/mailPass absorb (1) | pending |
| 2 | [Crew state and turn-end triage](phase-02-crew-state-turnend.md) | turn-end triage (7), run-step authority (8, CI-running observable), stale escalation (8), recovery triage (1) | pending |
| 3 | [Wedge detector and gone endpoint](phase-03-wedge.md) | wedge detector (17), busy-turn bound (2), gone endpoint (4) | pending |
| 4 | [Declared wait and cadence](phase-04-declared-wait.md) | declared wait (watch cases of 14), wait cadence (7) | pending |
| 5 | [Heartbeat backstop, override, runaway](phase-05-backstop-runaway.md) | heartbeat backstop (4), captain-relevance override (watch case), B-53 runaway (2: triage + busy-wake `watch.runaway-consumed-reply`), stale names unread steer (1) | pending |
| 6 | [Leader alarm](phase-06-leader-alarm.md) | `watch.leader-alarm-default`, `watch.leader-alarm`, `watch.leader-alarm-channels` | pending |
| 7 | [Kill test, untag, Makefile, supersession notes](phase-07-kill-test-close.md) | epic AC 3; `port_lifecycle_test.go` untagged when zero red; `make test` runs `test-port` | pending |

Order: 1 is plumbing every later phase uses (per-story pass isolation, atomic watch files). 2 defines "provably
working" which 3-5 consume. 6 is independent. 7 last.

## Design decisions (defaults taken; the PR body carries them)

- **One classifier, one place.** Port `crew_is_provably_working` / `crew_absorb_class` as `crewState(story)` in a new
  `internal/watch/crew.go`, returning firstmate's classes (busy / idle / dead / unknown) with its source
  (`busy-record`, `run-step`, `composer`, `probe`). Every pass reads it; no pass re-derives liveness.
- **Run-step = CI running at the live head** (captain ruling): new optional `Watcher.Forge forge.Forge`; the story PR
  URL comes from the latest `pr` evidence in the event log; three-state result (running / not running / unknown).
  Nil forge = unknown, which never absorbs (firstmate: absorb only on positive evidence).
- **Worktree write probe** (wedge write deferral): newest mtime under the story worktree, bounded by a wall-clock
  budget (firstmate `worktree_write_probe_is_wall_clock_bounded`), `.git/` and the epic's `.cox/` excluded.
- **Per-story watch state** stays under `<epic>/.cox/watch/<sub>/<story>` like today (stale timer, escalation count,
  pause throttle, gone report, write-deferral chain), written temp+rename.
- **Declared-wait verbs** (`paused:`, `captain-held:`, `until <UTC ISO 8601>`) are parsed in `internal/watch` with
  firstmate's grammar from `fm-classify-lib.sh` (`status_is_paused`, `status_paused_until`); the watcher needs the
  verb, not a wake kind, so `internal/wake` is not touched.
- **Constants** (firstmate defaults unless a cox name exists): `StaleMin` keeps its cox name, default becomes
  firstmate's 240s `STALE_ESCALATE_SECS`; `BusyTurnMax` default 3600s (`BUSY_TURN_MAX_SECS`); pause resurface cadence
  `FM_PAUSE_RESURFACE_SECS_DEFAULT`; turn-end churn absorb 900s; heartbeat 600s with 7200s backoff cap; alarm
  timeout 10s. Exact values re-read from firstmate in each phase.
- **Superseded cox readings** get a one-line note each (ADR 0016 busy record is not unconditional proof past the
  turn bound; "unknown stays unknown" now escalates after a bound; stalePass alive-silence; alerts default off).

## Questions for the leader (asked with the plan; assumptions I proceed with if unanswered)

- **Q1 (Files touched).** `watch.leader-alarm-default` asserts `(&workspace.Policy{}).AlertsChannel() != "off"`;
  that is `internal/workspace` (no wave 2 story owns the policy file). Assumption: I make that one-line default change
  (`"" -> "auto"`) and list it under Shared files.
- **Q2 (R12/R13/R19 live in `cmd/cox/port_turnend_test.go`, owned by w2-hooks).** Assumption: I ship the watch-side
  API only - `watch.ProcIdentity(pid)` (start time via `ps -o lstart=` under `LC_ALL=C`, locale invariant),
  `watch.Healthy(epicDir, now)` (the cheap liveness/tick read), `watch.ExitSignals` (TERM, INT, HUP) - each with a
  unit test in `internal/watch`; w2-hooks wires them into `cmd/cox` and turns those cases green. I do not edit
  `cmd/cox`. (The seams paragraph says w2-watch exports nothing new for w2-hooks; the story scope lists these three,
  so I read the story as the narrower rule.)
- **Q3 (wake.Classify dependency).** About 8 watch cases (`status_span_*`, `actionable_signal_*`,
  `classifier_primitives`, `heartbeat_backstop_surfaces_a_masked_status`, `needs_decision_*`) fail only because
  `wake.Classify` misses `needs-decision` (w2-wake's package). Assumption: I do not touch `internal/wake`; those cases
  go green when I rebase onto the w2-wake merge, and the PR body lists which ones wait on it.
- **Q4 (Makefile) - leader ruling q001:** `make test` runs `go test ./...` then `test-port`; `test-port` prints the red
  count per package and never fails the build. No ceiling in the Makefile; the ratchet is enforced at audit from the
  before/after counts in the PR body.

Leader answer q001 (2026-09-24): approved; Q1 yes (one-line `AlertsChannel` default in `internal/workspace`, Shared
files); Q2 yes (watch API only, w2-hooks wires it); Q3 yes (leave the Classify-dependent cases red, list them by name).
