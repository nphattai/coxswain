#!/bin/bash
# Facts for the Ship phase, per repo of an epic: where the epic tip sits against the staging and production
# branches (repos.md Production / Staging columns), conflicts with production, production commits the epic lacks,
# open PRs into production, PRs merged into the epic, and the diff surfaces an ops reader must know about
# (env samples, workflows, migrations, Dockerfiles, deploy docs, plans/). Read-only; the leader turns the output
# into the PR body (templates/ship-pr-body.md) through the crewkit-ship skill.
# Usage: bin/ship-facts.sh <project>/epics/<slug>   (fetches origin in every epic worktree; bash 3.2)
set -uo pipefail
. "$(dirname "$0")/lib.sh"
epic_paths "${1:?usage: ship-facts.sh <project>/epics/<slug>}"
repos_md="$root/$project/docs/repos.md"
# repos.md column (2 alias, 3 repo, 6 production, 7 staging); first word of the cell, trailing comma dropped
col() { awk -F'|' -v a="$1" -v c="$2" 'NR>4 { x=$2; gsub(/^ +| +$/,"",x); if (x==a) { v=$c; gsub(/^ +| +$/,"",v); sub(/ .*/,"",v); sub(/,$/,"",v); print v } }' "$repos_md"; }
g() { git -C "$wt" "$@"; }
anc() { g merge-base --is-ancestor "$1" "$2" 2>/dev/null && echo yes || echo no; }
while read -r alias repo; do
  [ -n "$alias" ] || continue
  wt="$(epic_worktree "$alias")"; [ -n "$wt" ] || { echo "== $alias: no epic worktree under $ws/$repo (bin/link.sh)"; continue; }
  prod="$(col "$alias" 6)"; stg="$(col "$alias" 7)"; E="origin/epic/$slug"; P="origin/$prod"
  g fetch -q origin 2>/dev/null
  echo "== $alias ($repo) production=$prod staging=$stg"
  echo "   worktree $wt on $(g rev-parse --abbrev-ref HEAD) $( [ -z "$(g status --short)" ] && echo clean || echo DIRTY)"
  for b in "$E" "$P"; do echo "   $b $(g rev-parse --short "$b") $(g log -1 --format='%ci %s' "$b" | cut -c1-88)"; done
  echo "   epic vs $prod (ahead behind): $(g rev-list --left-right --count "$E...$P" | tr '\t' ' ')   epic in $prod: $(anc "$E" "$P")   $prod in epic: $(anc "$P" "$E")"
  if [ -n "$stg" ] && [ "$stg" != none ]; then
    echo "   epic vs $stg (ahead behind): $(g rev-list --left-right --count "$E...origin/$stg" | tr '\t' ' ')   epic in $stg: $(anc "$E" "origin/$stg")"
  fi
  c="$(g merge-tree --write-tree --name-only "$E" "$P" 2>/dev/null | sed -n '2,/^$/p' | sed '/^$/d')"
  echo "   conflicts with $prod: ${c:-none}" | tr '\n' ' '; echo
  echo "   $prod commits not in epic (newest 20):"; g log --format='     %h %cs %s' "$E..$P" | cut -c1-110 | head -20
  echo "   open PRs into $prod:"; (cd "$wt" && gh pr list --base "$prod" --state open --json number,headRefName,title --jq '.[] | "     #\(.number) \(.headRefName): \(.title)"' 2>/dev/null)
  echo "   PRs merged into epic/$slug:"; (cd "$wt" && gh pr list --base "epic/$slug" --state merged --limit 100 --json number,mergedAt,author,title --jq '.[] | "     #\(.number) \(.mergedAt[:10]) \(.author.login) \(.title)"' 2>/dev/null)
  echo "   diff vs $prod: $(g diff --shortstat "$P...$E")"
  echo "   top dirs:"; g diff --name-only "$P...$E" | cut -d/ -f1-2 | sort | uniq -c | sort -rn | head -10 | sed 's/^/     /'
  echo "   ops surfaces in the diff (read every one):"
  g diff --name-only "$P...$E" | grep -iE '(^|/)\.env|\.github/workflows/|migration|Dockerfile|docker-compose|deploy|eas\.json|app\.config|app\.json|changelog' | sed 's/^/     /'
  echo "   plans/ dirs in the diff (captain rule: plan dirs stay local):"; g diff --name-only "$P...$E" | grep '^plans/' | cut -d/ -f1-2 | sort -u | sed 's/^/     /'
  echo
done < "$epic_dir/repos"
