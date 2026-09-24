---
phase: 6
title: "Leader alarm"
status: pending
priority: P1
effort: ""
dependencies: [1]
---

# Phase 6: Leader alarm

## Overview
Out-of-band leader alarm per `docs/wedge-alarm.md`: absent config = `auto` (default on), `auto` resolves to
`osascript` on macOS, a newline list fires every non-off channel best-effort, each invocation bounded by 10s and on
timeout the whole process group is killed.

## Firstmate reads
`docs/wedge-alarm.md` (whole), the alarm function in `bin/fm-watch.sh` / `fm-wake-lib.sh`.

## Design
`runAlarmChannel` -> `exec.Command` with `Setpgid`, `context.WithTimeout(10s)`, kill `-pgid` on timeout. Channel
list split on newlines. `workspace.Policy.AlertsChannel()` default `"auto"` (Q1).

## Success
`go test -tags port -count=3 -race -run TestPortWedgeAlarm ./internal/wake` green (file owned by w2-wake, read only
here).
