#!/bin/bash
# Classify a worker terminal's composer from a bounded Orca tail: empty | pending | busy | unknown.
# Orca's tail is plain text (no ANSI), so ghost text is invisible: a prompt row with trailing text is `pending`
# and a plain text row right above the status line is `pending` too; only a bare `❯` row, or a status line with
# nothing above it, is `empty`. A spinner row means `busy`. Anything else is `unknown` and callers defer.
# Usage: bin/composer.sh <terminal_handle>
set -uo pipefail
t="${1:?usage: composer.sh <terminal_handle>}"
tail_txt="$(orca terminal read --terminal "$t" --json 2>/dev/null | jq -r '.result.terminal.tail[]?' 2>/dev/null | sed 's/[[:space:]]*$//' | grep -v '^$' | tail -6)"
[ -n "$tail_txt" ] || { echo unknown; exit 0; }
echo "$tail_txt" | grep -q -E '[✳✶✻✽✢✺✹✸✷·]─*[A-Z][a-z]+…|Compacting conversation' && { echo busy; exit 0; }
last="$(echo "$tail_txt" | tail -1)"; prev="$(echo "$tail_txt" | tail -2 | head -1)"; [ "$prev" = "$last" ] && prev=""
is_status() { echo "$1" | grep -q -E '(Opus|Sonnet|Haiku) [0-9]|\| [0-9]+k \([0-9]+%\)|[0-9]+% → [0-9]{2}:[0-9]{2}'; }
case "$last" in
  "❯") echo empty; exit 0;;
  "❯ "*) echo pending; exit 0;;
esac
if is_status "$last"; then
  case "$prev" in ""|"❯") echo empty;; "❯ "*) echo pending;; *) if is_status "$prev" || echo "$prev" | grep -q -E '^[─╭╰│⎿⏺]'; then echo empty; else echo pending; fi;; esac
  exit 0
fi
echo unknown
