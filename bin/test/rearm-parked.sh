#!/bin/bash
# rearm-parked: the Stop-hook idle rearm must count a story as open only by the LAST value of dispatch.<id>. A parked
# story has dispatch.<id>= (empty last value) and must not be counted, so at MAX_WAIT with only parked stories the hook
# exits 0 (no churn). A story still dispatched must count (exit 2). Uses REWAKE_MAX_WAIT=0 like the recovery reproducer.
# Run: /bin/bash bin/test/rearm-parked.sh
set -eu
here="$(cd "$(dirname "$0")" && pwd)"; src="$(cd "$here/.." && pwd)"; tmp="$(cd "$(mktemp -d)" && pwd)"; trap 'rm -rf "$tmp"' EXIT
ws="$tmp/ws"; mkdir -p "$ws"; ln -s "$src" "$ws/bin"   # CLAUDE_PROJECT_DIR: hook resolves root here, finds bin/ + hook-lib.sh
e="$ws/proj/epics/e"; mkdir -p "$e"
run_hook() { CLAUDE_PROJECT_DIR="$ws" ORCA_TERMINAL_HANDLE=term_x REWAKE_MAX_WAIT=0 TMPDIR="$tmp" /bin/bash "$src/hook-stop-rewake.sh" >/dev/null 2>&1; echo $?; }
fail=0

# parked: dispatch.s1 was set then cleared -> open = 0 -> exit 0
printf 'run=run_test\nleader=term_x\ndispatch.s1=ctx_x\ndispatch.s1=\n' > "$e/.run"
rc="$(run_hook)"; [ "$rc" = 0 ] || { echo "FAIL: parked story counted as open (exit $rc, want 0)"; fail=1; }

# still dispatched: dispatch.s1 non-empty -> open = 1 -> exit 2 (start a minimal turn to re-arm the waiter)
printf 'run=run_test\nleader=term_x\ndispatch.s1=ctx_x\n' > "$e/.run"
rc="$(run_hook)"; [ "$rc" = 2 ] || { echo "FAIL: open dispatched story not counted (exit $rc, want 2)"; fail=1; }

# dispatched then done -> open = 0 -> exit 0
printf 'run=run_test\nleader=term_x\ndispatch.s1=ctx_x\ndone.s1=123\n' > "$e/.run"
rc="$(run_hook)"; [ "$rc" = 0 ] || { echo "FAIL: done story counted as open (exit $rc, want 0)"; fail=1; }

[ $fail = 0 ] && echo "ok: rearm-parked (open counts last dispatch value; parked and done stories are not open)"
exit $fail
