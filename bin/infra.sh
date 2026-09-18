#!/bin/bash
# The machine layer: one postgres (:5433), one redis (:6380), one minio (:9000) for every epic on this machine.
# Usage: bin/infra.sh up|down|status     (idempotent; `down` keeps the volumes; docker daemon = OrbStack)
set -uo pipefail
. "$(dirname "$0")/lib.sh"
root="$(ws_root)"
compose() { docker compose -f "$root/bin/infra/docker-compose.yml" -p crewkit "$@"; }
case "${1:-}" in
  up)
    docker info >/dev/null 2>&1 || { orbctl start >/dev/null 2>&1; for i in $(seq 1 24); do docker info >/dev/null 2>&1 && break; sleep 5; done; }
    docker info >/dev/null 2>&1 || { echo "docker daemon down" >&2; exit 1; }
    compose up -d >/dev/null 2>&1 || { echo "compose up failed" >&2; exit 1; }
    for i in $(seq 1 15); do
      docker exec "$PG_CONTAINER" pg_isready -U "$PG_USER" >/dev/null 2>&1 && nc -z localhost "$REDIS_PORT" 2>/dev/null \
        && [ "$(curl -s -o /dev/null -m 3 -w '%{http_code}' "http://localhost:$MINIO_PORT/minio/health/live")" = 200 ] \
        && { echo "infra up (postgres :$PG_PORT, redis :$REDIS_PORT, minio :$MINIO_PORT, temporal :$TEMPORAL_PORT)"; exit 0; }
      sleep 2
    done
    echo "infra not healthy after 30s: docker ps" >&2; exit 1;;
  down)   compose stop >/dev/null 2>&1 && echo "infra stopped (volumes kept)";;
  status)
    docker ps -a --filter label=com.docker.compose.project=crewkit --format '{{.Names}}\t{{.Status}}\t{{.Ports}}'
    docker exec "$PG_CONTAINER" psql -U "$PG_USER" -d postgres -tAc \
      "select datname||' '||pg_size_pretty(pg_database_size(datname)) from pg_database where not datistemplate and datname<>'postgres' order by 1" 2>/dev/null | sed 's/^/db: /';;
  *) echo "usage: $0 up|down|status" >&2; exit 2;;
esac
