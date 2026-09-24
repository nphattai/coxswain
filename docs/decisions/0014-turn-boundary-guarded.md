# 0014 - The leader turn boundary is guarded; no turn ends blind

- Status: Accepted (captain, 2026-09-21)
- Date: 2026-09-21

## Context

The architecture deep-dive against firstmate (`research/reports/coxswain-vs-firstmate.md`, sections 2.1-2.2) found
that coxswain's Stop hook is a waiter, not a guard: it blocks polling for wakes and re-opens when stories are open
(`cmd/cox/hook.go` `runStopRewake`), but it never checks that a watcher is alive. So a leader can idle for the whole
window over a dead watcher and never notice - `cox doctor` printed exactly that recurring "watcher dead, N stories
active" ISSUE for `m13` and `pi-harness`. Two adjacent gaps made it worse: a watcher whose epic dir, `.cox` tree, or own
binary had vanished kept polling a temp root for hours (B-37: five orphans over four hours after the Pi dogfood
worktrees were removed), and a leader doorbell that never reached its terminal (a stale handle after a restart) was
logged to `watch/log` and otherwise silent, so an unreachable leader stayed unreachable (B-33 also showed the opposite
failure, a nudge storm on a standing unacked backlog).

Firstmate solves the first with a push-based turn-end guard that fires at the boundary and blocks or forces one bounded
follow-up when supervision is needed and no healthy watcher has a fresh beacon (`bin/fm-turnend-guard.sh`,
`docs/turnend-guard.md`), bounded by a per-turn block budget (default 3) so a broken watcher can never wedge the
session; the second with single-instance self-eviction in the watch loop (`bin/fm-watch.sh:2294-2307`); and the third
with a rate-limited out-of-band wedge alarm (`docs/wedge-alarm.md`).

## Decision

1. **Turn-boundary guard (item 1).** `cox hook stop-rewake` and the leader `session-start` hook, before waiting, verify
   for every led epic with an open story that its watcher is alive AND fresh: `.cox/watch.pid` names a live process and
   `watch/lasttick` is younger than three tick intervals (the single `watch.DefaultPoll` constant the loop uses). A dead
   watcher is restarted with the startWatcher-equivalent (a detached `cox watch --epic <dir>`); a restart that cannot
   take over (a live-but-wedged watcher, or a launch error) reopens the turn with the exact repair line
   `cox watch --epic <dir> --replace`. The guard enumerates epics regardless of watcher liveness, so the very watcher
   whose death would hide the epic from every leader hook is still seen. `session-start` cannot block, so it surfaces
   the repair line as session context instead.
2. **Block budget (item 1).** Each block is charged against a per-terminal, per-turn budget file
   (`cox-rewake-<handle>.blocks`, reset when the next turn's `prompt-drain` runs). Once the budget (3) is exhausted the
   guard lets the turn end (exit 0 with a warning) so a permanently broken watcher can never wedge the leader.
3. **Watcher self-eviction (item 2).** The watch loop stands down - releasing its pidfile - when its epic's `.cox` tree
   or the epic dir is gone, its own binary no longer stats, or a `.cox.closed` marker appears; it logs one line first
   when the tree still allows it. `cox doctor` lists every live `cox watch` process whose `--epic` dir no longer exists
   or sits outside every known workspace root (B-37).
4. **Leader reachability (item 3).** A failed leader doorbell is counted per handle (`watch/doorbell-fail/<handle>`); a
   delivered one resets the count. At three consecutive failures the watcher raises one `_leader` stuck wake and, when
   `policy.alerts.channel` is set (`off|osascript|command:<cmd>`, default off), fires that out-of-band channel at most
   once per 30 minutes with the summary passed argv-safe. `cox doctor` raises an ISSUE while the count is >= 3.
   *Superseded 2026-09-24 (firstmate docs/wedge-alarm.md): an unset channel is `auto` (default on, osascript on macOS),
   the channel is a directive list where every non-off entry fires, and each invocation is process-group bounded (10s).*
5. **Nudge rate limit (item 3, B-33).** The leader doorbell nudges a standing unacked urgent backlog, but a re-nudge for
   a backlog whose max gen is unchanged is sent at most once per nudge window (`watch/nudged` records the last-nudged gen
   and time); a backlog that grew always nudges. This self-heals once the leader drains and acks.

## Consequences

- A leader turn can no longer end blind over a dead watcher: it is restarted, or the turn is reopened with the repair
  line, or (only after the budget is spent) it ends loudly rather than wedged.
- Disposable dogfood watchers stop polling temp roots forever, and `cox doctor` names the ones already leaked.
- An unreachable leader becomes a `_leader` stuck wake, a doctor ISSUE, and - if the captain opts into a channel - one
  out-of-band notification per window, instead of a silent line in `watch/log`.
- `policy.alerts` is a new optional section (default off), documented in `docs/reference/policy-json.md`.

## Review when

After the guard has run in the field: check whether the block budget of 3 was ever hit for a healthy watcher (then
raise it), whether the 30-minute alarm window is too quiet or too loud, and whether the freshness window of three ticks
false-restarts a watcher that is merely slow under load.
