#!/bin/bash
# Print the unread wake lines of an epic's durable queue and mark them read (offset file). Idempotent.
# Usage: bin/wake-drain.sh <epic_dir> [--peek]
set -uo pipefail
epic_dir="${1:?usage: wake-drain.sh <epic_dir> [--peek]}"; peek="${2:-}"
q="$epic_dir/.watch.queue"; pos="$q.pos"; [ -f "$q" ] || exit 0
total=$(wc -l < "$q" | tr -d ' '); read_n=$(cat "$pos" 2>/dev/null || echo 0)
[ "$total" -gt "$read_n" ] || exit 0
tail -n +$((read_n + 1)) "$q" | while IFS=$'\t' read -r ts line; do printf '[%s] %s\n' "$(date -r "$ts" +%H:%M 2>/dev/null || echo "$ts")" "$line"; done
[ "$peek" = --peek ] || echo "$total" > "$pos"
