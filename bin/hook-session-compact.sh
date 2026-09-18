#!/bin/bash
# SessionStart(compact|resume) hook for WORKER sessions: after a compaction or a resume, put the story's handoff and its
# "Read first" block back into context (design 5.7). Finds the story from the cwd: a `wt.<id>=<cwd>` key in any
# <project>/epics/<slug>/.run of any workspace under ~/Work (a dir carrying the kit: crewkit/ or bin/lib.sh; D6), else the
# story worktree name `story-<id>`. Silent when not a worker. Meant to be registered machine-wide; stdout becomes session context.
cwd="$(pwd -P)"; id=""; epic=""
in_ws() { local w; for w in "$HOME"/Work/*/; do { [ -d "$w/crewkit" ] || [ -f "$w/bin/lib.sh" ]; } && ls "$w"*/epics/*/"$1" "$w"*/*/epics/*/"$1" 2>/dev/null; done; return 0; }   # <relative path> -> matches
for r in $(in_ws .run); do
  [ -f "$r" ] || continue
  k="$(grep -F "=$cwd" "$r" | sed -n 's/^wt\.\([^=]*\)=.*/\1/p' | tail -1)"
  [ -n "$k" ] && { id="$k"; epic="$(dirname "$r")"; break; }
done
if [ -z "$id" ]; then
  case "$(basename "$cwd")" in story-*) id="${cwd##*/story-}";; *) exit 0;; esac
  for r in $(in_ws "stories/$id.md"); do [ -f "$r" ] && { epic="$(dirname "$(dirname "$r")")"; break; }; done
  [ -n "$epic" ] || exit 0
fi
story="$epic/stories/$id.md"; hand="$epic/handoffs/$id.md"
echo "Context re-injected after compaction/resume for story $id."
if [ -f "$hand" ]; then echo; echo "== Your handoff ($hand):"; cat "$hand"; else echo "(no handoff file yet at $hand)"; fi
if [ -f "$story" ]; then echo; echo "== Read first (from $story):"; sed -n '/^## Read first/,/^## /p' "$story" | sed '$d'; fi
echo; echo "Env: $epic/.env.$id (load with: set -a; . <file>; set +a). Inbox: $epic/inbox/$id/. Then: list the inbox, run orca orchestration check --json, act on the mail, continue from the handoff's next step."
exit 0
