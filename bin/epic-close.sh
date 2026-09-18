#!/bin/bash
# Tear down an epic's machine state on every host of the workspace (<ws>/hosts). Never deletes a branch (captain rule): every worktree is
# detached from its branch before `orca worktree rm`, so Orca has no checked-out branch to remove.
# Usage: bin/epic-close.sh <project>/epics/<slug> [--hosts=local,<remote>] [--complete] [--yes] [--force]
#   default   dry run: list what would happen
#   --yes     do it: stop watcher, stop+release workers, detach + remove worktrees (clean and pushed only),
#             remove symlinks, rename .run -> .run.closed
#   --complete  also set "Status: complete <today>, signed off by captain" in DESIGN.md (only on the captain's word)
#   --force   remove dirty/unpushed worktrees too (their uncommitted work is lost; branches still untouched)
#   --stories-only  epic stays open: only remove the per-story worktrees of stories marked done.<id> in .run
#             (no watcher/worker/task changes, epic worktrees and symlinks kept, .run untouched)
# Idempotent; bash 3.2; Orca terminal needed for the orchestration/worktree RPCs.
set -uo pipefail
. "$(dirname "$0")/lib.sh"
hosts="local${REMOTE:+,$REMOTE}"; yes=0; complete=0; force=0; stories_only=0; args=()
for a in "$@"; do case "$a" in --hosts=*) hosts="${a#--hosts=}";; --yes) yes=1;; --complete) complete=1;; --force) force=1;; --stories-only) stories_only=1;; *) args+=("$a");; esac; done
epic_paths "${args[0]:?usage: epic-close.sh <project>/epics/<slug> [--hosts=local,<remote>] [--complete] [--yes] [--force]}"
hosts_ok "$hosts" || { echo "bad --hosts=$hosts (local${REMOTE:+,$REMOTE})" >&2; exit 1; }
run_id="$(get run)"
say() { if ((yes)); then echo "DO   $*"; else echo "PLAN $*"; fi; }
do_() { ((yes)) && "$@"; return 0; }

# 1. watcher + workers + tasks
if [ -n "$run_id" ] && ((!stories_only)); then
  say "stop watcher for $run_id"; do_ pkill -f "watch.sh $run_id " ; do_ rm -rf "${TMPDIR:-/tmp}/watch-$run_id"
  for d in $(sed -n 's/^dispatch\.[^=]*=//p' "$state" | sort -u); do
    [ "$d" = "?" ] && continue
    say "worker-stop + worker-release $d"
    do_ orca orchestration worker-stop --dispatch "$d" --json >/dev/null 2>&1; do_ orca orchestration worker-release --dispatch "$d" --json >/dev/null 2>&1
  done
  for t in $(sed -n 's/^task\.[^=]*=//p' "$state" | sort -u); do
    st="$(orca orchestration task-list --run "$run_id" --json 2>/dev/null | jq -r --arg t "$t" '.result.tasks[]?|select(.id==$t)|.status')"
    [ "$st" = completed ] && continue
    say "task $t is '$st' -> failed (epic closed)"; do_ orca orchestration task-update --id "$t" --status failed --result "epic closed" --run "$run_id" --json >/dev/null 2>&1
  done
fi

# 2. worktrees: detach, then orca rm; refuse dirty/unpushed unless --force
close_wt() {  # host path
  local h="$1" wt="$2" sh env
  case "$h" in local) sh="";; *) sh="ssh -n $h"; env="--environment $h";; esac   # -n: never read the repos loop stdin
  $sh test -d "$wt" 2>/dev/null || { echo "skip $h $wt (absent)"; return; }
  local dirty unpushed
  dirty="$($sh git -C "$wt" status --porcelain 2>/dev/null | wc -l | tr -d ' ')"
  unpushed="$($sh git -C "$wt" log --oneline HEAD --not --remotes 2>/dev/null | wc -l | tr -d ' ')"   # commits on no remote ref at all (@{u} lies once the PR was rebased/merged elsewhere)
  if { [ "$dirty" != 0 ] || [ "$unpushed" != 0 ]; } && ((!force)); then
    echo "KEEP $h $wt: dirty=$dirty unpushed=$unpushed (commit+push, or --force to drop)"; return
  fi
  say "$h detach + orca worktree rm $wt (dirty=$dirty unpushed=$unpushed)"
  ((yes)) || return
  $sh git -C "$wt" switch -q --detach && orca worktree rm ${env:-} --worktree "path:$wt" --force --json >/dev/null \
    || echo "FAILED to remove $h $wt" >&2
}
while read -r alias repo; do
  ((stories_only)) && break
  [ -z "$alias" ] || [ "${alias#\#}" != "$alias" ] && continue
  for h in $(for x in ${hosts//,/ }; do norm_host "$x"; done | sort -u); do close_wt "$h" "$ws/$repo/epic-$slug"; done
  say "rm symlink $epic_dir/$alias"; do_ rm -f "$epic_dir/$alias"
done < "$epic_dir/repos"

# 2b. per-story worktrees (story/<id> branches stay; only the checkout is detached and removed)
for f in "$epic_dir"/stories/*.md; do
  [ -f "$f" ] || continue; id="$(fm "$f" id)"; repo="$(story_repo "$id")"; [ -n "$repo" ] || continue
  ((stories_only)) && [ -z "$(get "done.$id")" ] && { echo "skip $id (not done)"; continue; }
  for h in $(for x in ${hosts//,/ }; do norm_host "$x"; done | sort -u); do close_wt "$h" "$(story_wt "$repo" "$id")"; done
done

# 2c. story resources (database clones, simulator clones, env files) and the epic layer (backend, DB_NAME, snapshot)
for f in "$epic_dir"/stories/*.md; do
  [ -f "$f" ] || continue; id="$(fm "$f" id)"
  ((stories_only)) && [ -z "$(get "done.$id")" ] && continue
  for k in db sim env; do [ -n "$(get "$k.$id")" ] && { say "release $id ($k=$(get "$k.$id"))"; break; }; done
  do_ release_story "$id"
done
if ((!stories_only)) && load_epic_env 2>/dev/null; then
  say "local-env.sh down; drop databases $DB_NAME, ${DB_NAME}_tpl"
  do_ "$root/bin/local-env.sh" "$epic_dir" down; do_ db_drop "$DB_NAME"; do_ db_drop "${DB_NAME}_tpl"
fi

# 3. records
((stories_only)) && { ((yes)) || echo "(dry run; add --yes to execute)"; exit 0; }
[ -f "$state" ] && { say "mv .run -> .run.closed"; do_ mv -f "$state" "$state.closed"; }
if ((complete)); then
  say "DESIGN.md Status: complete $(date +%F), signed off by captain"
  do_ sed -i '' "s/^Status: .*/Status: complete $(date +%F), signed off by captain/" "$epic_dir/DESIGN.md"
fi
((yes)) && "$root/bin/hygiene.sh" | sed 's/^/hygiene: /'
((yes)) || echo "(dry run; add --yes to execute)"
