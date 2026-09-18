#!/bin/bash
# Park a worker that is waiting on the leader (30 min idle, overnight): ask it for a handoff, wait until its turn ends,
# stop and release the Orca dispatch, clear dispatch.<id> in .run so `bin/dispatch.sh <epic> --start` resumes it in a
# fresh session (the story template makes a resumed worker read <epic>/handoffs/<id>.md first). Design D10.
# Usage: bin/park.sh <project>/epics/<slug> <story-id> [--now]   (--now: skip the handoff request, stop at once)
set -uo pipefail
. "$(dirname "$0")/lib.sh"; . "$(dirname "$0")/inbox-lib.sh"
epic_paths "${1:?usage: park.sh <epic> <story-id> [--now]}"; story="${2:?story-id}"; now=0; [ "${3:-}" = --now ] && now=1
disp="$(get "dispatch.$story")"; [ -n "$disp" ] && [ "$disp" != "?" ] || { echo "no live dispatch for $story" >&2; exit 1; }
wt="$(story_worktree "$epic_dir" "$story")" || wt=""
hand="$epic_dir/handoffs/$story.md"
if ((!now)); then
  "$root/bin/send.sh" "$epic_dir" "$story" "PARK: you will be stopped and resumed later in a fresh session. Write $hand now (state, next step, open questions with the assumption you chose, files touched), commit and push your branch, then end your turn with one line. Do not start new work." >/dev/null
  echo "handoff requested; waiting for $story to end its turn (max ${PARK_WAIT:-1200}s)"
  t=0; while [ "$t" -lt "${PARK_WAIT:-1200}" ]; do
    [ -n "$wt" ] && session_idle "$wt" && [ -f "$hand" ] && [ "$(( $(date +%s) - $(stat -f %m "$hand") ))" -lt 900 ] && break
    sleep 15; t=$((t + 15))
  done
  [ -f "$hand" ] || echo "warn: no handoff at $hand after ${t}s; parking anyway (resume reads the story only)" >&2
fi
# F05: clear ownership only after the worker is stopped, or abandoned (abandon fences the dispatch so a later dispatch
# cannot launch a replacement, but does NOT confirm the worker stopped - warn so its terminal gets checked). If both
# fail the worker may still be running under a live dispatch, so keep dispatch.<id>/term.<id> and refuse to park.
if orca orchestration worker-stop --dispatch "$disp" --json >/dev/null 2>&1; then
  :
elif orca orchestration worker-abandon --dispatch "$disp" --json >/dev/null 2>&1; then
  echo "warn: $story parked via abandon: dispatch fenced, worker NOT confirmed stopped - check its terminal" >&2
else
  echo "error: could not stop or abandon $story ($disp); keeping ownership, NOT parking. Check the worker, then retry." >&2
  exit 1
fi
# stop/abandon confirmed: safe to release resources and clear ownership. A release failure is reported but does not
# undo the park (the worker is already stopped); the resource is just not confirmed freed.
orca orchestration worker-release --dispatch "$disp" --json >/dev/null 2>&1 || echo "warn: worker-release failed for $disp; parked anyway, resource release not confirmed" >&2
rm -f "${TMPDIR:-/tmp}/watch-$(get run)/hb/$disp" "${TMPDIR:-/tmp}/watch-$(get run)/phase/$disp" 2>/dev/null
put "parked.$story" "$(date +%s)"; put "dispatch.$story" ""; put "term.$story" ""
echo "parked $story (was $disp); resume with: bin/dispatch.sh $epic_dir --start"
