#!/bin/bash
# Live context of each worker of an epic, from Claude Code's session log (usage of the last assistant turn).
# Usage: bin/context.sh <project>/epics/<slug> [window_tokens=1000000]
# The flag is an ABSOLUTE budget (CTX_NOW, default 500k), not a share of the window: see bin/my-context.sh.
set -uo pipefail
. "$(dirname "$0")/lib.sh"
epic_paths "${1:?usage: context.sh <project>/epics/<slug> [window]}"; win="${2:-1000000}"
. "$(dirname "$0")/inbox-lib.sh"
for id in $(sed -n 's/^dispatch\.\([^=]*\)=.*/\1/p' "$epic_dir/.run" | sort -u); do
  grep -q "^done\.$id=" "$epic_dir/.run" && continue   # finished stories drop out
  alias="$id"; wt="$(story_worktree "$epic_dir" "$id" 2>/dev/null)" || continue; d="$HOME/.claude/projects/$(echo "$wt" | tr '/' '-')"
  f="$(ls -t "$d"/*.jsonl 2>/dev/null | head -1)"
  [ -n "$f" ] || { printf "%-10s no claude session log under %s\n" "$alias" "$d"; continue; }
  python3 - "$alias" "$f" "$win" "${CTX_NOW:-500000}" <<'PY'
import sys,json
alias,f,win,budget=sys.argv[1],sys.argv[2],int(sys.argv[3]),int(sys.argv[4]); last=None; n=0
for line in open(f):
    try: o=json.loads(line)
    except Exception: continue
    u=(o.get('message') or {}).get('usage')
    if u: last=u; n+=1
ctx=sum(last.get(k,0) for k in ('input_tokens','cache_read_input_tokens','cache_creation_input_tokens')) if last else 0
flag=' <- compact' if ctx>=budget else ''
print(f"{alias:<10} turns={n:<5} context={ctx/1000:6.0f}k  {ctx*100/win:3.0f}% of {win//1000}k{flag}")
PY
done
