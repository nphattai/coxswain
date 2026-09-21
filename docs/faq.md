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

**Where is state?** Two append-only logs per epic, merged by timestamp. Story lifecycle lives in the machine-local
`.cox/events.jsonl` (git-ignored, discarded on attach/close). Durable epic facts - `design_signed`, `design_amended` -
live in `<epic>/ledger.jsonl`, which is committed and travels with `git`, so a re-attached epic stays signed. `cox state`
and `cox board` render the merged history.

**`cox doctor` says signed/unsigned or `closed` unexpectedly.** doctor reads an epic's signed state from `ledger.jsonl`,
not the `Status:` line of `DESIGN.md`, and flags the two when they disagree (a re-attach that lost the signature).
An archived epic (`.cox.closed`, no `.cox`) prints as `closed`, not `active ... watcher dead`. doctor also fails when a
`workspace.json` repo path is missing or is not a git checkout, when a policy default harness has no adapter, and when a
second `cox` on `PATH` resolves to a different binary (a repeated PATH entry or a symlink to the same one is fine).

**The leader restarted and stopped getting wakes.** A leader is identified by the workspace it runs in, not by one Orca
pty handle, so a harness restart no longer orphans the epic: the first `cox hook prompt-drain` / `stop-rewake` from the
restarted terminal re-binds `<epic>/.cox/leader` to the new handle (when the recorded handle is no longer live and this
terminal is in the epic's workspace), the watcher reads `.cox/leader` fresh every tick and logs a failed leader doorbell
to `.cox/watch/log`, and `cox doctor` fails an active epic whose recorded leader handle is disconnected.

**Two leader terminals are open on the same workspace.** Only one leader may drive an epic, or a wake reaches the wrong
terminal. `cox doctor` fails an active epic when more than one connected Orca terminal in the workspace root runs the
leader harness, naming the recorded `.cox/leader` as the one to keep; `cox hook prompt-drain` prints the same warning
into the leader's turn context so it is seen without running doctor. Close the extra terminals and keep the recorded
`.cox/leader`.
