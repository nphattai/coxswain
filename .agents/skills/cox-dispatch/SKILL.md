---
name: cox-dispatch
description: Dispatch an epic's stories as workers and supervise them to PR - one worker per repo, watcher-driven wake, audit before the captain sees a PR, release then done. Use when the captain says "dispatch <slug>", "start the workers", or after cox-epic.
---

# cox-dispatch

Port of crewkit-dispatch to the `cox` binary. One worker per repo (policy default); more than one only when `files_owned` are disjoint.

## 1. Dispatch
Commit and push the epic dir. If the epic runs a backend, `cox env smoke --epic <epic>` must be green first. Then per story (respecting the wave DAG - backend before its clients):
```
cox story dispatch <id> --epic <epic>
```
This verifies the worktree, builds the brief context, spawns the worker, records the transition, and starts the watcher.

A story's `## Read first` block references workspace paths through the tokens `{{.EpicDir}}` and `{{.WorkspaceDir}}`, not an absolute path, so the story file never hard-codes one machine's layout. `cox checkpoint inject` (which the worker runs on launch and every resume) expands them to this machine's paths, so a workspace that has moved still resolves correctly - do not sed absolute paths into a story.

## 2. Supervise via wake
The leader loop is identical for claude and codex: drain the watcher's wakes, handle each, ack through. The only
difference is how an idle leader waits for the next wake, and the harness capability card (`docs/adapters/<harness>.md`)
decides it - the loop body does not change:
- **Push** (claude, or codex once `cox workspace hooks --harness codex` is installed and trusted): the `Stop` hook waits
  while idle and opens a new turn when a wake arrives, so you do not run `cox wake wait` by hand - just end your turn.
- **Pull** (a harness with no hooks, or a codex leader before its hooks are installed): end the turn with
  `cox wake wait --max 25m` so the process blocks until a wake or timeout.
```
cox wake drain --epic <epic>        # peek/pop the queue
cox wake wait --max 25m --epic <epic>   # pull harnesses only; push harnesses sleep on the Stop hook
```
Handle each: a `question`/`input_required` -> read the plan, then reply. ON THE TERMINAL PLANE the wake carries `evidence.question=qNNN`: `cox reply <story> qNNN "<answer>" --epic <epic>` (add `--again` for a second reply). ON THE ORCHESTRATION PLANE: `cox reply <msg-id> "<text>" --epic <epic>`. A `pr_ready` question -> audit (step 3), reply; `worker_done` -> tell the captain, the captain merges, then release (step 4); `stale` -> `cox control <story> interrupt|park|relaunch`. A `stuck` wake now carries the worker's on-screen prompt in its note and `evidence.prompt` (item 2): the worker is waiting on a local dialog cox cannot answer, so recover from the hook output without opening the terminal - `cox steer <story> "<ruling>" --epic <epic>`, then dismiss the prompt from the worker's terminal (`orca terminal send --enter`, or the option number). Workers ask ONLY through `cox story report question` + `cox question wait` (orchestration plane: `orca orchestration ask`), never through a harness dialog (AskUserQuestion, permission prompt, `/ask`) - it is invisible to cox. Ack processed wakes: `cox wake ack-through <gen> --epic <epic>`.

## 3. Audit before the captain sees a PR
```
cox audit pr <story> --epic <epic> --json
```
Three-state bundle bound to the head sha: shape, credential scan, CI, reviewer threads. Never mark a PR ready on an `unknown` (exit 3) or a `fail` (exit 1). Resolve every reviewer-bot comment first. A head that moved since the last audit prints `stale` - re-audit.

