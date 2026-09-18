#!/bin/bash
# One table for an epic: story, host, agent, task status, dispatch, last heartbeat age + phase, PR, merged marker.
# Usage: bin/status.sh <project>/epics/<slug>     (reads <epic>/.run, orca task-list/worker-list, the watcher state dir, gh)
set -uo pipefail
. "$(dirname "$0")/lib.sh"; . "$(dirname "$0")/inbox-lib.sh"
epic_paths "${1:?usage: status.sh <project>/epics/<slug>}"
run_id="$(get run)"; [ -n "$run_id" ] || { echo "no Run yet (run bin/dispatch.sh first)"; exit 0; }
tasks="$(orca orchestration task-list --run "$run_id" --json 2>/dev/null || echo '{}')"
workers="$(orca orchestration worker-list --run "$run_id" --json 2>/dev/null || echo '{}')"
hb="${TMPDIR:-/tmp}/watch-$run_id"; now=$(date +%s)
echo "run $run_id  leader $(get leader)@$(get host)  watcher $(pgrep -f "watch.sh $run_id " >/dev/null && echo up || echo DOWN)"
printf '%-30s %-6s %-12s %-10s %-17s %-9s %-10s %-5s %-8s %s\n' STORY HOST AGENT TASK DISPATCH HEARTBEAT PHASE CTX PUSHED PR
for f in "$epic_dir"/stories/*.md; do
  id="$(fm "$f" id)"; t="$(get "task.$id")"; disp="$(get "dispatch.$id")"
  host="$(fm "$f" host)"; agent="$(fm "$f" agent)"; model="$(fm "$f" model)"
  tstat="$(jq -r --arg t "$t" '.result.tasks[]?|select(.id==$t)|.status' <<<"$tasks")"
  [ -n "$disp" ] || disp="$(jq -r --arg t "$t" '[.result.workers[]?|select(.taskId==$t)]|last|.dispatchId // empty' <<<"$workers")"
  age="-"; phase="-"
  if [ -n "$disp" ] && [ -e "$hb/hb/$disp" ]; then age="$(( (now - $(stat -f %m "$hb/hb/$disp")) / 60 ))m"; phase="$(cat "$hb/phase/$disp" 2>/dev/null || echo -)"; fi
  pr="-"; ctx="-"; pushed="-"; repo="$(repo_of "$(fm "$f" repo)")"; wt="$(story_wt "$repo" "$id")"; [ -d "$wt" ] || wt="$ws/$repo/epic-$slug"
  if [ -d "$wt" ]; then
    # the story's own PR (head = story/<id>): draft or ready, state; last push = committer time of origin/story/<id>
    pr="$(cd "$wt" && gh pr list --head "story/$id" --state all --json number,state,isDraft,url --jq '.[]|"#\(.number) \(if .isDraft then "draft" else (.state|ascii_downcase) end) \(.url)"' 2>/dev/null | head -1)"; pr="${pr:--}"
    t="$(git -C "$wt" log -1 --format=%ct "origin/story/$id" 2>/dev/null)"; [ -n "$t" ] && pushed="$(( (now - t) / 60 ))m"
    [ -z "$(get "done.$id")" ] && read -r _ _ p < <(session_ctx "$wt") && ctx="${p}%"
  fi
  [ -n "$(get "done.$id")" ] && pr="$pr [done]"
  printf '%-30s %-6s %-12s %-10s %-17s %-9s %-10s %-5s %-8s %s\n' "$id" "${host:-auto}" "${agent:-claude}${model:+/$model}" "${tstat:--}" "${disp:--}" "$age" "$phase" "$ctx" "$pushed" "$pr"
done
