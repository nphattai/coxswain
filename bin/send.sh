#!/bin/bash
# Steer a worker by durable record: bin/send.sh [--fyi] [--override "<reason>"] <project>/epics/<slug> <story-id> "<text>"   ("-" = stdin)
# The text lands in <epic>/inbox/<story>/NNN.msg (the record is the delivery); the terminal gets only the constant doorbell,
# and only when its composer is empty. Delivery at the worker's next TOOL BOUNDARY (design D4) is the leader's own
# SendMessage tool: this script prints the session name to send the one-line pointer to (ListAgents shows it as
# `story-<id>-xx`); bin/watch.sh re-rings the doorbell as the fallback. Budget: 5 steers per story (D4); the 6th needs
# --override "<reason>", which is logged in the record. --fyi records are unlimited and never interrupt. Exit 0 = recorded.
set -uo pipefail
. "$(dirname "$0")/lib.sh"; . "$(dirname "$0")/inbox-lib.sh"
urg=steer; override=""
while :; do case "${1:-}" in --fyi) urg=fyi; shift;; --override) override="${2:?--override <reason>}"; shift 2;; *) break;; esac; done
epic_paths "${1:?usage: send.sh [--fyi] [--override <reason>] <epic> <story-id> <text|->}"; story="${2:?story-id}"; text="${3:?text}"
[ "$text" = - ] && text="$(cat)"
[ -n "$(get "dispatch.$story")" ] || { echo "no live dispatch for story $story in $state" >&2; exit 1; }
if [ "$urg" = steer ]; then
  d="$(inbox_dir "$epic_dir" "$story")"; n="$(grep -lL '^urgency=fyi' "$d"/*.msg "$d"/handled/*.msg 2>/dev/null | wc -l | tr -d ' ')"
  if [ "$n" -ge "${STEER_BUDGET:-5}" ] && [ -z "$override" ]; then
    echo "steer budget: $story already has $n steers (budget ${STEER_BUDGET:-5}); batch it into the next reply, or --override \"<reason>\"" >&2; exit 3
  fi
  [ -n "$override" ] && text="override: $override
$text"
fi
rec="$(inbox_write "$epic_dir" "$story" "$text" "$urg")"; echo "recorded $rec"
[ "$urg" = steer ] && echo "deliver now: SendMessage to the worker session (ListAgents: story-$story-*) with: 'Leader instruction in $rec - read it at your next tool boundary, mv it to handled/, then act.'"
h="$(story_terminal "$epic_dir" "$story")" || { echo "no terminal for $story yet; watcher will ring later"; exit 0; }
echo "doorbell -> $h: $(inbox_ring "$h" "$(inbox_dir "$epic_dir" "$story")" "$(story_worktree "$epic_dir" "$story")")"
