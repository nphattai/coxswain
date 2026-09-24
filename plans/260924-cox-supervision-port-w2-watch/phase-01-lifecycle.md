---
phase: 1
title: "Watcher lifecycle and robustness"
status: pending
priority: P1
effort: ""
dependencies: []
---

# Phase 1: Watcher lifecycle and robustness

## Overview
The watcher keeps running and stays single: a bad status source never aborts a pass, a takeover evicts it, its beacon
cannot be redirected, and the leader-side guard gets cheap identity/liveness reads.

## Firstmate reads
`docs/watcher-continuity.md` (whole), `tests/fm-watcher-lock.test.sh:560,1005,1071`, `tests/fm-watch-arm.test.sh:860`,
`bin/fm-watch.sh` `watcher_cleanup`, `watcher_stop_signals`, beacon write; `tests/fm-watch-triage.test.sh:2021,2067,5789`.

## Cases
- `fm-watcher-lock/watcher_self_evicts_on_lock_takeover` (R17): `evictReason` also evicts when `watch.pid` names another
  live process (identity-checked), logged like the other evictions.
- `fm-watch-arm/downtime_marker_does_not_follow_symlink` (R18): `markTick` and every watch-state write go through one
  `writeAtomic` (temp in the same dir + rename).
- R12/R13/R19 watch side: `ProcIdentity(pid)`, `Healthy(epicDir, now)`, `ExitSignals` + unit tests (Q2).
- `unreadable_status_reports_once_per_file_state`, `permission_recovery_surfaces_preserved_status`: a mailbox/status read
  error no longer aborts `Tick`; it raises one `unknown_probe` wake per failure state (signature = error class), the
  other passes still run, and the preserved content surfaces once readable.
- `beacon_stays_fresh_while_absorbing`: needs phase 2's absorb of a benign `working:` note; the beacon half lands here,
  the case goes green in phase 2.

## Files
Modify `internal/watch/watch.go`; create `internal/watch/proc.go` (identity, Healthy, ExitSignals, writeAtomic);
un-stub the lifecycle cases in `port_lifecycle_test.go`.

## Success
`go test -tags port -count=3 -race -run 'FMLifecycle|unreadable|permission_recovery' ./internal/watch` green except the
two R16 cases (w2-hooks). `go test ./...` green.
