#!/bin/bash
# What a worker is doing right now, from its Claude Code session log: last N actions (tool descriptions and
# prose) with timestamps, plus the log's last-activity time. Replaces reading the terminal or the whole jsonl.
# Usage: bin/worker-tail.sh <project>/epics/<slug> <story-id> [n=8]
set -uo pipefail
. "$(dirname "$0")/lib.sh"; . "$(dirname "$0")/inbox-lib.sh"
epic_paths "${1:?usage: worker-tail.sh <epic> <story-id> [n]}"; story="${2:?story-id}"; n="${3:-8}"
wt="$(story_worktree "$epic_dir" "$story")" || { echo "no worktree for $story" >&2; exit 1; }
d="$HOME/.claude/projects/$(echo "$wt" | tr '/' '-')"; f="$(ls -t "$d"/*.jsonl 2>/dev/null | head -1)"
[ -n "$f" ] || { echo "no session log under $d" >&2; exit 1; }
echo "$story  last-activity $(stat -f '%Sm' -t '%H:%M:%S' "$f")  ($(basename "$f"))"
python3 - "$f" "$n" <<'EOF'
import json, sys, datetime
lines = open(sys.argv[1]).read().splitlines(); n = int(sys.argv[2]); out = []
def local(ts):  # session log is UTC; print wall-clock
    try: return datetime.datetime.fromisoformat(ts.replace('Z', '+00:00')).astimezone().strftime('%H:%M:%S')
    except Exception: return ts[11:19]
for l in reversed(lines):
    try: j = json.loads(l)
    except Exception: continue
    if j.get('type') != 'assistant': continue
    for c in j['message'].get('content', []):
        if c.get('type') == 'tool_use':
            i = c['input']; out.append((local(j['timestamp']), c['name'], (i.get('description') or i.get('command') or i.get('file_path') or '')[:120]))
        elif c.get('type') == 'text' and c['text'].strip():
            out.append((local(j['timestamp']), 'text', c['text'].strip().replace('\n', ' ')[:120]))
    if len(out) >= n: break
for t, k, x in reversed(out[:n]): print(f"  {t} {k:<6} {x}")
EOF
