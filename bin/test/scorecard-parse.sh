#!/bin/bash
# Self-check for bin/scorecard.sh's parser on a fake epic: .watch.log with a midnight rollover (undated lines) plus one
# dated line, a fake .run, a handled inbox, and a 5-line fake session jsonl. Offline: no git, gh or orca.
# Run: /bin/bash bin/test/scorecard-parse.sh
set -u
here="$(cd "$(dirname "$0")" && pwd)"
t="$(mktemp -d "${TMPDIR:-/tmp}/scorecard-test.XXXXXX")"; trap 'rm -rf "$t"' EXIT
epic="$t/proj/epics/demo"; mkdir -p "$epic/stories" "$epic/inbox/demo-app/handled" "$t/ws/some-repo/story-demo-app"
printf -- '---\nid: demo-app\nrepo: app\n---\n' > "$epic/stories/demo-app.md"
echo "app some-repo" > "$epic/repos"
printf 'run=run_test\ndispatch.demo-app=ctx_a\nstarted.demo-app=1788300000\ndone.demo-app=1788310000\ntask.demo-app=task_x\n' > "$epic/.run"
cat > "$epic/.watch.log" <<'LOG'
23:50 question ctx_a msg_1  Question plan ready at plans/x, ok to implement? :: 
23:55 ring demo-app/001.msg -> skipped:busy ::
00:05 status ctx_a msg_2  phase 2 done :: pushed
00:10 question ctx_a msg_3  Question PR https://x/pull/42 ready for review; anything to change? :: 
2026-09-02 01:00 RUNAWAY demo-app/001.msg unread 700s while busy -> interrupted ::
LOG
touch -t 202609020130 "$epic/.watch.log"
printf 'schema=crewkit-inbox.v1\nat=x\nurgency=steer\n--\ndo this\n' > "$epic/inbox/demo-app/handled/001.msg"
printf 'schema=crewkit-inbox.v1\nat=x\nurgency=fyi\n--\nfyi\n' > "$epic/inbox/demo-app/handled/002.msg"
wt="$t/ws/some-repo/story-demo-app"; pd="$t/projects/$(echo "$wt" | tr '/' '-')"; mkdir -p "$pd"
cat > "$pd/s.jsonl" <<'JSONL'
{"type":"assistant","timestamp":"2026-09-01T16:50:00Z","message":{"usage":{"input_tokens":10,"output_tokens":100}}}
{"type":"system","subtype":"compact_boundary","timestamp":"2026-09-01T17:00:00Z","compactMetadata":{"trigger":"manual","preTokens":600000,"postTokens":20000}}
{"type":"pr-link","timestamp":"2026-09-01T17:05:00Z","prNumber":42,"prRepository":"org/repo"}
{"type":"system","subtype":"turn_duration","timestamp":"2026-09-01T17:06:00Z","durationMs":130000}
{"type":"assistant","timestamp":"2026-09-01T17:10:00Z","message":{"usage":{"input_tokens":10,"output_tokens":250}}}
JSONL
out="$(cd "$t" && SCORECARD_OFFLINE=1 SCORECARD_PROJECTS_DIR="$t/projects" ORCA_WORKSPACES="$t/ws" /bin/bash "$here/../scorecard.sh" proj/epics/demo --json)" || { echo "FAIL: scorecard exited non-zero"; exit 1; }
fail=0
chk() { local got; got="$(jq -r "$1" <<<"$out")"; [ "$got" = "$2" ] || { echo "FAIL: $1 = $got (want $2)"; fail=1; }; }
chk '.schema' crewkit-scorecard.v2
chk '.stories|length' 1
chk '.stories[0].wall_s' 10000
chk '.stories[0].plan_to_ready_s' 1200          # 23:50 -> 00:10 across midnight = 20 min
chk '.stories[0].questions' 2
chk '.stories[0].runaway' 1                     # the dated line
chk '.stories[0].rings_busy' 1
chk '.stories[0].steers' 1
chk '.stories[0].fyi' 1
chk '.stories[0].phases' 2
chk '.stories[0].phase_source' subject
chk '.stories[0].compactions|length' 1
chk '.stories[0].compactions[0].pre_tokens' 600000
chk '.stories[0].out_tokens' 350
chk '.stories[0].turns' 2
chk '.stories[0].max_turn_s' 130
chk '.stories[0].pr.number' 42
chk '.stories[0].commits' null                  # not a git repo -> absent source, not an error
table="$(cd "$t" && SCORECARD_OFFLINE=1 SCORECARD_PROJECTS_DIR="$t/projects" ORCA_WORKSPACES="$t/ws" /bin/bash "$here/../scorecard.sh" proj/epics/demo)"
grep -q '^STORY ' <<<"$table" && grep -q '^app ' <<<"$table" || { echo "FAIL: table"; echo "$table"; fail=1; }
none="$(cd "$t" && rm "$epic/.run" && /bin/bash "$here/../scorecard.sh" proj/epics/demo)"; grep -q "no .run" <<<"$none" || { echo "FAIL: no-.run path"; fail=1; }
[ $fail = 0 ] && echo "ok: scorecard parser (rollover, inbox, jsonl, absent git)"
exit $fail
