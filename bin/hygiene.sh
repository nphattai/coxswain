#!/bin/bash
# What this machine still carries from epics: worktrees no open epic references, databases and simulators of closed
# epics, containers outside the machine layer, DerivedData. Report only, one line per candidate: KIND PATH SIZE REASON.
# Deleting is the captain's word (`epic-close.sh --yes`, `simctl delete`, `docker rm`); never a git branch.
# Usage: bin/hygiene.sh
set -uo pipefail
. "$(dirname "$0")/lib.sh"
root="$(ws_root)"; ws="${ORCA_WORKSPACES:-$HOME/orca/workspaces}"
sz() { du -sh "$1" 2>/dev/null | cut -f1; }
# open epics = a .run without .run.closed; their slugs and DB_NAMEs
# open epic = has a DESIGN.md and no .run.closed (an epic created but not yet dispatched is open too)
open_slugs=""; open_dbs=""; all_slugs=""
for e in "$root"/*/epics/*/ "$root"/*/*/epics/*/; do [ -f "$e/DESIGN.md" ] || continue; s="$(basename "$e")"; all_slugs="$all_slugs $s"
  { [ -f "$e/.run.closed" ] || grep -q '^Status: complete' "$e/DESIGN.md"; } && continue
  open_slugs="$open_slugs $s"; d="$(sed -n 's/^DB_NAME=\([a-z0-9_]*\).*/\1/p' "$e/epic.env" 2>/dev/null)"; [ -n "$d" ] && open_dbs="$open_dbs $d"; done
is_open() { case " $open_slugs " in *" $1 "*) return 0;; *) return 1;; esac; }
echo "open epics:${open_slugs:- none}"
for wt in "$ws"/*/*/; do
  [ -d "$wt" ] || continue; n="$(basename "$wt")"; slug=""
  for s in $all_slugs; do case "$n" in "epic-$s"|"epic-$s"-[0-9]*|"story-$s-"*) slug="$s";; esac; done   # Orca suffixes a recreated epic worktree
  reason=""
  if [ -n "$slug" ]; then is_open "$slug" || reason="epic $slug not open"; else reason="not an epic or story worktree"; fi
  [ -n "$reason" ] && printf 'worktree   %s  %s  %s\n' "$wt" "$(sz "$wt")" "$reason"
done
for wt in "$ws"/*/story-*/; do [ -d "$wt/node_modules" ] && [ -n "$(git -C "$wt" status --porcelain 2>/dev/null | head -1)" ] && printf 'worktree   %s  %s  dirty story worktree\n' "$wt" "$(sz "$wt")"; done
docker ps -a --format '{{.Names}}\t{{.Status}}\t{{.Label "com.docker.compose.project"}}' 2>/dev/null | while IFS=$'\t' read -r name status proj; do
  [ "$proj" = crewkit ] && continue; printf 'container  %s  -  %s (project %s)\n' "$name" "$status" "${proj:-none}"; done
docker volume ls --format '{{.Name}}' 2>/dev/null | grep -vE '^(crewkit_|[0-9a-f]{64}$)' | sed 's/^/volume     /; s/$/  -  not the machine layer/'
docker exec "$PG_CONTAINER" psql -U "$PG_USER" -d postgres -tAc "select datname||' '||pg_size_pretty(pg_database_size(datname)) from pg_database where not datistemplate and datname<>'postgres'" 2>/dev/null \
  | while read -r db size; do base="${db%_tpl}"; keep=0; for d in $open_dbs; do case "$db" in "$d"|"${d}_"*) keep=1;; esac; done
      ((keep)) || printf 'database   %s  %s  no open epic owns it\n' "$db" "$size"; done
xcrun simctl list devices -j 2>/dev/null | jq -r '.devices[][] | "\(.udid)\t\(.name)\t\(.state)"' | while IFS=$'\t' read -r udid name state; do
  for s in $all_slugs; do case "$name" in "$s-"*) is_open "$s" || printf 'simulator  %s  -  %s (%s), epic %s not open\n' "$udid" "$name" "$state" "$s";; esac; done; done
dd="$HOME/Library/Developer/Xcode/DerivedData"; [ -d "$dd" ] && printf 'deriveddata %s  %s  rebuilt on demand\n' "$dd" "$(sz "$dd")"
for wt in "$ws"/*/*/; do [ -d "$wt/.nx" ] && [ "$(du -sm "$wt/.nx" 2>/dev/null | cut -f1)" -gt 500 ] && printf 'nx         %s  %s  per-worktree cache\n' "$wt/.nx" "$(sz "$wt/.nx")"; done
exit 0
