#!/bin/bash
# park.sh: asks for the handoff, waits for an idle session with a fresh handoff, stops the dispatch, clears dispatch.<id>
# so --start resumes; --now skips the wait. Fake orca; fake session log under a fake HOME.
# Run: /bin/bash bin/test/park.sh
set -eu
here="$(cd "$(dirname "$0")" && pwd)"; tmp="$(cd "$(mktemp -d)" && pwd)"; trap 'rm -rf "$tmp"' EXIT
e="$tmp/proj/epics/t"; wt="$tmp/orca/workspaces/orca-repo/story-t-a"; mkdir -p "$e/stories" "$e/handoffs" "$tmp/bin" "$wt"
echo "r orca-repo" > "$e/repos"; printf -- '---\nid: t-a\nrepo: r\n---\n' > "$e/stories/t-a.md"
printf 'run=run_p\ndispatch.t-a=ctx_1\nwt.t-a=%s\n' "$wt" > "$e/.run"
d="$tmp/home/.claude/projects/$(echo "$wt" | tr '/' '-')"; mkdir -p "$d"
echo '{"type":"assistant","message":{"role":"assistant","stop_reason":"end_turn","content":[{"type":"text","text":"done"}]}}' > "$d/s.jsonl"
cat > "$tmp/bin/orca" <<'SH'
#!/bin/bash
echo "$*" >> "$FAKE_LOG"; echo '{"result":{}}'
SH
chmod +x "$tmp/bin/orca"; export PATH="$tmp/bin:$PATH" FAKE_LOG="$tmp/calls" HOME="$tmp/home" ORCA_WORKSPACES="$tmp/orca/workspaces" PARK_WAIT=30
: > "$FAKE_LOG"; echo "state: phase 2 done" > "$e/handoffs/t-a.md"
out="$("$here/../park.sh" "$e" t-a 2>&1)"; fail=0
grep -q "parked t-a" <<<"$out" || { echo "FAIL: not parked: $out"; fail=1; }
grep -q "worker-stop --dispatch ctx_1" "$FAKE_LOG" || { echo "FAIL: dispatch not stopped"; fail=1; }
[ -z "$(sed -n 's/^dispatch\.t-a=//p' "$e/.run" | tail -1)" ] || { echo "FAIL: dispatch.t-a not cleared"; fail=1; }
grep -q "^parked.t-a=" "$e/.run" || { echo "FAIL: parked.t-a missing"; fail=1; }
ls "$e"/inbox/t-a/*.msg >/dev/null 2>&1 && grep -q "PARK" "$e"/inbox/t-a/001.msg || { echo "FAIL: PARK record not written"; fail=1; }
[ $fail = 0 ] && echo "ok: park (handoff request, idle wait, stop, dispatch cleared for --start)"
exit $fail
