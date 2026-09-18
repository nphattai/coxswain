#!/bin/bash
# Bind this leader terminal to the epic's Run (create it once), create one task per story (deps wired), keep the
# watcher attached to THIS terminal, and start the workers that are ready.
# Usage: bin/dispatch.sh <project>/epics/<slug> [--start [--only <story-id>]] [--done <story-id>] [--workers=<host>|local]
#   (no flag)   ensure Run/tasks/watcher, print the worker-start lines (dry)
#   --start     also run worker-start for every story whose depends are all --done and that has no dispatch yet
#   --done ID   mark a story merged so --start can start its dependants (also completes its task); releases the story's
#               database clone, simulator clone and env file; a BACKEND story's --done prints the local-env.sh refresh step (D24)
# --start allocates per story (bin/lib.sh allocate_story): node_modules cloned from the epic worktree, <epic>/.env.<id>,
# a port block, and for BACKEND stories a database clone + redis prefix, for device: true stories a simulator clone.
# Default worker host: the workspace's remote host (<ws>/hosts) unless this leader runs on it, else local; a story's
# `host:` pins it. Must run inside a live Orca terminal. Idempotent: state in <epic>/.run (last value wins).
set -euo pipefail
. "$(dirname "$0")/lib.sh"
workers=""; start=0; done_id=""; only=""; args=()
while (($#)); do case "$1" in
  --workers=*) workers="${1#--workers=}";; --start) start=1;; --done) done_id="${2:?story id}"; shift;; --only) only="${2:?story id}"; shift;;
  *) args+=("$1");; esac; shift; done
epic_paths "${args[0]:?usage: dispatch.sh <project>/epics/<slug> [--start] [--done <id>] [--workers=<host>|local]}"
touch "$state"
workers="$(norm_host "${workers:-${REMOTE:-local}}")"
hosts_ok "$workers" || { echo "bad --workers=$workers (local${REMOTE:+|$REMOTE})" >&2; exit 1; }
: "${ORCA_TERMINAL_HANDLE:?run this from an Orca terminal}"

# 1. Run: create once, else rebind this terminal to it (a resumed leader has a new handle)
run_id="$(get run)"
if [ -z "$run_id" ]; then
  run_id="$(orca orchestration run-create --objective "epic $slug" --json | jq -r '.result.run.id')"; put run "$run_id"
elif [ "$(orca orchestration run-current --json 2>/dev/null | jq -r '.result.run.id // empty')" != "$run_id" ]; then
  orca orchestration run-use --id "$run_id" --json >/dev/null \
    || { echo "cannot rebind to $run_id; if the old leader terminal is gone: orca orchestration run-use --id $run_id --takeover-legacy" >&2; exit 1; }
fi
put leader "$ORCA_TERMINAL_HANDLE"; put host "$(hostname)"
echo "run=$run_id  leader=$ORCA_TERMINAL_HANDLE  workers=$workers"

