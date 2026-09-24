---
phase: 3
title: "Wedge detector, busy-turn bound, gone endpoint"
status: pending
priority: P1
effort: ""
dependencies: [2]
---

# Phase 3: Wedge detector, busy-turn bound, gone endpoint

## Overview
Port the wedge ladder: a provably-working crew silent past `StaleMin` escalates as "possible wedge, escalation N"
paced by `StaleMin`, marks demand-deep-inspection after the threshold, resets on activity, defers while the worktree
is written (bounded cadence), and a busy record past `BusyTurnMax` with no turn-end/native progress enters the same
ladder (never an automatic interrupt). A proven-gone endpoint is reported once and re-armed when it comes back.

## Firstmate reads
`bin/fm-watch.sh` `wedge_defer_writing`, `wedge_timer_check`, `wedge_dead_record`, `busy_turn_over_age`,
`busy_turn_bound_check`, `clear_write_tracking`, `clear_stale_hash_tracking`; `bin/fm-crew-state.sh`
`crew_worktree_written_since`; cases `tests/fm-watch-triage.test.sh:515,580,642,907,3400,3444,3478,3525,3597,
4207,4262,4380,4425,4467,4501,4537,4991,5059,5104,5154,5223`.

## Design
`internal/watch/wedge.go`: per-story `since`, `escalations`, `write-defer`, `gone` files under `.cox/watch/`; texts
verbatim from fm-watch.sh. `busyTurnMaxPass` becomes the phase-B entry into the ladder (routine nudge removed where
cases say escalate). Gone = `backend.Gone` probe OR busy record older than its endpoint (successor display), keyed by
session identity so a relaunch re-arms.

## Success
The three groups green `-count=3 -race`.
