#!/usr/bin/env bash
# Stand up one epic on every host of the workspace: an Orca worktree on branch epic/<slug> per repo (local and on the
# remote host named in <ws>/hosts), the branch pushed to origin, Claude trust pre-seeded for every worktree, the epic dir
# with repos/symlinks/DESIGN.md.
# Usage: bin/epic-new.sh <project> <slug> <alias>=<orca-repo-name> [...]   [--hosts=local,<remote>]
# Example: bin/epic-new.sh shop checkout-v2 api=shop-api web=shop-web
# Idempotent. Must run inside an Orca terminal (worktree RPCs). Base branch = Production column of <project>/docs/repos.md.
set -euo pipefail
. "$(dirname "$0")/lib.sh"
root="$(ws_root)"
hosts="local${REMOTE:+,$REMOTE}"; args=()
for a in "$@"; do case "$a" in --hosts=*) hosts="${a#--hosts=}";; *) args+=("$a");; esac; done
set -- "${args[@]}"
project="${1:?usage: epic-new.sh <project> <slug> <alias>=<repo>... [--hosts=local,<remote>]}"; slug="${2:?slug}"; shift 2
(($#)) || { echo "need at least one <alias>=<repo>" >&2; exit 1; }
[[ -d "$root/$project" ]] || { echo "unknown project: $project" >&2; exit 1; }
hosts_ok "$hosts" || { echo "bad --hosts=$hosts (local${REMOTE:+,$REMOTE})" >&2; exit 1; }
ws="${ORCA_WORKSPACES:-$HOME/orca/workspaces}"
# the remote host is used only when named in --hosts AND known to this machine's Orca as an environment
remote=""; [[ -n "$REMOTE" && ",$hosts," == *",$REMOTE,"* ]] && remote="$(orca environment list --json 2>/dev/null | jq -r --arg n "$REMOTE" '.result.environments[]? | select(.name==$n) | .name' | head -1)"
epic_dir="$root/$project/epics/$slug"
mkdir -p "$epic_dir/stories" "$epic_dir/plans" "$epic_dir/reports" "$epic_dir/handoffs"
touch "$epic_dir/repos"   # append-only: a rerun with a subset of repos must not drop the others
trust_paths=()

# Production branch of a repo: column found by the header name, not by position (projects differ in column count)
base_of() { awk -F'|' -v r="$1" '!c && /Production/ {for(i=1;i<=NF;i++) if($i ~ /Production/) c=i; next} c && index($3, r) {split($c, a, " "); print a[1]; exit}' "$root/$project/docs/repos.md"; }
orca_or_die() { local out; out="$(orca "$@" 2>&1)" || { echo "orca $1 $2 failed: $(jq -r '.error.message // empty' <<<"$out" 2>/dev/null || echo "$out")" >&2; exit 1; }; }

for pair in "$@"; do
  alias="${pair%%=*}"; repo="${pair#*=}"; grep -qx "$alias $repo" "$epic_dir/repos" || echo "$alias $repo" >> "$epic_dir/repos"
  base="$(base_of "$repo")"; [[ -n "$base" ]] || { echo "no Production branch for $repo in $project/docs/repos.md" >&2; exit 1; }
  main="$(ls -d "$HOME/Work/repo/"*/"$repo" 2>/dev/null | head -1)"; [[ -n "$main" ]] || { echo "no main checkout for $repo under ~/Work/repo" >&2; exit 1; }
  git -C "$main" fetch -q origin "$base" 2>/dev/null || true
  wt="$ws/$repo/epic-$slug"
  if [[ ! -d "$wt" ]]; then
    # Orca slugifies --name (epic/<slug> -> epic-<slug>) and prefixes the branch; fixed right after.
    orca_or_die worktree create --repo "name:$repo" --name "epic/$slug" --base-branch "origin/$base" --no-parent --json
    [[ -d "$wt" ]] || { echo "orca did not create $wt" >&2; exit 1; }
  fi
  if git -C "$wt" show-ref --quiet "refs/heads/epic/$slug"; then git -C "$wt" switch -q "epic/$slug"; else git -C "$wt" switch -q -c "epic/$slug"; fi
  # no branch deletion (F01, captain ruling: no tool deletes a git branch); Orca's slugified branch name, if any, is left as-is
  # the epic branch must exist on origin: workers PR into it and the remote worktree is cut from it
  git -C "$wt" ls-remote --exit-code --heads origin "epic/$slug" >/dev/null 2>&1 || git -C "$wt" push -q -u origin "epic/$slug"
  echo "local  $alias: $wt @ $(git -C "$wt" branch --show-current)"
  trust_paths+=("$wt")
  if [[ -n "$remote" ]]; then
    if ! orca worktree list --environment "$remote" --json | jq -e --arg p "$wt" '.result.worktrees[] | select(.path==$p)' >/dev/null; then
      orca_or_die worktree create --environment "$remote" --repo "name:$repo" --name "epic/$slug" --base-branch "origin/epic/$slug" --no-parent --json
    fi
    ssh -n "$remote" "git -C '$wt' fetch -q origin 'epic/$slug' && (git -C '$wt' switch -q 'epic/$slug' 2>/dev/null || git -C '$wt' switch -q -c 'epic/$slug' --track 'origin/epic/$slug'); echo \"$remote   $alias: $wt @ \$(git -C '$wt' branch --show-current)\""
  fi
done

# Claude trust: the folder-trust dialog eats the injected prompt otherwise. One python call per host, all paths at once.
TRUST_PY='import json,os,sys
f=os.path.expanduser("~/.claude.json"); d=json.load(open(f)) if os.path.exists(f) else {}
p=d.setdefault("projects",{})
for k in sys.argv[1:]:
    e=p.setdefault(k,{}); e["hasTrustDialogAccepted"]=True; e.setdefault("allowedTools",[]); e.setdefault("mcpServers",{})
json.dump(d,open(f,"w"),indent=2); print("trusted", len(sys.argv)-1, "worktrees")'
python3 -c "$TRUST_PY" "${trust_paths[@]}"
[[ -n "$remote" ]] && ssh -n "$remote" "python3 -c '$TRUST_PY' ${trust_paths[*]}" | sed "s|^|$remote: |"

"$root/bin/link.sh" "$epic_dir"
# DESIGN.md from templates/epic/DESIGN.md (copy, do not search: D19)
if [[ ! -f "$epic_dir/DESIGN.md" ]]; then
  rows="$(awk '{printf "| %s | %s | TODO |\n", $1, $2}' "$epic_dir/repos")"
  python3 - "$root/templates/epic/DESIGN.md" "$epic_dir/DESIGN.md" "$slug" "$project" "$rows" <<'PYT'
import sys; t=open(sys.argv[1]).read()
open(sys.argv[2],"w").write(t.replace("{{slug}}",sys.argv[3]).replace("{{project}}",sys.argv[4]).replace("{{repo_rows}}",sys.argv[5].rstrip("\n")))
PYT
fi
# epic.env: names and ports only, next free block after every other epic's (D17); the leader edits BACKEND_APP/SEED/SIM_BASE
if [[ ! -f "$epic_dir/epic.env" ]]; then
  # `|| :`: in a workspace with no epic yet the glob matches nothing and cat fails, which pipefail + set -e would turn into an exit
  api=$({ cat "$root"/*/epics/*/epic.env "$root"/*/*/epics/*/epic.env 2>/dev/null || :; } | sed -n 's/^API_PORT=\([0-9][0-9]*\).*/\1/p' | sort -n | tail -1); api=$(( ${api:-3332} + 1 ))
  base=$({ cat "$root"/*/epics/*/epic.env "$root"/*/*/epics/*/epic.env 2>/dev/null || :; } | sed -n 's/^STORY_PORT_BASE=\([0-9][0-9]*\).*/\1/p' | sort -n | tail -1); base=$(( ${base:-3300} + 100 ))
  backend="$(awk '$2 ~ /services/ {print $1; exit}' "$epic_dir/repos")"
  cat > "$epic_dir/epic.env" <<EV
EPIC=$slug
PROJECT=$project
BACKEND=$backend                     # alias whose stories run their own backend and get a database clone; blank = none
BACKEND_APP=                         # nx app of the backend (dist/apps/<app>/main.js), e.g. insurtech-service
API_PORT=$api                        # the epic backend; story n gets STORY_PORT_BASE + 10n
STORY_PORT_BASE=$base
DB_NAME=$(tr - _ <<<"$slug")         # epic database on the machine postgres (bin/infra.sh); snapshot <DB_NAME>_tpl; clones <DB_NAME>_<story>
DB_SCHEMA=                           # schemas the backend migrations expect to exist, space-separated (services: ms_ins ms_user ms_ai ms_gw)
SEED=                                # seed script in the backend worktree, e.g. tools/seed-$slug-local.sh (SQL against DB_NAME)
SIM_BASE=                            # udid of the epic's base simulator; blank = no device stories
SECRETS=~/.config/$(ws_name)/$slug.env   # sourced by local-env.sh before the backend starts; never read by a worker
EV
  echo "epic.env written (api :$api, story ports from $base); edit BACKEND_APP, SEED, SIM_BASE if needed"
fi
echo "epic dir: $epic_dir"
