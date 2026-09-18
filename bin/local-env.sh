#!/bin/bash
# The epic layer, leader-run and generic: the epic backend on API_PORT from the epic worktree's dist, the epic database
# DB_NAME on the machine postgres and its never-connected snapshot DB_NAME_tpl (story databases clone it), seeds.
# Reads <epic>/epic.env (written by epic-new.sh); secrets from SECRETS (~/.config/<workspace>/<slug>.env), never from the repo.
# Usage: bin/local-env.sh <project>/epics/<slug> up|down|restart|refresh|smoke|status|log|seed [args]|snapshot|migrate|build|db-create <n>|db-drop <n>
#   up        infra up, database (clone of the snapshot, else migrate + seed + snapshot), backend (build if no dist)
#   refresh   wave merge (D24): ff-pull the epic worktree, build, migrate + seed DB_NAME, re-snapshot, restart
#   snapshot  DB_NAME_tpl := DB_NAME (backend stopped for the seconds it takes; stories clone from the snapshot)
set -uo pipefail
. "$(dirname "$0")/lib.sh"
epic_paths "${1:?usage: local-env.sh <project>/epics/<slug> <cmd>}"; cmd="${2:-}"; shift 2 2>/dev/null
load_epic_env || { echo "no $epic_dir/epic.env (bin/epic-new.sh writes it)" >&2; exit 1; }
[ -n "${BACKEND:-}" ] || { echo "epic.env has no BACKEND alias: nothing to run" >&2; exit 1; }
S="$(epic_worktree "$BACKEND")"; [ -d "$S" ] || { echo "no epic worktree for $BACKEND under $ws" >&2; exit 1; }
APP="${BACKEND_APP:?epic.env: BACKEND_APP=<nx app name>}"; MAIN="$S/dist/apps/$APP/main.js"
PIDF="$epic_dir/.backend.pid"; LOG="$epic_dir/.backend.log"; TPL="${DB_NAME}_tpl"
code() { curl -s -o /dev/null -m 5 -w '%{http_code}' "$1" 2>/dev/null || echo 000; }
# the backend's environment: the worktree's own .env (dotenv, untracked) under these overrides + the secrets file
backend_env() { [ -f "$SECRETS" ] && { set -a; . "$SECRETS"; set +a; }
  export PORT="$API_PORT" DB_HOST=localhost DB_PORT="$PG_PORT" DB_NAME="$DB_NAME" DB_USERNAME="$PG_USER" DB_PASSWORD="$PG_PASS"
  export REDIS_URL="redis://localhost:$REDIS_PORT" REDIS_PREFIX="${DB_NAME}:" S3_ENDPOINT="http://localhost:$MINIO_PORT" S3_FORCE_PATH_STYLE=true DOTENV_CONFIG_PATH="$S/.env"
  # the sdk repo reads TYPEORM_* (same values) and its Temporal client connects at boot (bin/infra.sh runs a dev server)
  export TYPEORM_HOST=localhost TYPEORM_PORT="$PG_PORT" TYPEORM_DATABASE="$DB_NAME" TYPEORM_USERNAME="$PG_USER" TYPEORM_PASSWORD="$PG_PASS"
  export TYPEORM_SECONDARY_HOST=localhost TYPEORM_SECONDARY_PORT="$PG_PORT" TEMPORAL_HOST="localhost:$TEMPORAL_PORT"; }
