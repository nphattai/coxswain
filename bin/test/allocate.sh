#!/bin/bash
# Self-check for the story layer: dispatch --start allocates (port block, .env.<id>, database clone + redis prefix for the
# BACKEND story, nothing for the client story), depends gates the client wave, --done releases. Fake orca, docker, xcrun.
# Run: /bin/bash bin/test/allocate.sh
set -eu
here="$(cd "$(dirname "$0")" && pwd)"; tmp="$(cd "$(mktemp -d)" && pwd)"; trap 'rm -rf "$tmp"' EXIT
e="$tmp/proj/epics/t"; mkdir -p "$e/stories" "$tmp/bin" "$tmp/orca/workspaces/orca-repo/epic-t/node_modules" "$tmp/orca/workspaces/web-repo/epic-t"
printf 'api orca-repo\nweb web-repo\n' > "$e/repos"; echo x > "$tmp/orca/workspaces/orca-repo/epic-t/.env"
printf 'EPIC=t\nPROJECT=proj\nBACKEND=api\nBACKEND_APP=svc\nAPI_PORT=3333\nSTORY_PORT_BASE=3400\nDB_NAME=t_db\nSEED=\nSIM_BASE=\nSECRETS=~/none\n' > "$e/epic.env"
mk() { printf -- '---\nid: %s\nrepo: %s\ndepends: [%s]\nhost:\nagent: claude\nmodel: sonnet\ndevice: false\ntitle: t\n---\n' "$1" "$2" "$3" > "$e/stories/$1.md"; }
mk t-api api ""; mk t-web web t-api
cat > "$tmp/bin/orca" <<'SH'
#!/bin/bash
echo "$*" >> "$FAKE_LOG"
case "$2" in
  run-create|run-current) echo '{"result":{"run":{"id":"run_alloc"}}}';;
  task-create) n=$(grep -c task-create "$FAKE_LOG"); echo "{\"result\":{\"task\":{\"id\":\"task_$n\"}}}";;
  worker-start) echo '{"ok":true}';;
  worker-list) echo '{"result":{"workers":[{"taskId":"task_1","dispatchId":"ctx_1"},{"taskId":"task_2","dispatchId":"ctx_2"}]}}';;
  check) sleep 1; echo '{"result":{"count":0}}';;
  *) echo '{"result":{}}';;
esac
SH
cat > "$tmp/bin/docker" <<'SH'
#!/bin/bash
# fake postgres: `docker exec -i crewkit-postgres psql ... -c "<sql>"`; databases in $FAKE_DBS, one name per line
echo "docker $*" >> "$FAKE_LOG"; sql="${@: -1}"
case "$sql" in
  "select 1 from pg_database where datname='"*) n="${sql#*datname=\'}"; n="${n%\'*}"; grep -qx "$n" "$FAKE_DBS" && echo 1;;
  "create database "*) n="${sql#create database \"}"; n="${n%%\"*}"; echo "$n" >> "$FAKE_DBS";;
  "drop database "*) n="${sql#drop database \"}"; n="${n%%\"*}"; grep -vx "$n" "$FAKE_DBS" > "$FAKE_DBS.n" || true; mv "$FAKE_DBS.n" "$FAKE_DBS";;
esac
SH
printf '#!/bin/bash\nexit 0\n' > "$tmp/bin/ssh"; printf '#!/bin/bash\necho "{}"\n' > "$tmp/bin/xcrun"; printf '#!/bin/bash\ncat\n' > "$tmp/bin/jq.disabled"
chmod +x "$tmp/bin/"*
export PATH="$tmp/bin:$PATH" FAKE_LOG="$tmp/calls" FAKE_DBS="$tmp/dbs" ORCA_TERMINAL_HANDLE=term_test HOME="$tmp" ORCA_WORKSPACES="$tmp/orca/workspaces"
: > "$FAKE_LOG"; echo "t_db_tpl" > "$FAKE_DBS"
mkdir -p "$tmp/orca/workspaces/orca-repo/story-t-api" "$tmp/orca/workspaces/web-repo/story-t-web"   # ensure_story_worktree finds them (no git)
out="$("$here/../dispatch.sh" "$e" --workers=local --start 2>&1)"
s="$e/.run"; fail=0
chk() { grep -q -- "$1" "$2" || { echo "FAIL: '$1' missing in $2"; fail=1; }; }
chk "port.t-api=3400" "$s"; chk "db.t-api=t_db_api" "$s"; chk "redis.t-api=t_db:api:" "$s"; chk "env.t-api=$e/.env.t-api" "$s"
chk "DB_NAME=t_db_api" "$e/.env.t-api"; chk "REDIS_PREFIX=t_db:api:" "$e/.env.t-api"; chk "PORT=3400" "$e/.env.t-api"; chk "SEED_PREFIX=t-api-" "$e/.env.t-api"
grep -q "t_db_api" "$FAKE_DBS" || { echo "FAIL: database clone not created"; fail=1; }
grep -q 'create database "t_db_api" template "t_db_tpl"' "$FAKE_LOG" || { echo "FAIL: clone not from the snapshot"; fail=1; }
grep -q "t-web: waits for" <<<"$out" || { echo "FAIL: client story not gated by depends: $out"; fail=1; }
[ -e "$tmp/orca/workspaces/orca-repo/story-t-api/.env" ] || { echo "FAIL: .env not copied from the epic worktree"; fail=1; }
[ -d "$tmp/orca/workspaces/orca-repo/story-t-api/node_modules" ] || { echo "FAIL: node_modules not cloned"; fail=1; }
out2="$("$here/../dispatch.sh" "$e" --workers=local --done t-api --start 2>&1)"
grep -q "refresh" <<<"$out2" || { echo "FAIL: backend --done did not print the refresh step"; fail=1; }
grep -q "t_db_api" "$FAKE_DBS" && { echo "FAIL: database clone not dropped at --done"; fail=1; }
[ -e "$e/.env.t-api" ] && { echo "FAIL: env file not removed at --done"; fail=1; }
chk "port.t-web=3410" "$s"; chk "env.t-web=$e/.env.t-web" "$s"
grep -q "DB_NAME" "$e/.env.t-web" && { echo "FAIL: client story got a database"; fail=1; }
pkill -f "watch.sh run_alloc " 2>/dev/null || true; sleep 2
[ $fail = 0 ] && echo "ok: allocation (port, env, db clone, redis prefix), depends gate, release at --done"
exit $fail
