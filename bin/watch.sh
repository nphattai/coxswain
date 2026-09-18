#!/bin/bash
# Run watcher: drains the Run mailbox continuously, acks heartbeats silently, and wakes the leader terminal only
# on actionable mail (question, escalation, worker_done) or a silent worker (> STALE_MIN without heartbeat).
# Wake = one line appended to the durable queue <epic>/.watch.queue; the leader session picks it up through its Claude
# Code hooks (bin/hook-prompt-drain.sh at every prompt, bin/hook-stop-rewake.sh while idle). Nothing is ever typed into
# the leader terminal, so the captain's own input is never disturbed (firstmate's model: durable queue + Stop rewake).
# Usage: nohup bin/watch.sh <run_id> <leader_terminal_handle> <epic_dir> >> <epic>/.watch.log 2>&1 &   (dispatch.sh does this)
# State: $TMPDIR/watch-<run>/hb/<dispatch> (mtime = last heartbeat), phase/<dispatch> (last phase).
# Targets macOS /bin/bash 3.2: no associative arrays, BSD `stat -f %m`.
# Log line format: "YYYY-MM-DD HH:MM <type> <dispatch> <msg_id> <phase> <subject> :: <body>" (before 2026-09-02: no date; bin/scorecard.sh reads both)
set -uo pipefail
. "$(dirname "${BASH_SOURCE[0]}")/inbox-lib.sh"
# message -> tsv: type, dispatch (from_handle without "dispatch:"), message id (for reply --id), phase (payload may be a JSON string or an object), subject, body
WATCH_JQ='.result.messages[] | [ .type,
  ((.from_handle // "?") | sub("^dispatch:"; "")),
  .id,
  (((.payload | objects) // (.payload | strings | fromjson?) // {}) | .phase // ""),
  (.subject | tostring | .[0:80]),
  (.body | tostring | .[0:400] | gsub("\n"; " ")) ] | @tsv'
[ "${BASH_SOURCE[0]}" = "$0" ] || return 0   # sourced by bin/test/watch-parse.sh for WATCH_JQ only

run="${1:?usage: watch.sh <run_id> <terminal_handle>}"
# shellcheck disable=SC2034  # positional kept for the dispatch.sh call contract; wakes go through the queue, not the terminal
leader="${2:?leader terminal handle}"
epic_dir="${3:-}"   # optional: epic dir, enables the context line on question wakes
STALE_MIN="${STALE_MIN:-20}"
RUNAWAY_MIN="${RUNAWAY_MIN:-30}"   # a worker busy this long with an unread inbox record is interrupted once (bin/interrupt.sh); 30 min = the D4 fallback, the message at the tool boundary comes first
state="${TMPDIR:-/tmp}/watch-$run"; mkdir -p "$state/hb" "$state/phase"
queue="${epic_dir:-$state}/.watch.queue"
wake() { printf '%s\t%s\n' "$(date +%s)" "watch: $1" >> "$queue"; }
trap 'pkill -P $$ 2>/dev/null' EXIT      # a killed watcher must not leave its `check --wait` child holding the Run waiter
trap 'exit 143' TERM INT

while true; do
  # 60s wait so the stale check below runs every minute; on error (timeout, waiter_exists) fall through to it
  # check runs in the background and we `wait` on it so a TERM interrupts immediately (a foreground child would defer the trap)
  orca orchestration check --run "$run" --wait --types heartbeat,worker_done,escalation,question,status,merge_ready --timeout-ms 60000 --json > "$state/out" 2>/dev/null &
  if wait $!; then out=$(cat "$state/out"); else out=""; sleep 15; fi
  n=$(jq -r '.result.count // 0' <<<"${out:-null}"); d=$(jq -r '.result.deliveryId // empty' <<<"${out:-null}")
  if [ "${n:-0}" -gt 0 ]; then
    # keep every full message on disk: the wake line carries only 400 chars of the body
    msgs="${epic_dir:-$state}/.watch.msgs"; mkdir -p "$msgs"
    jq -c '.result.messages[]' <<<"$out" | while read -r m; do
      mid=$(jq -r '.id' <<<"$m"); jq -r '"# \(.type) from \(.from_handle // "?") [\(.id)]\n## \(.subject)\n\n\(.body)"' <<<"$m" > "$msgs/$mid.md"
    done
    while IFS=$'\t' read -r type disp mid phase subject body; do
      [ -n "$type" ] || continue
      touch "$state/hb/$disp"
      # a finished worker must not be reported STALE forever: forget its heartbeat/phase
      [ "$type" = worker_done ] && rm -f "$state/hb/$disp" "$state/phase/$disp"
      if [ "$type" = heartbeat ]; then
        [ "$(cat "$state/phase/$disp" 2>/dev/null)" = "$phase" ] && continue
        echo "$phase" > "$state/phase/$disp"; echo "$(date '+%F %H:%M') heartbeat $disp $phase phase-change ::"; continue
      fi
      # workers rarely pass --phase; a status/question subject "phase 6 done", "Phase 7 pushed" carries it for status.sh
      if [ -z "$phase" ]; then p=$(grep -oiE 'phase [0-9]+' <<<"$subject" | head -1 | tr -d -c '0-9'); [ -n "$p" ] && { phase="p$p"; echo "$phase" > "$state/phase/$disp"; }; fi
      # a worker whose `ask` timed out (or lost its capability after a compaction) reports by `status`; a status that
      # carries a decision point is as actionable as a question and must not sit in the rewake hook's 5-minute batch
      label="$type"
      [ "$type" = merge_ready ] && label="status(actionable)"   # a worker's PR-ready mail
      if [ "$type" = status ] && grep -qiE 'ready for review|ready to compact|review (changes|fixes) done|worker_done|PR ready|all .*phases done|plan ready|blocked|need(s)? (a )?(decision|ruling|answer)' <<<"$subject $body"; then label="status(actionable)"; fi
      ctx=""; case "$label" in question|status\(actionable\)) ctx=" | ctx: $("$(dirname "$0")/context.sh" "$epic_dir" 2>/dev/null | awk '{print $1, $3, $4}' | tr '\n' ';')";; esac
      echo "$(date '+%F %H:%M') $label $disp $mid $phase $subject :: $body$ctx"
      wake "$label from $disp [$mid] - $subject :: $body$ctx | full: $msgs/$mid.md"
    done < <(jq -r "$WATCH_JQ" <<<"$out")
    [ -n "$d" ] && orca orchestration check --run "$run" --ack "$d" --json >/dev/null 2>&1
  fi
  # inbox re-ring ladder: an unhandled steer older than INBOX_GRACE gets one delivery attempt per grace period
  # (skipped while the composer is not empty); after INBOX_RING_MAX attempts the leader is woken once.
  now=$(date +%s)
  if [ -n "$epic_dir" ]; then
    for rec in "$epic_dir"/inbox/*/*.msg; do
      [ -e "$rec" ] || continue
      age=$(( now - $(stat -f %m "$rec") )); [ "$age" -gt "$INBOX_GRACE" ] || continue
      story="$(basename "$(dirname "$rec")")"; rs="$(dirname "$rec")/.ring-state"; key="$(basename "$rec")"
      # ring rows are "key<TAB>count<TAB>ts"; the escalation row is "key<TAB>escalated<TAB>ts" - keep it out of the numbers
      cnt="$(awk -F'\t' -v k="$key" '$1==k && $2 ~ /^[0-9]+$/ {print $2}' "$rs" 2>/dev/null | tail -1)"; cnt="${cnt:-0}"
      last="$(awk -F'\t' -v k="$key" '$1==k && $2 ~ /^[0-9]+$/ {print $3}' "$rs" 2>/dev/null | tail -1)"; last="${last:-0}"
      [ $(( now - last )) -ge "$INBOX_GRACE" ] || continue
      if [ "$cnt" -ge "$INBOX_RING_MAX" ]; then
        grep -q "^$key	escalated" "$rs" 2>/dev/null || { echo "$(date '+%F %H:%M') STUCK inbox $story/$key unacknowledged after $cnt rings ::"; wake "STUCK inbox $story/$key - worker has not acknowledged after $cnt rings; check its terminal"; printf '%s\tescalated\t%s\n' "$key" "$now" >> "$rs"; }
        continue
      fi
      h="$(story_terminal "$epic_dir" "$story" 2>/dev/null)" || continue
      v="$(inbox_ring "$h" "$(dirname "$rec")" "$(story_worktree "$epic_dir" "$story")")"; echo "$(date '+%F %H:%M') ring $story/$key -> $v ::"
      case "$v" in rang) printf '%s\t%s\t%s\n' "$key" "$((cnt + 1))" "$now" >> "$rs";; esac
      # runaway turn: still busy after RUNAWAY_MIN with this record unread -> interrupt once, which also rings
      # - never for an fyi record; never when the session log shows the worker already read it (acting takes time, the mv
      #   comes after); at most once per story per RUNAWAY_MIN window (the row is keyed by story, not by record)
      swt="$(story_worktree "$epic_dir" "$story" 2>/dev/null)"; lastint="$(awk -F'\t' '$1=="*story*" && $2=="interrupted" {print $3}' "$rs" 2>/dev/null | tail -1)"; lastint="${lastint:-0}"
      if [ "$v" = skipped:busy ] && [ "$age" -gt $(( RUNAWAY_MIN * 60 )) ] && [ $(( now - lastint )) -gt $(( RUNAWAY_MIN * 60 )) ] \
         && ! grep -q '^urgency=fyi' "$rec" && ! { [ -n "$swt" ] && inbox_seen "$swt" "$(dirname "$rec")" "$key"; }; then
        r="$("$(dirname "${BASH_SOURCE[0]}")/interrupt.sh" "$epic_dir" "$story" 2>&1)"; echo "$(date '+%F %H:%M') RUNAWAY $story/$key unread ${age}s while busy -> $r ::"
        printf '*story*\tinterrupted\t%s\n' "$now" >> "$rs"; wake "RUNAWAY $story: busy $((age / 60)) min with $key unread - interrupted and rang; check bin/worker-tail.sh"
      fi
    done
  fi
  # stale: a dispatch we have heard from went quiet
  for f in "$state"/hb/*; do
    [ -e "$f" ] || continue
    disp=$(basename "$f"); age=$(( now - $(stat -f %m "$f") ))
    if [ "$age" -gt $(( STALE_MIN * 60 )) ]; then
      # claude workers go silent on the mailbox while they code: only wake the leader when Orca says the worker
      # is not live (or when the sender is not a dispatch we can read); otherwise log and re-arm the window
      live=""; gone=0
      case "$disp" in
        ctx_*) out="$(orca orchestration worker-read --dispatch "$disp" --json 2>/dev/null)"; jq -e '.ok' <<<"$out" >/dev/null 2>&1 || gone=1
               live="$(jq -r '.result.status.liveness // empty' <<<"$out" 2>/dev/null)";;
        term_*) orca terminal list --json 2>/dev/null | jq -e --arg h "$disp" '[.result.terminals[]?.handle] | index($h) != null' >/dev/null 2>&1 || gone=1   # closed terminals still answer `read`; only the live list is truth
                [ "$gone" = 0 ] && [ "$(orca terminal read --terminal "$disp" --json 2>/dev/null | jq -r '.result.terminal.status // empty')" = running ] && live=live;;
      esac
      if ((gone)); then   # a closed terminal or a settled dispatch (post-worker_done status mail re-created its heartbeat): forget it, no wake
        rm -f "$f" "$state/phase/$disp"; echo "$(date '+%F %H:%M') gone $disp - terminal/dispatch closed, forgotten ::"; continue
      fi
      if [ "$live" = live ]; then
        echo "$(date '+%F %H:%M') quiet $disp - no heartbeat for >${STALE_MIN}m but worker-read says live ::"
      else
        echo "$(date '+%F %H:%M') STALE $disp - no heartbeat for >${STALE_MIN}m ::"
        wake "STALE $disp - no heartbeat for >${STALE_MIN}m, check worker-read"
      fi
      touch "$f"   # one check per window
    fi
  done
done