HEALTH_PATH="${HEALTH_PATH:-/api/docs-json}"   # epic.env may override (the sdk serves /api/v1/docs-json)
up_p()   { [ "$(code "http://localhost:$API_PORT$HEALTH_PATH")" = 200 ]; }
start()  { up_p && { echo ":$API_PORT already up"; return 0; }
  [ -f "$MAIN" ] || build || return 1
  ( backend_env; cd "$S" || exit 1; nohup node -r dotenv/config "$MAIN" >"$LOG" 2>&1 & echo $! >"$PIDF" )   # plain command + &: $! is node itself
  for i in $(seq 1 60); do sleep 1; up_p && { echo ":$API_PORT up (pid $(cat "$PIDF"), ${i}s, log $LOG)"; return 0; }; done
  echo ":$API_PORT not up after 60s - tail $LOG" >&2; return 1; }
stop()   { [ -f "$PIDF" ] && kill "$(cat "$PIDF")" 2>/dev/null; lsof -tiTCP:"$API_PORT" -sTCP:LISTEN 2>/dev/null | xargs kill 2>/dev/null
  for i in $(seq 1 20); do lsof -tiTCP:"$API_PORT" -sTCP:LISTEN >/dev/null 2>&1 || break; sleep 1; done   # pools close slowly; a stale 200 would fool start()
  lsof -tiTCP:"$API_PORT" -sTCP:LISTEN 2>/dev/null | xargs kill -9 2>/dev/null; rm -f "$PIDF"; echo ":$API_PORT stopped"; }
build()  { echo "building $APP (webpack, minutes)..."; ( backend_env; cd "$S" && npx nx build "$APP" >"$epic_dir/.build.log" 2>&1 ) && [ -f "$MAIN" ] && echo "built $MAIN" || { echo "build failed - see $epic_dir/.build.log" >&2; return 1; }; }
migrate(){ local sc; for sc in ${DB_SCHEMA:-}; do psql_ -d "${1:-$DB_NAME}" -c "create schema if not exists \"$sc\"" >/dev/null; done   # epic.env DB_SCHEMA="a b": migrations that expect the schemas to exist
  ( backend_env; export DB_NAME="${1:-$DB_NAME}" TYPEORM_DATABASE="${1:-$DB_NAME}"; cd "$S" && yarn migration-run "$APP" >"$epic_dir/.migrate.log" 2>&1 ) && echo "migrated ${1:-$DB_NAME}" || { echo "migration failed - see $epic_dir/.migrate.log" >&2; return 1; }; }
seed()   { [ -n "${SEED:-}" ] || { echo "no SEED in epic.env"; return 0; }   # SEED may list several scripts, space-separated, run in order
  local s; for s in $SEED; do [ -x "$S/$s" ] || { echo "seed script missing: $S/$s" >&2; return 1; }; done
  for s in $SEED; do ( backend_env; export PG_CONTAINER PG_USER; cd "$S" && "./$s" "$@" ) || return 1; done; }
snapshot(){ local was=0 rc=0; up_p && { was=1; stop >/dev/null; }   # build the new snapshot first, swap only on success: a client connection to DB_NAME fails the clone
  db_drop "${TPL}_new"
  if db_create "${TPL}_new" "$DB_NAME"; then db_drop "$TPL"; psql_ -c "alter database \"${TPL}_new\" rename to \"$TPL\"" >/dev/null && echo "snapshot $TPL := $DB_NAME"
  else echo "snapshot FAILED: something is connected to $DB_NAME (a client story?); $TPL kept" >&2; rc=1; fi
  ((was)) && start; return $rc; }
ensure_db(){ db_exists "$DB_NAME" && return 0
  if db_exists "$TPL"; then db_create "$DB_NAME" "$TPL"; echo "database $DB_NAME cloned from $TPL"
  else db_create "$DB_NAME"; migrate && seed && snapshot; fi; }
case "$cmd" in
  up)      "$root/bin/infra.sh" up || exit 1; ensure_db && start;;
  down)    stop;;
  restart) stop; start;;
  refresh) [ -z "$(git -C "$S" status --porcelain)" ] || { echo "epic worktree dirty: $S" >&2; exit 1; }
           git -C "$S" fetch -q origin "epic/$slug" && git -C "$S" merge -q --ff-only "origin/epic/$slug" || { echo "ff-pull failed" >&2; exit 1; }
           build && stop >/dev/null && migrate && seed && snapshot && start;;
  smoke)   printf '%-22s %s\n' "$APP :$API_PORT" "$(code "http://localhost:$API_PORT$HEALTH_PATH")"
           printf '%-22s %s\n' "postgres :$PG_PORT" "$(db_exists "$DB_NAME" && echo "$DB_NAME ok" || echo "NO $DB_NAME") / $(db_exists "$TPL" && echo "$TPL ok" || echo "NO $TPL")"
           printf '%-22s %s\n' "redis :$REDIS_PORT" "$( (nc -z localhost "$REDIS_PORT" 2>/dev/null && echo open) || echo CLOSED)"
           printf '%-22s %s\n' "minio :$MINIO_PORT" "$(code "http://localhost:$MINIO_PORT/minio/health/live")"
           printf '%-22s %s\n' "worktree" "$S @ $(git -C "$S" rev-parse --short HEAD) $(git -C "$S" branch --show-current)";;
  status)  up_p && echo ":$API_PORT up (pid $(cat "$PIDF" 2>/dev/null || echo ?))" || echo ":$API_PORT down"; "$root/bin/infra.sh" status;;
  log)     tail -50 "$LOG";;
  seed)    seed "$@";;
  snapshot) snapshot;;
  migrate) migrate "${1:-}";;
  build)   build;;
  db-create) db_create "${1:?name}" "$TPL" && echo "created $1 from $TPL";;
  db-drop)   case "${1:-}" in "$DB_NAME"|"$TPL"|"") echo "refusing to drop '$1'" >&2; exit 1;; esac; db_drop "$1" && echo "dropped $1";;
  *) sed -n '4,7p' "$0" >&2; exit 2;;
esac
