#!/bin/bash
# Break a worker out of a runaway turn (a capture loop, a rabbit hole) and make it read its inbox NOW.
# Claude Code rejects the pending tool call cleanly on --interrupt (no half-written edit), then sits at the prompt;
# the composer probe reports `unknown` right after, so the doorbell is rung unconditionally here.
# Write the instruction with bin/send.sh FIRST, then run this. Usage: bin/interrupt.sh <project>/epics/<slug> <story-id>
set -uo pipefail
. "$(dirname "$0")/lib.sh"; . "$(dirname "$0")/inbox-lib.sh"
epic_paths "${1:?usage: interrupt.sh <epic> <story-id>}"; story="${2:?story-id}"
h="$(story_terminal "$epic_dir" "$story")" || { echo "no terminal for $story" >&2; exit 1; }
orca terminal send --terminal "$h" --interrupt --json >/dev/null 2>&1 || { echo "interrupt failed on $h" >&2; exit 1; }
sleep 6
orca terminal send --terminal "$h" --text "$(inbox_doorbell "$(inbox_dir "$epic_dir" "$story")")" --enter --json >/dev/null 2>&1 \
  && echo "interrupted + rang $h" || echo "interrupted, ring FAILED on $h"
