#!/bin/bash
# Shared helpers for bin/dispatch.sh, bin/status.sh, bin/epic-close.sh, bin/epic-new.sh. Source it; bash 3.2.
# Epic state file: <epic>/.run, append-only "key=value" lines, last value wins (so rewrites are appends).
fm()  { sed -n '/^---$/,/^---$/p' "$1" | sed -n "s/^$2:[[:space:]]*//p" | sed 's/[[:space:]]*#.*$//'; }   # frontmatter field
get() { sed -n "s/^$1=//p" "$state" 2>/dev/null | tail -1; }
put() { echo "$1=$2" >> "$state"; }
# Workspace = the git superproject when this kit is mounted as a submodule, else the checkout that holds bin/ (crewkit D2).
ws_root() { local d; d="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
  { git -C "$d" rev-parse --show-superproject-working-tree 2>/dev/null | grep .; } || git -C "$d" rev-parse --show-toplevel 2>/dev/null || (cd "$d/.." && pwd -P); }
# ws_name: the workspace as named by its origin (the clone lives at ~/Work/<ws_name> on every machine; the epic dir itself may be
# an Orca worktree of the workspace repo, whose directory name says nothing)
ws_name() { basename -s .git "$(git -C "$(ws_root)" remote get-url origin 2>/dev/null || basename "$(ws_root)")"; }
# The one remote worker host is the first non-`local` line of <ws>/hosts (D3); no file = everything runs here.
# set -e safe (grep exits 1/2 on no match / no file); lowercased, CR stripped: the value must pass hosts_ok as a default.
REMOTE="$( { grep -vx local "$(ws_root)/hosts" 2>/dev/null || :; } | head -1 | tr -d '\r' | tr 'A-Z' 'a-z')"
# a host name that matches this machine's hostname is local (leader on the remote host itself: its name means here)
norm_host() { [ -n "$1" ] || return 0; case "$(hostname | tr 'A-Z' 'a-z')" in *"$1"*) echo local;; *) echo "$1";; esac; }
hosts_ok() { case ",$1," in *,,*|*,*[!a-z0-9.,-]*) return 1;; esac; for h in ${1//,/ }; do [ "$h" = local ] || { [ -n "$REMOTE" ] && [ "$h" = "$REMOTE" ]; } || return 1; done; }
# epic_paths <project>/epics/<slug> -> sets epic_dir, slug, project, state, ws, root
epic_paths() {
  epic_dir="$(cd "$1" && pwd)"; slug="$(basename "$epic_dir")"; project="$(basename "$(dirname "$(dirname "$epic_dir")")")"
  state="$epic_dir/.run"; ws="${ORCA_WORKSPACES:-$HOME/orca/workspaces}"; root="$(ws_root)"
}
repo_of() { awk -v a="$1" '$1==a{print $2}' "$epic_dir/repos"; }   # alias -> orca repo name

# --- per-story worktrees (captain 2026-09-01: parallel stories in one repo; PR = rebase onto epic/<slug> first) ---
story_repo() { awk -v a="$(fm "$epic_dir/stories/$1.md" repo)" '$1==a{print $2}' "$epic_dir/repos"; }   # story id -> orca repo name
story_wt()   { printf '%s/%s/story-%s' "$ws" "$1" "$2"; }                                             # <repo> <story id> -> path
# Trust seeding for claude (the folder-trust dialog eats the injected prompt otherwise). One python call, all paths.
trust_worktrees() { [ $# -gt 0 ] || return 0; python3 - "$@" <<'PYT' 2>/dev/null || true
import json,os,sys
f=os.path.expanduser("~/.claude.json"); d=json.load(open(f)) if os.path.exists(f) else {}
p=d.setdefault("projects",{})
for k in sys.argv[1:]:
    e=p.setdefault(k,{}); e["hasTrustDialogAccepted"]=True; e.setdefault("allowedTools",[]); e.setdefault("mcpServers",{})
json.dump(d,open(f,"w"),indent=2)
PYT
}
# ensure_story_worktree <repo> <story id>: an Orca worktree at story_wt on branch story/<id> cut from origin/epic/<slug>.
# Prints the verified path on success; on failure prints nothing and returns non-zero (F02: never fall back to the shared
# epic checkout - an unverified isolated worktree can place a worker in the integration checkout and rewrite its env).
ensure_story_worktree() {
  local repo="$1" id="$2" wt; wt="$(story_wt "$repo" "$id")"
  if [ ! -d "$wt" ]; then
    orca worktree create --repo "name:$repo" --name "story/$id" --base-branch "origin/epic/$slug" --no-parent --json >/dev/null 2>&1 || true
    [ -d "$wt" ] || { echo "error: no isolated story worktree for $id at $wt (Orca worktree create failed); not falling back to the epic checkout" >&2; return 1; }
  fi
  if git -C "$wt" show-ref --quiet "refs/heads/story/$id"; then git -C "$wt" switch -q "story/$id" 2>/dev/null || true
  else git -C "$wt" switch -q -c "story/$id" 2>/dev/null || true; fi
  # a story branch that has not started yet (no own commits, clean tree) follows epic/<slug>: stories build on what is
  # merged there (captain 2026-09-01: no story starts before the foundation PR is in the epic branch)
  git -C "$wt" fetch -q origin "epic/$slug" 2>/dev/null || true
  if [ -z "$(git -C "$wt" status --porcelain 2>/dev/null)" ] && [ -z "$(git -C "$wt" rev-list "origin/epic/$slug..HEAD" 2>/dev/null)" ]; then
    git -C "$wt" reset -q --hard "origin/epic/$slug" 2>/dev/null || true
  fi
  trust_worktrees "$wt"; printf '%s' "$wt"
}

# --- v3 machine layer + epic.env (design D13-D19, D24; bin/infra.sh owns the containers) ---
PG_CONTAINER=crewkit-postgres; PG_PORT=5433; PG_USER=dev; PG_PASS=dev; REDIS_PORT=6380; MINIO_PORT=9000; TEMPORAL_PORT=7234
DEVICE_SLOTS="${DEVICE_SLOTS:-3}"   # booted simulators this machine tolerates; one of them is the CI runner's
# load_epic_env: EPIC PROJECT BACKEND BACKEND_APP API_PORT STORY_PORT_BASE DB_NAME SEED SIM_BASE SECRETS from <epic>/epic.env
load_epic_env() { [ -f "$epic_dir/epic.env" ] || return 1; set -a; . "$epic_dir/epic.env"; set +a; SECRETS="${SECRETS/#\~/$HOME}"; }
short_id() { printf '%s' "${1#$slug-}" | tr - _; }                 # story id -> suffix for database and redis names
psql_() { docker exec -i "$PG_CONTAINER" psql -U "$PG_USER" -d postgres -v ON_ERROR_STOP=1 -tA "$@"; }
db_exists() { [ "$(psql_ -c "select 1 from pg_database where datname='$1'" 2>/dev/null)" = 1 ]; }
db_create() { db_exists "$1" || psql_ -c "create database \"$1\" template \"${2:-template0}\"" >/dev/null; }   # <name> [template]
db_drop()   { db_exists "$1" && psql_ -c "drop database \"$1\" with (force)" >/dev/null; return 0; }
# epic_worktree <alias> -> the epic worktree path (Orca may suffix a recreated worktree: epic-<slug>-2)
epic_worktree() { local r d; r="$(repo_of "$1")"; [ -n "$r" ] || return 1
  d="$ws/$r/epic-$slug"; [ -d "$d" ] || d="$(ls -d "$ws/$r/epic-$slug"-* 2>/dev/null | tail -1 || true)"   # callers run set -e -o pipefail
  [ -n "$d" ] && [ -d "$d" ] && printf '%s' "$d"; return 0; }
# --- story layer: allocate at dispatch --start, release at --done (design 3, 5.2) ---
sim_booted() { xcrun simctl list devices booted -j 2>/dev/null | jq '[.devices[][] | select(.state=="Booted")] | length' 2>/dev/null || echo 0; }
# allocate_story <id> <story file> <worktree>: node_modules + env files cloned from the epic worktree (APFS cp -c, D13);
# with epic.env: a port block, <epic>/.env.<id>, a database clone + redis prefix for BACKEND stories, a simulator clone
# for device: true stories. Idempotent (keys in .run, last wins). Prints what it did.
allocate_story() {
  local id="$1" f="$2" wt="$3" alias ewt short envf n base
  alias="$(fm "$f" repo)"; ewt="$(epic_worktree "$alias" 2>/dev/null)"
  if [ -n "$ewt" ] && [ "$ewt" != "$wt" ]; then
    [ -d "$wt/node_modules" ] || { [ -d "$ewt/node_modules" ] && cp -Rc "$ewt/node_modules" "$wt/node_modules" 2>/dev/null && echo "  $id: node_modules cloned (apfs)"; }
    for e in .env .env.local apps/*/.env apps/*/.env.local; do
      for src in "$ewt"/$e; do [ -e "$src" ] || continue; dst="$wt/${src#$ewt/}"; [ -e "$dst" ] || cp -RP "$src" "$dst"; done
    done
  fi
  load_epic_env 2>/dev/null || return 0
  short="$(short_id "$id")"; envf="$epic_dir/.env.$id"
  base="$(get "port.$id")"
  if [ -z "$base" ]; then n="$(get portn)"; n="${n:-0}"; base=$((STORY_PORT_BASE + 10 * n)); put portn $((n + 1)); put "port.$id" "$base"; fi
  { echo "STORY=$id"; echo "PORT=$base"; echo "API_URL=http://localhost:$API_PORT"; echo "SEED_PREFIX=$id-"
    for kv in ${STORY_ENV_EXTRA:-}; do echo "$kv"; done; } > "$envf"   # epic.env STORY_ENV_EXTRA="K=V K=V": client-story keys (API base names, local API keys)
  # stories name the env file under ~/Work/<workspace> (the project clone); this epic dir may be an Orca worktree: mirror it there
  twin="$HOME/Work/$(ws_name)/$project/epics/$slug"
  if [ "$twin" != "$epic_dir" ] && [ -d "$twin" ]; then cp "$envf" "$twin/" 2>/dev/null
    # the inbox too: stories name <epic>/inbox under the project clone; records are written here - one directory, linked
    [ -L "$twin/inbox" ] || { [ -d "$twin/inbox" ] && cp -Rn "$twin/inbox/" "$epic_dir/inbox/" 2>/dev/null && rm -rf "$twin/inbox"; ln -sfn "$epic_dir/inbox" "$twin/inbox"; }
  fi
  if [ -n "${BACKEND:-}" ] && [ "$alias" = "$BACKEND" ]; then
    local db="${DB_NAME}_$short"
    db_create "$db" "${DB_NAME}_tpl" 2>/dev/null && put "db.$id" "$db" || { echo "  $id: database $db not created (snapshot ${DB_NAME}_tpl missing? bin/local-env.sh $epic_dir up)" >&2; return 1; }
    put "redis.$id" "${DB_NAME}:$short:"
    { echo "DB_HOST=localhost"; echo "DB_PORT=$PG_PORT"; echo "DB_NAME=$db"; echo "DB_USERNAME=$PG_USER"; echo "DB_PASSWORD=$PG_PASS"
      echo "REDIS_URL=redis://localhost:$REDIS_PORT"; echo "REDIS_PREFIX=${DB_NAME}:$short:"
      # sdk-shaped backends read TYPEORM_* and connect to Temporal at boot (same values; harmless for the DB_* readers)
      echo "TYPEORM_HOST=localhost"; echo "TYPEORM_PORT=$PG_PORT"; echo "TYPEORM_SECONDARY_HOST=localhost"; echo "TYPEORM_SECONDARY_PORT=$PG_PORT"
      echo "TYPEORM_DATABASE=$db"; echo "TYPEORM_USERNAME=$PG_USER"; echo "TYPEORM_PASSWORD=$PG_PASS"; echo "TEMPORAL_HOST=localhost:$TEMPORAL_PORT"; } >> "$envf"
    # the cloned worktree .env files still name the EPIC database and port: point them at the story's own
    for envfile in $(ls "$wt"/.env "$wt"/apps/*/.env 2>/dev/null); do [ -f "$envfile" ] || continue   # ls: no unmatched-glob error under zsh
      sed -i '' -e "s/^TYPEORM_DATABASE=.*/TYPEORM_DATABASE=$db/" -e "s/^DB_NAME=.*/DB_NAME=$db/" -e "s/^PORT=.*/PORT=$base/" "$envfile"; done
  fi
  if [ "$(fm "$f" device)" = true ] && [ -n "${SIM_BASE:-}" ]; then
    local udid; udid="$(get "sim.$id")"
    if [ -z "$udid" ]; then
      udid="$(xcrun simctl clone "$SIM_BASE" "$slug-$short" 2>/dev/null | tr -d '[:space:]' || true)"
      [ -n "$udid" ] && { xcrun simctl boot "$udid" >/dev/null 2>&1; put "sim.$id" "$udid"; echo "  $id: simulator $slug-$short ($udid) cloned from SIM_BASE"; } \
        || echo "  $id: WARN simulator clone failed (SIM_BASE=$SIM_BASE)" >&2
    fi
    [ -n "$udid" ] && echo "SIM_UDID=$udid" >> "$envf"   # re-emitted on every allocation: the env file is rewritten above
  fi
  put "env.$id" "$envf"; echo "  $id: env $envf (port $base$(get "db.$id" | sed 's/^./, db &/'))"
}
# release_story <id>: drop the database clone, delete the simulator clone, remove the env file (D16)
release_story() {
  local id="$1" db sim
  db="$(get "db.$id")"; [ -n "$db" ] && { db_drop "$db" && echo "  $id: dropped $db"; put "db.$id" ""; }
  sim="$(get "sim.$id")"; [ -n "$sim" ] && { xcrun simctl shutdown "$sim" >/dev/null 2>&1; xcrun simctl delete "$sim" >/dev/null 2>&1 && echo "  $id: simulator $sim deleted"; put "sim.$id" ""; }
  rm -f "$epic_dir/.env.$id"; put "env.$id" ""
}
