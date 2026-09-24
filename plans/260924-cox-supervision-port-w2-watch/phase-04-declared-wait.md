---
phase: 4
title: "Declared wait and wait cadence"
status: pending
priority: P1
effort: ""
dependencies: [3]
---

# Phase 4: Declared wait and wait cadence

## Overview
A worker that declares `paused: ... [until <UTC ISO>]` or `captain-held: ...` is absorbed instead of wedge-escalated,
re-surfaced once per pause cadence (live endpoint surfaces on first sight, then the cadence; exited endpoint takes the
bounded cadence), rechecked when `until` passes, bounded when `until` is absurd (wrong year), and a captain hold is
never rechecked while the away record exists. The recheck names its evidence ("declared wait", "awaiting the
captain", "awaiting external").

## Firstmate reads
`bin/fm-watch.sh` `handle_paused_stale`, `pause_state_class`, `resurface_absorbed`, `wait_record`,
`wedge_wait_evidence`, `wedge_defer_wait`, `task_captain_call_open`, `stale_wait_*`, `captain_call_stale_bound`,
`captain_held_silenced`, `afk_record_present`; `bin/fm-classify-lib.sh` `status_is_paused`, `status_paused_until`,
`status_is_terminal`; `docs/captain-hold-lifecycle.md`. Cases: the declared-wait and wait-cadence rows of the triage
report (`:324,455,1834,2289,2358,2481,2582,2669,2814,2899,3000,3164,3231,3773,3819,3893,3927,3961,4041,4098,4128,
4596,5920,6073,6087,6102`).

## Design
`internal/watch/wait.go`: verb parser (firstmate grammar), per-story pause throttle file, until-time handling. Cox's
"away record" = the nearest cox observable, named in the PR body (proposal: `<epic>/.cox/afk`, absent today, so the
case uses the file). Parked-gate cases (`wedge_threshold_*parked_gate*`) use the run-step observable from phase 2.

## Success
Watch cases of both groups green `-count=3 -race`; the two corr-token cases in `internal/wake` are w2-wake's.
