---
name: crewkit-dispatch
description: Dispatch an epic's stories as Orca orchestration workers and supervise them to PR - Run + task DAG from story frontmatter, one worker per repo, watcher-driven supervision, follow-ups to the warm worker. Use when the captain says "dispatch <slug>", "start the workers", or after crewkit-new.
---

# crewkit-dispatch

`$ROOT` = `git rev-parse --show-toplevel`. Run everything from this Orca terminal, preferably on the remote
host (`hosts` file). Protocol and recovery table: `$ROOT/docs/workflow.md` sections 4, 5, 6 and "Failure modes".

1. Commit and push the epic dir first (workers on the other host read the story from their clone). The epic
   layer must be up (`$ROOT/bin/local-env.sh <epic> smoke` green, snapshot present). Then
   `$ROOT/bin/dispatch.sh <project>/epics/<slug> --start` - Run (or rebind), tasks with `depends`, watcher
   on this terminal, allocation per story (`.env.<id>`, ports, database clone, simulator clone, node_modules),
   starts every story whose dependencies are done. Rerun any time; it is idempotent.
2. Wait for the watcher's `watch: ...` lines; do not write your own wait loop and do not call `check --wait`.
   - `question`: read the plan in the worker's worktree, `orca orchestration reply --id <msg_id> --body "..."`.
     Never answer a design question on the captain's behalf.
   - `question` "PR ready for review": audit the PR (`gh pr diff`; shas, verified-by, commands exist); reply with
     changes or `released`. The worker sends `worker_done` only after `released`.
   - `worker_done`: tell the captain; after the captain merges: `$ROOT/bin/dispatch.sh <epic> --done <story-id>`
     (releases the story's database clone, simulator and env file). If the story was the BACKEND story, run
     `$ROOT/bin/local-env.sh <epic> refresh` (ff-pull, build, migrate + seed, re-snapshot, restart) BEFORE
     `--start` launches the client wave (D24).
   - `STALE` / `escalation`: `orca orchestration worker-read --dispatch <id>`, then reply, send, or restart.
3. `$ROOT/bin/status.sh <project>/epics/<slug>` whenever the captain asks how it is going.
4. Follow-ups go to the same worker while it is alive: the reply to its question, or
   `orca orchestration send --to dispatch:<id> --subject "..." --body "..."`. After `worker_done` nothing reaches it.
5. All stories merged: captain decides; then `$ROOT/bin/epic-close.sh <epic>` (dry run), `--yes`, and
   `--complete` only on the captain's word. Ship per `<project>/docs/repos.md`.
