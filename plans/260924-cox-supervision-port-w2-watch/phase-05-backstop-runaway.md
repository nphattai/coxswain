---
phase: 5
title: "Heartbeat backstop, override, runaway ladder"
status: pending
priority: P1
effort: ""
dependencies: [4]
---

# Phase 5: Heartbeat backstop, override, runaway ladder

## Overview
A status seen but never surfaced is re-presented by a heartbeat backstop (600s base, backoff to 7200s, no-change
heartbeat absorbed); the captain-relevance override (`FM_CAPTAIN_RE` counterpart) marks a line urgent; B-53: a reply
already consumed through `cox question wait` is not an unread steer, so no runaway interrupt; the stale escalation
names the unread steer.

## Firstmate reads
`bin/fm-watch.sh` `heartbeat_scan_finds_actionable`, `mark_all_captain_relevant_surfaced`, `signal_files_actionable`,
`inbox_steer_check`, `inbox_steer_escalate_unavailable`; `bin/fm-pending-reply-lib.sh`;
`tests/fm-wake-drain-unread-status.test.sh:126`; SKILL.md:74-76; triage `:342,1775,5711,5743,5764`.

## Design
Seen-but-unsurfaced ledger next to `seen` in `.cox/watch/`; `inboxLadder` skips `kind=reply` records whose question is
answered-and-consumed (read through the existing question files, no new package); stale wake note carries the oldest
unread steer's path and first line.

## Success
Groups green `-count=3 -race`, plus `internal/wake` port case `watch.runaway-consumed-reply` (runs in w2-wake's file,
passes against my watcher; I do not edit that file).
