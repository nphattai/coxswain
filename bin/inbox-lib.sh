#!/bin/bash
# Per-story steering inbox (firstmate's fm-task-inbox model): durable records + a constant doorbell.
# Layout: <epic>/inbox/<story>/NNN.msg ; <epic>/inbox/<story>/handled/ (the worker's mv IS the ack) ; .ring-state
INBOX_GRACE="${INBOX_GRACE:-90}"; INBOX_RING_MAX="${INBOX_RING_MAX:-3}"
inbox_dir() { printf '%s/inbox/%s' "$1" "$2"; }
inbox_write() {  # <epic_dir> <story> <text> [urgency=steer|fyi]  -> prints record path; fyi records never trigger a runaway interrupt
  local d; d="$(inbox_dir "$1" "$2")"; mkdir -p "$d/handled"
  local n; n=$(ls "$d"/*.msg "$d"/handled/*.msg 2>/dev/null | sed -E 's#.*/([0-9]+)\.msg#\1#' | sort -n | tail -1); n=$((10#${n:-0} + 1))
  local rec; rec="$(printf '%s/%03d.msg' "$d" "$n")"
  { echo "schema=crewkit-inbox.v1"; echo "at=$(date -u +%FT%TZ)"; echo "urgency=${4:-steer}"; echo "--"; printf '%s\n' "$3"; } > "$rec.tmp" && mv "$rec.tmp" "$rec"; echo "$rec"
}
inbox_seen() {  # <worktree_path> <inbox_dir> <key> -> 0 when the worker's session log already shows it reading the record (cat/ls of the path)
  local f l; f="$(session_log "$1")"; [ -n "$f" ] || return 1
  grep -qF "$(basename "$2")/$3" "$f" && return 0
  l="$(sed -n '/^--$/{n;p;q;}' "$2/$3" | cut -c1-60)"; [ -n "$l" ] && grep -qF "$l" "$f"   # read through a glob (cat *.msg): the body shows up instead
}
inbox_doorbell() {  # <inbox_dir>
  printf 'Leader instruction waiting: list %s/*.msg and, in numeric order, read and act on each, then mv each handled file to %s/handled/. Then continue.' "$1" "$1"
}
session_log() { ls -t "$HOME/.claude/projects/$(echo "$1" | tr '/' '-')"/*.jsonl 2>/dev/null | head -1; }   # <worktree_path> -> newest claude session log
session_ctx() {  # <worktree_path> [window=1000000] -> "<ctx_tokens> <turns> <pct>" from the last assistant usage in the session log
  local f; f="$(session_log "$1")"; [ -n "$f" ] || { echo "0 0 0"; return 1; }
  python3 - "$f" "${2:-1000000}" <<'PY'
import sys,json
f,win=sys.argv[1],int(sys.argv[2]); last=None; n=0
for line in open(f):
    try: o=json.loads(line)
    except Exception: continue
    u=(o.get('message') or {}).get('usage')
    if u: last=u; n+=1
ctx=sum(last.get(k,0) for k in ('input_tokens','cache_read_input_tokens','cache_creation_input_tokens')) if last else 0
print(ctx, n, ctx*100//win)
PY
}
agent_state() {  # <worktree_path> -> Orca's structured agent state for that worktree (working|done|idle|...) or empty
  orca worktree ps --json 2>/dev/null | jq -r --arg p "$1" '.result.worktrees[]? | select(.path==$p) | .agents[0].state // empty' | head -1
}
session_idle() {  # <worktree_path> -> 0 when the claude session log ends on an assistant end_turn (TUI at the prompt)
  local d="$HOME/.claude/projects/$(echo "$1" | tr '/' '-')" f; f="$(ls -t "$d"/*.jsonl 2>/dev/null | head -1)"; [ -n "$f" ] || return 1
  tail -60 "$f" | python3 -c '
import sys,json
last=None
for l in sys.stdin:
    try: o=json.loads(l)
    except: continue
    m=o.get("message") or {}
    if o.get("type") in ("assistant","user") and m.get("role"): last=(o["type"], m.get("stop_reason"), m.get("content"))
if not last: sys.exit(1)
t,stop,c=last
tool_use = isinstance(c,list) and any(b.get("type")=="tool_use" for b in c)
sys.exit(0 if (t=="assistant" and not tool_use and stop in ("end_turn","stop_sequence",None)) else 1)'
}
inbox_ring() {  # <terminal_handle> <inbox_dir> [worktree_path] -> rings only when the worker is idle; prints the verdict
  # Orca's agents[] state is recovery-grade for claude/codex (firstmate: fm_backend_orca_probe), but a background shell
  # (a worker's own poll loop) keeps it at `working` while the TUI sits at the prompt - the session log settles that.
  local v="" st=""; [ -n "${3:-}" ] && st="$(agent_state "$3")"
  case "$st" in done|idle|stopped) v=empty;; working|busy|running) if [ -n "${3:-}" ] && session_idle "$3"; then v=empty; else v=busy; fi;; *) v="$("$(dirname "${BASH_SOURCE[0]}")/composer.sh" "$1")";; esac
  if [ "$v" = empty ]; then orca terminal send --terminal "$1" --text "$(inbox_doorbell "$2")" --enter --json >/dev/null 2>&1 && echo rang || echo ring-failed; else echo "skipped:$v"; fi
}
story_worktree() {  # <epic_dir> <story> -> the worker's worktree path (repo alias from the story frontmatter)
  local alias repo; alias="$(sed -n '/^---$/,/^---$/p' "$1/stories/$2.md" | sed -n 's/^repo:[[:space:]]*//p' | sed 's/[[:space:]]*#.*//')"
  repo="$(awk -v a="$alias" '$1==a{print $2}' "$1/repos")"; [ -n "$repo" ] || return 1
  local recorded; recorded="$(sed -n "s/^wt\.$2=//p" "$1/.run" | tail -1)"; [ -n "$recorded" ] && [ -d "$recorded" ] && { printf '%s' "$recorded"; return 0; }
  local ws="${ORCA_WORKSPACES:-$HOME/orca/workspaces}"; [ -d "$ws/$repo/story-$2" ] && printf '%s/%s/story-%s' "$ws" "$repo" "$2" || printf '%s/%s/epic-%s' "$ws" "$repo" "$(basename "$1")"
}
story_terminal() {  # <epic_dir> <story> -> terminal handle of the worker (cached in .run as term.<story>)
  local state="$1/.run" h; h="$(sed -n "s/^term\.$2=//p" "$state" | tail -1)"
  if [ -z "$h" ]; then local disp; disp="$(sed -n "s/^dispatch\.$2=//p" "$state" | tail -1)"; [ -n "$disp" ] || return 1
    h="$(orca orchestration worker-show --dispatch "$disp" --json 2>/dev/null | jq -r '.result.dispatch.assignee_handle // empty')"; [ -n "$h" ] && echo "term.$2=$h" >> "$state"; fi
  [ -n "$h" ] && echo "$h"
}