# 2. tasks, topologically (depends -> --deps)
pending=("$epic_dir"/stories/*.md)
while ((${#pending[@]})); do
  progress=0; next=()
  for f in "${pending[@]}"; do
    id="$(fm "$f" id)"; [ -n "$(get "task.$id")" ] && continue
    deps="$(fm "$f" depends | tr -d '[] ')"; dep_ids=(); ok=1
    for d in ${deps//,/ }; do t="$(get "task.$d")"; [ -n "$t" ] && dep_ids+=("\"$t\"") || ok=0; done
    if ((ok)); then
      # one-line spec: the story file exists at the same path on every machine; a pasted document stalls TUIs
      spec="Your task is the story file $f - read it in full and follow its Working rules exactly."
      tid="$(orca orchestration task-create --run "$run_id" --spec "$spec" --task-title "$id" \
              --deps "[$(IFS=,; echo "${dep_ids[*]:-}")]" --json | jq -r '.result.task.id')"
      put "task.$id" "$tid"; progress=1
    else next+=("$f"); fi
  done
  pending=("${next[@]+"${next[@]}"}"); ((${#pending[@]})) || break
  ((progress)) || { echo "unresolved depends (cycle or unknown id): ${pending[*]}" >&2; exit 1; }
done

# 3. --done: story merged
if [ -n "$done_id" ]; then
  t="$(get "task.$done_id")"; [ -n "$t" ] || { echo "unknown story $done_id" >&2; exit 1; }
  out="$(orca orchestration task-update --id "$t" --status completed --run "$run_id" --json 2>&1)" && jq -e '.ok' <<<"$out" >/dev/null 2>&1 \
    || echo "  WARN: task $t not completed in Orca ($(jq -r '.error.message // "no json"' <<<"$out" 2>/dev/null)); dependants stay pending until: orca orchestration task-update --run $run_id --id $t --status completed"
  # the story is done: stop the watcher from flagging its dispatch as STALE
  d="$(get "dispatch.$done_id")"; [ -n "$d" ] && rm -f "${TMPDIR:-/tmp}/watch-$run_id/hb/$d" "${TMPDIR:-/tmp}/watch-$run_id/phase/$d" 2>/dev/null   # a story done without a dispatch (captain-driven) has no heartbeat file
  put "done.$done_id" "$(date +%s)"; echo "done: $done_id"
  release_story "$done_id"
  if load_epic_env 2>/dev/null && [ -n "${BACKEND:-}" ] && [ "$(fm "$epic_dir/stories/$done_id.md" repo)" = "$BACKEND" ]; then
    echo "backend story merged: run  bin/local-env.sh $epic_dir refresh  before starting the client wave (D24)"
  fi
fi

# 4. watcher attached to this terminal (restart if dead or attached to an old handle)
if ! pgrep -f "watch.sh $run_id " >/dev/null || [ "$(get watcher)" != "$ORCA_TERMINAL_HANDLE" ]; then
  pkill -f "watch.sh $run_id " 2>/dev/null || true; sleep 1
  nohup "$root/bin/watch.sh" "$run_id" "$ORCA_TERMINAL_HANDLE" "$epic_dir" >> "$epic_dir/.watch.log" 2>&1 &
  put watcher "$ORCA_TERMINAL_HANDLE"; echo "watcher started (pid $!), log $epic_dir/.watch.log"
fi

# 5. worker-start lines; run the ready ones with --start
echo; ((start)) && echo "# starting ready workers:" || echo "# workers (rerun with --start to launch the ready ones):"
newsims=0   # device stories allocated in this run: simctl boot is asynchronous, so the slot gate counts them too
for f in "$epic_dir"/stories/*.md; do
  id="$(fm "$f" id)"; repo="$(repo_of "$(fm "$f" repo)")"
  host="$(norm_host "$(fm "$f" host)")"; host="${host:-$workers}"; agent="$(fm "$f" agent)"; agent="${agent:-claude}"; model="$(fm "$f" model)"
  [ "$model" = opus ] && model=claude-opus-4-8   # captain ruling 2026-09-03: bare "opus" resolves to Opus 5; claude workers run Opus 4.8. Blank = ~/.claude/settings.json default (claude-opus-4-8)
  wt="$ws/$repo/epic-$slug"   # dry-run print default; a story this run actually starts gets a verified per-story worktree below
  if ((start)) && [ "$host" = local ] && [ -z "$(get "done.$id")" ] && [ -z "$(get "dispatch.$id")" ] && { [ -z "$only" ] || [ "$id" = "$only" ]; }; then
    # F02: no verified isolated worktree -> stop this story, never allocate against the shared epic checkout
    wt="$(ensure_story_worktree "$repo" "$id")" || { echo "  $id: NOT STARTED - no isolated story worktree (Orca worktree create failed); fix Orca and rerun, dispatch will not fall back to the epic checkout"; continue; }
  fi
  place="--worktree path:$wt"; [ "$host" != local ] && place="--on $host $place"
  cmd="orca orchestration worker-start --run $run_id --task $(get "task.$id") $place --agent $agent${model:+ --model $model} --timeout-ms 240000 --json"
  ready=1; for d in $(fm "$f" depends | tr -d '[] ' | tr , ' '); do [ -n "$(get "done.$d")" ] || ready=0; done
  if [ -n "$only" ] && [ "$id" != "$only" ]; then continue
  elif [ -n "$(get "dispatch.$id")" ]; then echo "  $id: running as $(get "dispatch.$id")"
  elif ((!ready)); then echo "  $id: waits for $(fm "$f" depends)"
  elif ((start)) && [ "$(fm "$f" device)" = true ] && [ -z "$(get "sim.$id")" ] && [ $(( $(sim_booted) + newsims )) -ge "$DEVICE_SLOTS" ]; then
    echo "  $id: waits for a device slot ($(sim_booted) booted + $newsims starting, DEVICE_SLOTS=$DEVICE_SLOTS; shut one down or raise DEVICE_SLOTS)"
  elif ((start)); then
    # the story is the worker's prompt: it must exist at the same path on the worker host (pull the project clone there)
    if [ "$host" != local ] && ! ssh -n "$host" test -f "$f"; then
      ssh -n "$host" git -C "$root" pull -q --ff-only 2>/dev/null
      ssh -n "$host" test -f "$f" || { echo "  $id: NOT STARTED - $f missing on $host; commit and push the epic dir, then rerun"; continue; }
    fi
    if [ "$host" != local ]; then echo "  $id: NOT STARTED - story allocation (env file, database, simulator) is local-only; run the leader on $host or pin 'host: local'"; continue; fi
    allocate_story "$id" "$f" "$wt" || { echo "  $id: NOT STARTED - allocation failed (see above)"; continue; }
    [ "$(fm "$f" device)" = true ] && [ -n "$(get "sim.$id")" ] && newsims=$((newsims + 1))
    if out="$($cmd 2>&1)"; then
      disp="$(orca orchestration worker-list --run "$run_id" --json | jq -r --arg t "$(get "task.$id")" '[.result.workers[]|select(.taskId==$t)]|last|.dispatchId // empty')"
      put "dispatch.$id" "${disp:-?}"; put "started.$id" "$(date +%s)"; put "wt.$id" "$wt"; echo "  $id: started on $host ($agent${model:+/$model}) -> ${disp:-?}"
    else echo "  $id: FAILED $(jq -r '.error.message // .result.failedStage // "see json"' <<<"$out" 2>/dev/null || echo "$out" | tail -1)"; fi
  else echo "  $id ($host, $agent${model:+/$model}): $cmd"; fi
done
