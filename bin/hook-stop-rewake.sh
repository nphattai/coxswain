#!/bin/bash
# Stop hook (asyncRewake): while the leader is idle, wait for a watcher wake in the durable queue of any epic this
# terminal leads; when one arrives print it to stderr and exit 2, which starts a new leader turn without typing into
# the composer. Silent exit 0 when this terminal leads nothing, after MAX_WAIT, or when another waiter already runs.
# Firstmate's model (bin/fm-claude-stop-autoarm.sh), reduced to what this workflow needs.
. "$(dirname "$0")/lib.sh"; root="${CLAUDE_PROJECT_DIR:-$(ws_root)}"; . "$root/bin/hook-lib.sh"
epics="$(led_epics "$root")"; [ -n "$epics" ] || exit 0
lock="${TMPDIR:-/tmp}/rewake-${ORCA_TERMINAL_HANDLE}.lock"
if [ -f "$lock" ] && kill -0 "$(cat "$lock" 2>/dev/null)" 2>/dev/null; then exit 0; fi   # single waiter per leader
echo $$ > "$lock"; trap 'rm -f "$lock"' EXIT
MAX_WAIT="${REWAKE_MAX_WAIT:-3300}"; t=0; batch=0
while [ "$t" -lt "$MAX_WAIT" ]; do
  pending=""
  for e in $epics; do
    out="$("$root/bin/wake-drain.sh" "$e" --peek)"; [ -n "$out" ] && pending="$pending$(basename "$e"):
$out
"
  done
  # batching: a question, escalation, worker_done, STUCK or RUNAWAY starts a turn at once; anything else (status,
  # STALE, quiet) waits up to WAKE_BATCH seconds so several routine wakes cost one leader turn, not one each
  # (example-app-mobile: 2 of 3 leader turns were machine wakeups at ~355k context apiece)
  if [ -n "$pending" ]; then
    if grep -qE '(question|escalation|worker_done|actionable|STUCK|RUNAWAY)' <<<"$pending" || [ "$batch" -ge "${WAKE_BATCH:-300}" ]; then
      printf 'Watcher wake while idle. Run bin/wake-drain.sh <epic> to mark these read, then handle them:\n%s' "$pending" >&2
      exit 2
    fi
    batch=$((batch + 15))
  else
    batch=0
  fi
  sleep 15; t=$((t + 15))
  epics="$(led_epics "$root")"; [ -n "$epics" ] || exit 0   # run closed or leader moved
done
# MAX_WAIT reached without a wake. A Stop hook cannot outlive its timeout, and an idle leader with no waiter sleeps
# until the captain types: on 2026-09-13 the leader's turn ended 22:51, the waiter expired 23:46, the PR-ready asks
# landed 23:47 and 00:05 and waited until 08:48. While any led story is dispatched and not done, start a minimal turn
# (exit 2) so that turn's Stop re-arms a fresh waiter; with no open story stay silent (no idle churn between epics).
open=0
for e in $epics; do
  r="$e/.run"; [ -f "$r" ] || continue
  # .run is append-only "last value wins"; a parked story has dispatch.<id> set then re-appended empty. Count a story
  # open only when the LAST dispatch.<id> is a real handle (non-empty, not "?") and no done.<id> was written.
  for id in $(sed -n 's/^dispatch\.\([^=]*\)=.*/\1/p' "$r" | sort -u); do
    last="$(sed -n "s/^dispatch\.$id=//p" "$r" | tail -1)"
    [ -n "$last" ] && [ "$last" != "?" ] || continue
    grep -q "^done\.$id=" "$r" && continue
    open=$((open + 1))
  done
done
if [ "$open" -gt 0 ]; then
  printf 'Rewake tick: no watcher wake in %s min and %s dispatched story(ies) still open. Nothing to handle - end this turn at once with one line and no tool calls, so the Stop hook re-arms the waiter.\n' "$((MAX_WAIT / 60))" "$open" >&2
  exit 2
fi
exit 0
