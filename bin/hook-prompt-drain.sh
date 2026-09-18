#!/bin/bash
# UserPromptSubmit hook: attach the unread watcher wakes of every epic this terminal leads to the captain's prompt.
# stdout becomes context for the turn; nothing is typed anywhere.
. "$(dirname "$0")/lib.sh"; root="${CLAUDE_PROJECT_DIR:-$(ws_root)}"; . "$root/bin/hook-lib.sh"
for e in $(led_epics "$root"); do
  out="$("$root/bin/wake-drain.sh" "$e")"; [ -n "$out" ] && printf 'Watcher wakes for %s since your last turn:\n%s\n' "$(basename "$e")" "$out"
done
exit 0
