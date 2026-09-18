#!/bin/bash
# F02: when Orca cannot create an isolated story worktree, ensure_story_worktree must fail (exit != 0) and print no path,
# and dispatch --start must stop that story with a clear error instead of falling back to the shared epic checkout - so no
# env/port/allocation key is written for it. Fake orca (worktree create fails). Run: /bin/bash bin/test/worktree-no-fallback.sh
set -eu
here="$(cd "$(dirname "$0")" && pwd)"; src="$(cd "$here/.." && pwd)"; tmp="$(cd "$(mktemp -d)" && pwd)"; trap 'rm -rf "$tmp"' EXIT
fail=0

# --- part 1: the helper returns non-zero and prints nothing when creation fails ---
out="$(ORCA_WORKSPACES="$tmp/ws" /bin/bash -c '
  . "'"$src"'/lib.sh"; ws="'"$tmp"'/ws"; slug=t
  orca() { return 1; }                       # every orca call fails; no story worktree appears
  path="$(ensure_story_worktree repo missing)"; rc=$?
  printf "rc=%s path=[%s]\n" "$rc" "$path"' 2>/dev/null)"
echo "$out" | grep -q "rc=0" && { echo "FAIL: ensure_story_worktree returned 0 on creation failure"; fail=1; }
echo "$out" | grep -q "path=\[\]" || { echo "FAIL: ensure_story_worktree printed a fallback path: $out"; fail=1; }

# --- part 2: dispatch --start refuses to start the story and writes no allocation keys ---
e="$tmp/proj/epics/t"; mkdir -p "$e/stories" "$tmp/bin"
echo "api orca-repo" > "$e/repos"
printf -- '---\nid: t-a\nrepo: api\ndepends: []\nhost:\nagent: claude\nmodel: sonnet\ndevice: false\ntitle: t\n---\n' > "$e/stories/t-a.md"
cat > "$tmp/bin/orca" <<'SH'
#!/bin/bash
echo "$*" >> "$FAKE_LOG"
case "$2" in
  run-create|run-current) echo '{"result":{"run":{"id":"run_nf"}}}';;
  task-create) echo '{"result":{"task":{"id":"task_1"}}}';;
  worktree) exit 1;;                          # story worktree creation always fails here
  worker-list) echo '{"result":{"workers":[]}}';;
  *) echo '{"result":{}}';;
esac
SH
chmod +x "$tmp/bin/orca"
export PATH="$tmp/bin:$PATH" FAKE_LOG="$tmp/calls" ORCA_TERMINAL_HANDLE=term_test HOME="$tmp" ORCA_WORKSPACES="$tmp/ws"
: > "$FAKE_LOG"
out2="$("$src/dispatch.sh" "$e" --workers=local --start 2>&1)"
s="$e/.run"
grep -q "NOT STARTED" <<<"$out2" || { echo "FAIL: dispatch did not report NOT STARTED: $out2"; fail=1; }
grep -q "^dispatch.t-a=" "$s" 2>/dev/null && { echo "FAIL: a dispatch was recorded despite no worktree"; fail=1; }
grep -q "^env.t-a=" "$s" 2>/dev/null && { echo "FAIL: env allocation written despite no worktree"; fail=1; }
grep -q "^port.t-a=" "$s" 2>/dev/null && { echo "FAIL: port allocation written despite no worktree"; fail=1; }
grep -q "worker-start" "$FAKE_LOG" && { echo "FAIL: worker-start was called despite no worktree"; fail=1; }
pkill -f "watch.sh run_nf " 2>/dev/null || true; sleep 1
[ $fail = 0 ] && echo "ok: worktree-no-fallback (failed creation stops dispatch, no shared-checkout fallback, no allocation)"
exit $fail
