#!/bin/bash
# Self-check for bin/dispatch.sh state handling with a fake `orca` on PATH: three stories A, B(depends A), C.
# Run: /bin/bash bin/test/run-state.sh
set -eu
here="$(cd "$(dirname "$0")" && pwd)"; tmp="$(mktemp -d)"; trap 'rm -rf "$tmp"' EXIT
mkdir -p "$tmp/proj/epics/t/stories" "$tmp/bin"
echo "r orca-repo" > "$tmp/proj/epics/t/repos"
mk() { printf -- '---\nid: %s\nrepo: r\ndepends: [%s]\nhost:\nagent: claude\nmodel: sonnet\ntitle: t\n---\n' "$1" "$2" > "$tmp/proj/epics/t/stories/$1.md"; }
mk A ""; mk B A; mk C ""
cat > "$tmp/bin/orca" <<'SH'
#!/bin/bash
# fake orca: canned JSON, counts calls in $FAKE_LOG
echo "$*" >> "$FAKE_LOG"
case "$2" in
  run-create)  echo '{"result":{"run":{"id":"run_test"}}}';;
  run-current) echo '{"result":{"run":{"id":"run_test"}}}';;
  task-create) n=$(grep -c task-create "$FAKE_LOG"); echo "{\"result\":{\"task\":{\"id\":\"task_$n\"}}}";;
  worker-start) echo '{"ok":true}';;
  worker-list) n=$(grep -c worker-start "$FAKE_LOG"); printf '{"result":{"workers":['; for i in $(seq 1 $n); do t=$(sed -n "s/.*--task \(task_[0-9]*\).*/\1/p" "$FAKE_LOG" | sed -n "${i}p"); printf '%s{"taskId":"%s","dispatchId":"ctx_%s"}' "$([ $i -gt 1 ] && echo ,)" "$t" "$t"; done; echo ']}}';;
  check) sleep 1; echo '{"result":{"count":0}}';;
  *) echo '{"result":{}}';;
esac
SH
printf '#!/bin/bash\nexit 0\n' > "$tmp/bin/ssh"   # fake ssh: the remote story-presence check always passes
chmod +x "$tmp/bin/orca" "$tmp/bin/ssh"
export PATH="$tmp/bin:$PATH" FAKE_LOG="$tmp/calls" ORCA_TERMINAL_HANDLE=term_test HOME="$tmp"
mkdir -p "$tmp/orca/workspaces/orca-repo/story-A" "$tmp/orca/workspaces/orca-repo/story-B" "$tmp/orca/workspaces/orca-repo/story-C"   # ensure_story_worktree finds them (no git); F02: dispatch no longer falls back to the epic checkout
: > "$FAKE_LOG"
"$here/../dispatch.sh" "$tmp/proj/epics/t" --workers=local --start >/dev/null 2>&1   # explicit: the default depends on <ws>/hosts and the hostname; a remote host is never started (allocation is local-only)
s="$tmp/proj/epics/t/.run"
chk() { grep -q "^$1" "$s" || { echo "FAIL: missing $1"; exit 1; }; }
nochk() { grep -q "^$1" "$s" && { echo "FAIL: unexpected $1"; exit 1; } || true; }
chk task.A=task_1; chk task.B=task_2; chk task.C=task_3
chk dispatch.A=ctx_task_1; chk dispatch.C=ctx_task_3; nochk dispatch.B
grep -q -- "--deps \[\"task_1\"\]" "$FAKE_LOG" || { echo "FAIL: B not wired to A"; exit 1; }
grep -q -- "--on " "$FAKE_LOG" && { echo "FAIL: --workers=local must not place a worker on a remote host"; exit 1; }
grep -q -- "--model sonnet" "$FAKE_LOG" || { echo "FAIL: model not passed"; exit 1; }
"$here/../dispatch.sh" "$tmp/proj/epics/t" --workers=local --done A --start >/dev/null 2>&1
chk done.A; chk dispatch.B=ctx_task_2
[ "$(grep -c run-create "$FAKE_LOG")" = 1 ] || { echo "FAIL: run created twice"; exit 1; }
pkill -f "watch.sh run_test " 2>/dev/null || true; sleep 2
echo "ok: run state, deps, --start/--done"
