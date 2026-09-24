---
phase: 2
title: "Crew state and turn-end triage"
status: pending
priority: P1
effort: ""
dependencies: [1]
---

# Phase 2: Crew state and turn-end triage

## Overview
Port `crew_is_provably_working` / `crew_absorb_class` and the turn-end triage: a worker whose turn ended without a
report surfaces urgently with no steer needed (B-50); a stale busy record on a dead/idle worker is not proof (B-51);
a silent alive worker past `StaleMin` escalates; an active CI run (run-step observable) is positive evidence.

## Firstmate reads
`bin/fm-crew-state.sh` (whole: sources, `source: run-step`, absorb classes), `bin/fm-watch.sh` `scan_signals`,
`signal_turnend_panes_churned`, `surface_nonterminal_stale`, `age_of`, `TURNEND_CHURN_ABSORB_SECS`,
`docs/supervision-protocols/*.md`, `stuck-crewmate-recovery/SKILL.md:16`. Cases at
`tests/fm-watch-triage.test.sh:423,488,683,729,774,861,979,1007,1153,1230,1307,1429,1513,2104,2137,2192,2248,4936`.

## Design
- `crew.go`: `crewState(story) (class, source string)`; classes `working` (busy record busy and inside the turn
  bound, or CI running at the live PR head), `stopped` (idle record, `Settled` probe), `gone` (probe `Gone`),
  `unknown`. Order and precedence as `fm-crew-state.sh`.
- Turn-end pass replaces `idleNoDonePass`'s steer requirement: on a busy record's `idle` transition (turn ended), if no
  worker report arrived since the turn began and the crew is not provably working -> urgent `idle_no_done` with
  firstmate's text. Churn deferral bounded at 900s; an invalid/future deadline surfaces (fixes defect 9).
- `stalePass`: a stale timer per working story is created on first sight even with no heartbeat (defect 8, timer
  repair); an alive worker silent past `StaleMin` escalates (`stale` wake, urgent); a completed/landed story is skipped
  (defect 6).
- `Watcher.Forge` + story PR from `pr` evidence; `ciRunning(story)` three-state.
- Absorb a no-verb `working:` status only when provably working (`provably_working_signal_absorbed`,
  `beacon_stays_fresh_while_absorbing`).

## Files
Create `internal/watch/crew.go`, `internal/watch/crew_test.go`; modify `watch.go`; un-stub the run-step cases in
`port_triage_test.go` with a fake forge.

## Success
The four groups green `-count=3 -race`; watch_test.go still green (existing tests that pinned the old steer-required
behavior are updated only where a firstmate case contradicts them, each named in the PR body).

## Risk
Existing `idleNoDonePass` unit tests encode the steer requirement; a case overrides them (captain ruling). Signal:
watch_test.go failures limited to those tests.
