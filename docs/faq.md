# FAQ & troubleshooting

**`cox doctor` says a watcher is dead but a story is active.** Start it: `cox watch --epic <dir> --replace`. A dispatch
does not always leave a watcher alive.

**A worker is stuck or idle without finishing.** `cox control <story> interrupt|park|relaunch --epic <dir>`. A relaunch
injects the story's checkpoint.

**State looks wrong after an interrupted run.** `cox reconcile --epic <dir>` (dry run first) probes saved sessions and
confirms only the transitions it can prove; it never re-issues a side effect or removes a branch.

**Codex leader keeps asking for approval.** Codex has no Shift+Tab. Launch it with full access
(`codex -a never -s danger-full-access` or `--dangerously-bypass-approvals-and-sandbox`); the leader must act outside the
workspace sandbox to run the watcher and git.

**Where is state?** The append-only `.cox/events.jsonl` per epic. It is the source of truth; `cox state` and `cox board`
render it.
