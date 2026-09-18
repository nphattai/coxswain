#!/bin/bash
# hook-session-compact.sh: from a story worktree (wt.<id> in .run, or the story-<id> dir name) it prints the handoff and the
# story's Read first block; outside a story it prints nothing. Run: /bin/bash bin/test/hook-compact.sh
set -eu
here="$(cd "$(dirname "$0")" && pwd)"; tmp="$(cd "$(mktemp -d)" && pwd)"; trap 'rm -rf "$tmp"' EXIT
e="$tmp/Work/root/proj/epics/t"; wt="$tmp/ws/repo/story-t-a"; mkdir -p "$e/stories" "$e/handoffs" "$wt" "$tmp/other" "$tmp/Work/root/bin" "$tmp/Work/junk/x/epics/t"
touch "$tmp/Work/root/bin/lib.sh"; printf 'wt.t-a=%s\n' "$wt" > "$tmp/Work/junk/x/epics/t/.run"   # junk: no kit marker, must be ignored
printf -- '---\nid: t-a\nrepo: r\n---\n\n## Read first\n- Contract: X\n- Env: Y\n\n## Goal\nz\n' > "$e/stories/t-a.md"
echo "next step: phase 3" > "$e/handoffs/t-a.md"; printf 'wt.t-a=%s\n' "$wt" > "$e/.run"
fail=0
out="$(cd "$wt" && HOME="$tmp" "$here/../hook-session-compact.sh")"
grep -q "next step: phase 3" <<<"$out" || { echo "FAIL: handoff not printed"; fail=1; }
grep -q "Contract: X" <<<"$out" && ! grep -q "^z" <<<"$out" || { echo "FAIL: Read first block wrong: $out"; fail=1; }
grep -q ".env.t-a" <<<"$out" || { echo "FAIL: env file pointer missing"; fail=1; }
: > "$e/.run"; out2="$(cd "$wt" && HOME="$tmp" "$here/../hook-session-compact.sh")"   # fallback: dir name
grep -q "story t-a" <<<"$out2" || { echo "FAIL: dir-name fallback: $out2"; fail=1; }
out3="$(cd "$tmp/other" && HOME="$tmp" "$here/../hook-session-compact.sh")"
[ -z "$out3" ] || { echo "FAIL: printed outside a story: $out3"; fail=1; }
[ $fail = 0 ] && echo "ok: compact hook (handoff + Read first, dir fallback, silent elsewhere)"
exit $fail
