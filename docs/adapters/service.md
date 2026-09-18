# Service adapter

A service adapter is a shell script the project owns and cox calls: `<workspace>/cox/services/<alias>.sh`. cox never
contains service logic (plan 5.3, F13); it invokes the script with one of four verbs and reads the exit code.

## Contract

```
<alias>.sh preflight   # free the port / check prerequisites, start nothing; non-zero = not ready
<alias>.sh start       # launch the service in the background, record the pid where health/stop find it
<alias>.sh health      # exit 0 = healthy, non-zero = unhealthy (cox reads this as fail; a missing/unrunnable script is unknown)
<alias>.sh stop        # stop only the process this adapter started; verify ownership before killing
```

cox provides the environment from the story env file (`<epic>/.env.<story>`, written by `cox env up`): always
`STORY`, `PORT`, `API_URL`, `SEED_PREFIX`; for a backend story also `DB_HOST`, `DB_PORT`, `DB_NAME`, `DB_USERNAME`,
`DB_PASSWORD`, `REDIS_URL`, `REDIS_PREFIX`, `TYPEORM_*`, `TEMPORAL_HOST`; for a device story `SIM_UDID`; plus any
`KEY=value` pairs a project adds via `STORY_ENV_EXTRA` in its epic env. `HEALTH_PATH` is not one of these - a script
that wants a configurable health path (as in the example below) must default it itself. Do not hardcode ports or
paths. `cox env smoke` runs `health` and is three-state: exit 1 on a fail, exit 3 on an unknown (a script that is
missing or not executable). `templates/services/example.sh` is a working skeleton.

## Ownership (F13)

cox verifies the port owner before `stop`: a port held by a pid cox did not start is reported as a conflict, never
killed. Your `stop` verb must be equally careful - kill only the pid in your own pidfile, not "whatever listens on the
port". Your `preflight` may refuse when the port is held by an unowned process rather than clearing it blindly.

## Porting the pipo-admin shim

The old shim `~/Work/example/infra/pipo-admin-twin-up.sh` hardcodes the epic dir, the pid file (`$E/.backend-skip.pid`),
the log (`$E/.backend-skip.log`), and port 3336, and it does readiness AFTER starting. Split it into the four verbs and
take the port and paths from the environment cox passes:

```bash
#!/usr/bin/env bash
set -euo pipefail
verb="${1:?}"
: "${PORT:?}"
pidfile="${COX_SERVICE_PIDFILE:-/tmp/cox-pipo-admin-${PORT}.pid}"
logfile="${COX_SERVICE_LOGFILE:-/tmp/cox-pipo-admin-${PORT}.log}"
main="dist/apps/pipo-core-service/main.js"   # relative to the app worktree cox runs this in

case "$verb" in
  preflight)
    # refuse only if an UNOWNED process holds the port
    if lsof -tiTCP:"$PORT" -sTCP:LISTEN >/dev/null 2>&1 \
       && ! { [ -f "$pidfile" ] && kill -0 "$(cat "$pidfile")" 2>/dev/null; }; then
      echo "port $PORT held by an unowned process" >&2; exit 1
    fi ;;
  start)
    SKIP_ADMIN_AUTHORIZATION=true PORT="$PORT" \
      nohup node -r dotenv/config "$main" >"$logfile" 2>&1 & echo $! >"$pidfile" ;;
  health)
    curl -fsS -m 3 "http://localhost:${PORT}${HEALTH_PATH:-/api/v1/docs/admin-json}" >/dev/null ;;
  stop)
    [ -f "$pidfile" ] && kill "$(cat "$pidfile")" 2>/dev/null || true; rm -f "$pidfile" ;;
  *) echo "usage: $0 preflight|start|health|stop" >&2; exit 2 ;;
esac
```

The old shim stays in place for v1; do not edit the downstream workspace. Register the adapter in
`<workspace>/cox/workspace.json` under `services` (`{"alias":"pipo-admin","script":"services/pipo-admin.sh"}`).
