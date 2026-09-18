#!/usr/bin/env bash
# Recreate the machine-local symlinks of one epic dir from its `repos` file.
# Usage: bin/link.sh <project>/epics/<slug>   (run from the repo root, or pass an absolute path)
# Each `repos` line: <alias> <orca-repo-name>. Target: ~/orca/workspaces/<repo>/epic-<slug>.
set -euo pipefail
epic_dir="${1:?usage: link.sh <project>/epics/<slug>}"
epic_dir="$(cd "$epic_dir" && pwd)"
slug="$(basename "$epic_dir")"
ws="${ORCA_WORKSPACES:-$HOME/orca/workspaces}"
while read -r alias repo; do
  [[ -z "$alias" || "$alias" == \#* ]] && continue
  target="$ws/$repo/epic-$slug"
  [[ -d "$target" ]] || target="$(ls -d "$ws/$repo/epic-$slug"-* 2>/dev/null | tail -1)"   # Orca suffixes a recreated worktree (epic-<slug>-2)
  [[ -n "$target" && -d "$target" ]] || { echo "missing worktree: $ws/$repo/epic-$slug (create it with: orca worktree create --repo name:$repo --name epic-$slug)" >&2; continue; }
  ln -sfn "$target" "$epic_dir/$alias"
  echo "$alias -> $target"
done < "$epic_dir/repos"
