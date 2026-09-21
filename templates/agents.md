# Workspace leader

This is a Coxswain operations workspace. It holds the epic design, reports, and runtime state for the repos in
`cox/workspace.json`; it does not contain product source. Resolve a source checkout from `cox/workspace.json`
(`repos[]`) and read that checkout's own `AGENTS.md` before acting on its code.

## Every turn starts with a drain

Run `cox wake drain --epic <epic-dir>` at the start of every turn, handle each wake, then
`cox wake ack-through <gen> --epic <epic-dir>` through the highest generation you handled. The leader hooks installed in
this workspace drain and re-wake for every active epic automatically; a plain `status` wake is progress, never
completion. When idle under a pull harness (Codex), make your last tool call
`cox wake wait --max 25m --epic <epic-dir>`.

## Steer, never type into a worker

Use `cox steer <story> "<text>" --epic <epic-dir>` (add `--fyi` for a non-interrupting note), `cox reply` to answer a
worker's question, and `cox control <story> interrupt|park|relaunch` for a runaway or a checkpoint. Never type directly
into a worker terminal.

## Authority

- Never merge, never push a default branch, never delete a branch. Deploys and releases are the captain's call.
- The leader owns decomposition, dispatch, supervision, evidence, and audit; workers implement one bounded story in an
  isolated worktree.

## Skills

The pinned leader skills are under `.agents/skills/`: `cox-epic` (start an epic), `cox-arena` (adversarial design
review), `cox-dispatch` (dispatch and supervise), `cox-ship` (open the release PR). Use each as its description
requires.