## 4. Release and done
Only the captain merges, and `cox ship merge --pr <n> --epic <epic>` is the command the captain runs - it merges only an open, non-draft, mergeable PR green at the live head, pins the head, reads it back, and records a `merged` event in `ledger.jsonl`. It is refused from a worker terminal and, while `merge.yolo` is false (the default), unless `--captain`; `--check` is a read-only dry run the leader may run to preview the verdict. After the captain merges a story PR into `epic/<slug>`: reply `released` to the worker, which then sends `worker_done`. Release the story's resources and stop its worker:
```
cox env release <story> --epic <epic>
```
Refresh the epic backend if a merged story changed it (`cox env refresh --epic <epic>`).

A **scout** story (`kind: scout`) has no PR: its deliverable is `<epic>/reports/<id>.md`. `cox audit pr` and `cox state` print `kind=scout report=<path|missing>` for it (no forge call), and `cox story done` refuses to complete it until the report lands. When a scout's findings become work, promote it: `cox story promote <id> --epic <epic> --mode <m>` flips it to a ship story, appends the superseding delivery contract to the story, and prints the `cox steer` command to deliver it - you send that steer to the warm worker; the command never sends it for you.

## 5. Follow-ups and status
Follow-ups (steer) go to the same warm worker via the durable inbox (`cox steer <story> "<text>" --epic <epic>`), `--fyi` for a non-interrupting note. `cox state --epic <epic> --json` is the fleet view for the captain. A follow-up steer re-runs the worker: it MUST end with a completion signal. ON THE ORCHESTRATION PLANE Orca allows one `worker_done` per dispatch, so the first completion is `worker_done` (listing the commits) and every later completion in the same dispatch is `orca orchestration send --type status --subject "done: <3-line summary>"` - the watcher classifies a `done:` status as a completion and advances the story. ON THE TERMINAL PLANE there is no cap: the worker sends `cox story report done` every time (no `done:` convention). A plain progress `status` never advances the story, so a re-run that ends with only a plain `status` leaves the leader waiting; the watcher raises an `idle_no_done` wake when that happens.

## 6. Close
All stories merged and the captain says close: `cox epic close --epic <epic>` (dry run first, then `--yes`); ship per `cox-ship`.

## Captain rulings (verbatim, from docs/workflow.md)
- 2026-09-02 Only the captain merges - every PR, any size. The leader audits, looks at frames, marks ready, presents. Not to be proposed again.
- 2026-09-02 Workers LOOK at every frame they capture; a capture script saves a frame only when its flow exited 0. The earlier "no PNG" rule is withdrawn.
- 2026-09-02 Infra is never a worker: the local backend is leader-run; a merged worker is released.
- 2026-09-02 Release is not a story: shipping is the Ship phase (epic/<slug> -> staging branch -> production branch).
- 2026-09-02 Obviously synthetic fixture values in a local-only flow on an evidence branch stay; real credentials, tokens, OTP request ids, seed login ids and anything that resolves on staging or prod are banned everywhere. Creds files live outside every repo, env NAMES only in artifacts.
- 2026-09-02 send --fyi records never interrupt a worker; an automation that interrupts must prove "unread", not infer it.
- 2026-09-02 Evidence behind a third-party grader goes through one leader-scheduled stand-in window for that hop only, captioned; workers never run a stand-in.
- 2026-09-02 PR comments, including the repo's bot reviewer, must be resolved before the leader marks a PR ready; the audit prints them.
- 2026-09-02 One leader terminal + session per project; lifecycle (open, close, follow-up epics) is captain-owned.
- 2026-09-02 ak is the skill standard for every agent; memory is files in git.
- 2026-09-08 Only the captain merges a PR, story PRs into the epic branch included; the leader never runs gh pr merge.
- 2026-09-08 Context budgets raised to 400k (plan) / 500k (now); compaction at 200k wasted time.
- 2026-09-07 Workflow v3 signed (D1-D25): tests are the evidence; layer waves (backend first, depends gates clients); scout phase 0 per repo; one container set per machine, database per backend story; budgets absolute.
- 2026-09-07 A worker's environment is the env file dispatch wrote; it never creates a device, port, container or database and never migrates anything but its own DB_NAME.
