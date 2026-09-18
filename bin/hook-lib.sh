#!/bin/bash
# Epics whose live Run is led by THIS terminal (last leader= in .run equals $ORCA_TERMINAL_HANDLE).
led_epics() {
  local root="$1" h="${ORCA_TERMINAL_HANDLE:-}"; [ -n "$h" ] || return 0
  for r in "$root"/*/epics/*/.run "$root"/*/*/epics/*/.run; do   # one- and two-level projects (apps/<x>/epics in a monorepo workspace)
    [ -f "$r" ] || continue; case "$r" in */node_modules/*) continue;; esac   # npm workspace symlinks mirror apps/* under node_modules
    [ "$(sed -n 's/^leader=//p' "$r" | tail -1)" = "$h" ] && dirname "$r"
  done
}
