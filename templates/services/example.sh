#!/usr/bin/env bash
# Example cox service adapter. Copy to <workspace>/cox/services/<alias>.sh and fill in the four verbs. cox calls this
# script; the project owns the service logic (plan 5.3, F13). Each verb prints human output and exits 0 on success.
#
#   preflight  free the port / check prerequisites, without starting anything
#   start      start the service in the background, record its pid where health/stop can find it
#   health     exit 0 = healthy, 1 = unhealthy; anything the adapter cannot determine is a non-zero it explains
#   stop       stop only the process this adapter started (verify ownership before killing; never kill an unknown pid)
#
# The environment (PORT, DB_*, etc.) is provided by cox from the story env file; do not hardcode paths.
set -euo pipefail

verb="${1:-}"
: "${PORT:?PORT must be set by cox}"
pidfile="${COX_SERVICE_PIDFILE:-/tmp/cox-service-${PORT}.pid}"
logfile="${COX_SERVICE_LOGFILE:-/tmp/cox-service-${PORT}.log}"
health_url="http://localhost:${PORT}${HEALTH_PATH:-/health}"

case "$verb" in
  preflight)
    # Fail if the port is already held by a process this adapter does not own.
    if lsof -tiTCP:"$PORT" -sTCP:LISTEN >/dev/null 2>&1; then
      if [ -f "$pidfile" ] && kill -0 "$(cat "$pidfile")" 2>/dev/null; then
        echo "preflight: our pid $(cat "$pidfile") already holds :$PORT"
      else
        echo "preflight: :$PORT is held by an unowned process; refusing" >&2
        exit 1
      fi
    fi
    echo "preflight ok on :$PORT"
    ;;
  start)
    # Replace the echo with the real launch, redirecting to $logfile and writing the pid to $pidfile.
    echo "start: launch your service here, e.g. nohup <cmd> > $logfile 2>&1 & echo \$! > $pidfile" >&2
    exit 1
    ;;
  health)
    if curl -fsS -m 3 "$health_url" >/dev/null 2>&1; then
      echo "healthy"
      exit 0
    fi
    echo "unhealthy: no 200 from $health_url" >&2
    exit 1
    ;;
  stop)
    if [ -f "$pidfile" ]; then
      pid="$(cat "$pidfile")"
      if kill -0 "$pid" 2>/dev/null; then
        kill "$pid"
        echo "stopped pid $pid"
      fi
      rm -f "$pidfile"
    else
      echo "stop: no pidfile; nothing this adapter owns" >&2
    fi
    ;;
  *)
    echo "usage: $0 preflight|start|health|stop" >&2
    exit 2
    ;;
esac
