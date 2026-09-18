#!/bin/bash
# Self-check for bin/watch.sh's jq filter against a captured Orca batch (bin/test/watch-fixture.json).
# Run: /bin/bash bin/test/watch-parse.sh
set -u
here="$(cd "$(dirname "$0")" && pwd)"
. "$here/../watch.sh"
out=$(jq -r "$WATCH_JQ" < "$here/watch-fixture.json") || { echo "FAIL: jq filter errored"; exit 1; }
fail=0
chk() { grep -q "$1" <<<"$out" || { echo "FAIL: missing $1"; fail=1; }; }
chk $'^heartbeat\tctx_ae65a30255c0\trelay_933cfeff8dbd\treviewing\t'
chk $'^heartbeat\tctx_282338471bfe\trelay_[0-9a-f]*\timplementing\t'
chk $'^question\tctx_ae65a30255c0\trelay_0bfdccd7f75a\t\tQuestion\t'
chk $'^worker_done\tctx_ae65a30255c0\trelay_[0-9a-f]*\t\t'
[ "$(wc -l <<<"$out" | tr -d ' ')" = 4 ] || { echo "FAIL: expected 4 rows"; fail=1; }
[ $fail = 0 ] && echo "ok: 4 rows parsed"
exit $fail
