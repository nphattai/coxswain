# Orca backend adapter

The default backend (decision 0002). Wraps the exact `orca` CLI commands v1 drives from `bin/`, always with `--json`,
parsing each response into a typed struct and surfacing every Orca error as a real Go error - never `|| true` on the
data path (0001, F02). Source: `internal/adapter/backend/orca/`.

## Capability card

| Capability | Orca | Notes |
|---|---|---|
| roles | leader, worker | supervised workers via orchestration run/task |
| worktree create | yes | `orca worktree create`; path + branch re-verified by `worktree.Ensure`, no fallback (F02) |
| worktree remove | yes | detach-first: `git switch --detach` then `orca worktree rm --force`; branch survives (F01), see below |
| spawn | yes | `task-create` + `worker-start`; dispatch resolved via `worker-list`, terminal via `worker-show` |
| send (doorbell) | yes | knocks on the terminal: `terminal read --screen` -> classify composer -> `terminal send --text ... --enter` only when empty (v1 composer.sh); falls back to `orchestration send --to dispatch:<id>` with no handle. Doorbell only, not the durable steer channel; returns rang only on a real terminal delivery. The read passes `--screen` so it classifies the rendered composer rows (prompt + status line), not the truncated raw stream tail, which would classify unknown and never ring. classifyComposer drops trailing footer rows (the `⏵⏵ ... (shift+tab to cycle)` permissions hint current Claude Code renders under the status line) before reading the last row, so the status line is found and an idle worker classifies empty |
| interrupt | yes | `terminal send --terminal <handle> --interrupt` (v1 bin/interrupt.sh); errors on an empty handle |
| stop | yes | `orchestration worker-stop`; `confirmed=true` only on `ok=true`, else ownership kept (F03/F04/F05) |
| probe | yes | `orchestration worker-read`, `.result.status.liveness`; alive/settled/unknown, never inferred gone (F08) |
| mailbox | yes | `orchestration check --run <run>` (run-scoped batch read, replays until ack) + `check --ack` (explicit ack) + `send` + `reply` |
| remote hosts | yes (not wired in M1) | Orca supports `--on <host>`; M1 does not use it |

`cox doctor` / `cox story dispatch` refuse a role, or run a documented reduced mode, when it needs a capability marked
reduced above - never a silent weaker guarantee (P9).

## Observed behavior (2026-09-15)

Recorded from `orca agent-context --json` (234 commands, schema v1) and live `--json` calls on this machine.

### Response envelope

Every `--json` response is `{ "ok": bool, "result": {...}, "error": { "message": ... } }`. The adapter treats a
non-zero exit, invalid JSON, or `ok:false` as an error; only `ok:true` yields a result.

- `orca worktree show --json` -> `.result.worktree.{path, branch}`; `branch` is fully-qualified
  (`"refs/heads/v2/m1"`), so the adapter strips the `refs/heads/` prefix.
- `orca worktree list --json` -> `.result.worktrees[]` with the same worktree shape.

### Probe: `worker-read` liveness

`orca orchestration worker-read --dispatch <id> --json` reports `.result.status.liveness`; v1's `bin/watch.sh` reads
the value `"live"`. Mapping (`mapLiveness`): `live`/`alive`/`running` -> Alive; `settled`/`closed`/`done`/`completed`/
`failed`/`stopped`/`exited` -> Settled; empty or unrecognized on an otherwise-ok read -> Unknown **with an error**
(F08: never infer gone). A call error or invalid JSON is likewise Unknown with an error. The full liveness enum is not
published in `agent-context`; the settled set above is the observed/expected vocabulary and is widened as new values
are seen, never guessed into Alive.

### Spawn: `worker-start` returns no dispatch

`orca orchestration worker-start --run <r> --task <t> --worktree path:<p> --agent <a> --json` only acknowledges the
request: its result is `{ "stage": "input_accepted" }` with no dispatch id or terminal handle. So `Spawn` resolves them
after the fact, exactly as v1 does:

- Dispatch id: `orchestration worker-list --run <r> --json` -> `.result.workers[]` of `{ dispatchId, taskId,
  workerState, agentTerminalHandle, ... }`; take the **last** entry whose `taskId` matches the task just created (v1
  `bin/dispatch.sh` line 104). No match is a real error. `WorkerList` returns every row as `{ Dispatch, State, Handle }`
  from these fields; `workerState` is the dispatch state (`ready|running|succeeded|failed|abandoned`), and `cox migrate`
  keeps a session only for `ready|running`.
- Terminal handle: `orchestration worker-show --dispatch <id> --json` -> `.result.terminal.handle` (v1
  `bin/interrupt.sh`). The handle is best-effort - empty or an unreadable show leaves `Session.Handle` empty rather than
  failing the spawn, since only `Interrupt` needs it.

### Mailbox: `check` (why not `inbox`)

Two different readers, confirmed from `agent-context`, v1 `bin/watch.sh`, and live `--json` calls:

- `orchestration check [--run <r>] --json` is the **run-scoped FIFO-batch reader**: it returns `.result.count`,
  `.result.deliveryId`, and `.result.messages[]`, and **replays the same delivery** on every call until
  `--ack <delivery_id>`. A plain `check` (no `--wait`, no `--peek`) returns the current batch immediately without
  advancing it, so a read alone never consumes mail.
- `orchestration inbox [--terminal <h>] --json` is a **read-only listing** - it returns `.result.messages[]` for every
  run on the machine, never acks, and carries **no delivery id**.

`Mailbox.Check` uses **`check --run <run>`**, not `inbox`. Both are non-consuming on read, but `check` returns a real
delivery id, so the watcher can Check, write every wake durably, then `Ack` the delivery id (`check --ack`). Acking
drains Orca's own delivery queue, which is what stops Orca re-ringing the terminal doorbell ("You have N orchestration
messages. Run `orca orchestration check`") at the leader - the earlier `inbox` path never acked, so that bell never went
quiet. `check --run` is already run-scoped, but `Check` still filters each message to `to_handle == "run:<run>"` as a
guard, errors on a blank Run, and `Ack("")` is a no-op. A message's `payload` arrives as a JSON **string**, kept
verbatim in `Message.Payload` for the caller to parse (structured end to end, F10).

Message shape observed: `{ id, run_id, from_handle, to_handle, subject, body, type, payload (JSON string), read (0/1),
sequence, created_at }`.

### Worktree removal deletes branches - detach first

`agent-context` note on `worktree rm`: "For Git worktrees, removal also attempts to delete the checked-out local
branch, with or without `--force`. Orca retains branches it knows predated the worktree and any branch whose changes it
cannot prove are already merged." A fresh story branch is neither, so a naive `orca worktree rm` would delete it, which
violates the never-delete-a-branch contract (0002, F01).

`WorktreeRemove` uses v1's proven sequence (`bin/epic-close.sh` `close_wt`): `git -C <path> switch --detach`, then
`orca worktree rm --worktree path:<path> --force --json`. With HEAD detached there is no checked-out branch for Orca to
delete, so the branch survives. If the detach fails, `rm` is **not** called - removing a still-attached worktree is
exactly what would destroy the branch. Verified by a unit test over a real temp repo: the story branch is still listed
after removal.

### Interrupt

`Interrupt` sends the interrupt to the worker's terminal, the way v1 does (`bin/interrupt.sh`):
`orca terminal send --terminal <handle> --interrupt --json`. It breaks a worker out of a runaway turn (a capture loop, a
rabbit hole) so it re-reads its inbox. It addresses the terminal handle resolved at spawn (`worker-show`), so a Session
with no handle is a real error rather than a silent no-op.

## Two planes: orchestration and terminal (ADR 0012)

`backend.orca.plane` selects how the adapter drives Orca. **orchestration** (the pre-M10 path, above) uses
`task-create` + `worker-start`, the `orchestration check` mailbox, `worker-stop`, and the coordinator doorbell.
**terminal** uses worktrees and terminals only, and cox owns the whole handoff (report/reply/wake); no `task-create`,
`worker-start`, mailbox, `worker-stop`, run binding, or completion cap runs. The default is `orchestration` until one
live E2E and one live story pass on claude and codex, then it flips to `terminal` (decision 4); the orchestration path
is removed one milestone later by its own ADR.

Terminal-plane command shapes, verified live on this machine (Orca CLI 1.4.197):

- **Spawn** = `orca terminal create --worktree path:<wt> --title <story> --json` (`.result.terminal.{handle, paneKey,
  tabId}`), then `orca terminal send --terminal <handle> --text <launch> --enter --json` where `<launch>` is
  `COX_EPIC=.. COX_STORY=.. COX_PLANE=terminal <harness> --model <id> <approval flags> '<prompt>'`. The model is always
  typed (captain ruling: workers run claude-opus-4-8) and the approval flags come from policy `harness.launch.<name>`
  (claude `--permission-mode bypassPermissions`, codex `-a never -s workspace-write`); the launch line is built by the
  shared `backend.LaunchLine` so orca and herdr cannot drift. It waits up to the confirm window (policy
  `backend.orca.launch_confirm_s`, default **60s**; `COX_SPAWN_CONFIRM` overrides) for either an agents[] entry to appear
  for the pane (any state - the harness registered with Orca) OR the composer to go busy. The 60s/agents[] window
  replaces the old 20s composer-only one, which warned on cold harness starts that were in fact running (M10b). An
  unconfirmed launch is logged, not fatal (the durable handle plus the watcher's liveness own detection - there is no
  pending_external signal out of Spawn, since the Backend interface returns only `(Session, error)`). A `terminal send`
  failure closes the orphan terminal and fails the spawn. Session is `{orca-terminal, <handle>, <handle>}`.
- **Probe** = `orca terminal show --terminal <handle> --json` (`.result.terminal.{connected, exitCause, tabId,
  leafId}`), then correlate the pane key `tabId:leafId` against `orca worktree ps --json`
  `.result.worktrees[].agents[]` by `paneKey`. A stale handle (`ok:false` code `terminal_handle_stale` /
  `tab_not_found`), a disconnected terminal, or an `exitCause` is **Settled**; an agents[] `state` of
  `working`/`idle`/`waiting` is **Alive**, `dead`/`exited` is **Settled**; a connected terminal with no agents[] entry,
  or an unreadable `worktree ps`, is **Unknown**, never gone (decision 3). `waiting` is Alive because a worker blocked on
  a local approval/input prompt is alive, not gone (M10b); the block is surfaced by the Composer/watcher path, not by
  liveness. A real anonymized `worktree ps` fixture lives in `internal/adapter/backend/orca/testdata/worktree-ps.json`.
- **Composer / blocked (M10b).** On the terminal plane `Composer` first reads the pane's agents[] state: a `waiting`
  agent is reported as `blocked` (a codex "Would you like to run the following command?" or any local approval/input
  prompt a dispatched worker cannot answer). Text classification (`bin/composer.sh`) cannot recognize a codex approval
  prompt, so the structured state is the reliable signal; a non-waiting agent falls back to the text tail. The watcher's
  blocked pass turns a worker that stays `blocked` longer than 2m into an urgent `stuck` wake ("worker waiting on a local
  prompt (approval or input); check the terminal"), once per waiting interval. Ringing uses the private text composer, so
  the doorbell path is unchanged.
- **Stop** = `orca terminal close --terminal <handle> --json`, confirmed when a follow-up probe reads Settled; an
  unconfirmed close keeps ownership (F03/F04/F05).
- **Mail** = reduced (Check empty, Ack no-op, Send no-op, Reply refused); the watcher's only wake source is
  `wake.jsonl`, written by `cox story report`.

### agents[] is populated only for harnesses Orca tracks

`worktree ps` agents[] carries an entry only for a harness Orca recognizes through a per-harness agent hook. Verified
against Orca CLI 1.4.197: claude populates agents[] with `{paneKey, state, agentType}` (see the captured fixture). Codex
must be verified live: if `worktree ps` reports no agents[] for a codex worker, Probe falls back to Unknown and the
composer classifier (`Composer`) is the liveness signal. **This is left as a live check when the plane flips**; record
the result and the Orca version here (per-harness agent-hook coverage can change between Orca releases).

### Reaping smoke (open item)

Orca might auto-close or idle-reap a terminal it created via `terminal create` (as opposed to a dispatched worker's
terminal). Before the plane becomes default, run a 30-minute idle smoke: spawn a terminal-plane worker, leave it idle,
and confirm `terminal show` still reports it `connected` after 30 minutes. If Orca reaps it, record the behavior here
and open an Orca issue; cox's liveness would see the reaped terminal as Settled and could end a live story early.

## Contract tests

Live tests that need a real Orca run go behind `//go:build orca` and are not part of the default `go test ./...`. The
adapter's parsing and liveness mapping are unit-tested with an injected command runner (`orca_test.go`), no Orca
required.

## Live behaviors discovered in the M3 E2E (2026-09-15)

Two real-Orca behaviors the fake did not model, found running `tests/e2e/dispatch-live.sh` on this machine (Orca CLI
1.4.197):

### Branch naming: username prefix + slash flattening

`orca worktree create --name <name>` does NOT create a branch equal to `<name>`. Orca derives the branch from the
worktree name by prefixing the repo's git username and flattening slashes: a requested `epic/e2e-1` became
`nphattai/epic-e2e-1` (the repo's `gitUsername` is `nphattai`, from `orca repo show`). There is no create flag to set
the exact branch. cox addresses branches by their canonical names everywhere (`epic/<slug>`, `story/<id>`), so
`WorktreeCreate` puts the worktree on the requested name right after creation. When that branch does not exist yet it
renames the mangled branch to it (`git -C <path> branch -m <requested>`). When it already exists - a parked story whose
worktree was removed without deleting the branch (F01), then resumed into a fresh worktree - `git branch -m` refuses
(exit 128), so `WorktreeCreate` runs `git -C <path> switch <requested>` instead. Either way a rename/switch is not a
delete (F01), and `worktree.Ensure` re-verifies the branch afterwards. Confirmed live: epic new produced canonical
`epic/e2e-<ts>` and, on dispatch, `story/e2e-hello`.

A switch lands on the existing branch at its **old HEAD**, not the requested base (M5: an arena role reviewed `d770a2a`
while the pack said `cd1e383`). So after switching, `WorktreeCreate` brings the reused branch to base (A11): it resets
`--hard` to base only when `git rev-list --count <base>..<branch>` is `0` (the branch has no commits missing from base,
so nothing is lost). A branch that is ahead of base is **refused**, not reset - discarding its commits would be exactly
the data loss F01 guards against - with guidance to merge or remove the branch, or remove its worktree, then retry.

**Ceiling (ponytail):** the switch path leaves the orphaned mangled branch (`nphattai/story-e2e-hello`, ...) behind
rather than deleting it - the never-delete-a-branch contract (F01) forbids cleanup here. These leftovers accumulate one
per resume-onto-existing; an operator prunes them by hand with `git -C <repo> branch -D nphattai/<...>` once the real
canonical branch is confirmed. Automating the prune would mean a tool deleting a branch, which F01 rules out.

### Dispatch depth limit: max 1

`orca orchestration worker-start` refuses to launch a worker from inside another worker's session:

```
"Sub-worker dispatch is not permitted at depth 2 (max 1). Complete this task yourself."
```

A session dispatched as an Orca worker (depth 1) cannot itself dispatch a supervised worker (depth 2). Consequence for
cox: `cox story dispatch` (and `cox story resume`, which spawns) can only run from a depth-0/1 context - the captain's
terminal or the leader session - not from within a worker. The `dispatch-live.sh` E2E therefore must be run from the
coordinator/leader context, not from a dispatched worker. Everything up to and including `cox epic new` (worktree
create + the branch rename above) runs at any depth.

### One worker_done per dispatch

Orca accepts exactly one `worker_done` per dispatch. A second `worker_done` on the same dispatch is still delivered but
flagged rejected: it arrives with subject `Rejected worker_done: <original subject>` and a body explaining the dispatch
capability is revoked. So a re-run (a worker handling a follow-up steer in the same dispatch) must NOT send a second
`worker_done`; it reports completion as `orca orchestration send --type status --subject "done: <summary>"`. The watcher
classifies a `done:` status as a completion (`wake.Classify`) and strips the `Rejected worker_done:` prefix from a
worker_done note (`wakeNote`), so the leader reads the real summary either way.

### One run per coordinator terminal (run-use fencing)

`orca orchestration run-current` shows the run the coordinator terminal is bound to (`result.run` is `null` when
unbound); `orca orchestration run-use --id <run>` binds it. A terminal binds exactly one run at a time, so a mutation
issued against a run the terminal is not bound to is fenced:

```
consumer_fenced
```

This bit us live in M5: an E2E run launched from the leader terminal rebound it to the E2E run, and the next
`cox epic arena` on epic v2 failed `consumer_fenced` (the story arena-adversary was left `pending_external`). Fix: the
adapter now wraps the four run-scoped mutations - `task-create`, `worker-start`, `worker-stop`, and `reply` - in
`Client.mutate`, which reads `run-current`, rebinds to the epic's `.cox/run` only when it differs, runs the mutation,
then restores the previous binding so the leader terminal is left as it was. Reads (`worker-read`, `worker-list`,
`check`, `terminal read`) are not wrapped and pay no extra round-trips.

Ceiling: the rebind is best-effort. cox runs synchronously in the terminal it rebinds, so within one command the
binding is stable, but nothing prevents another process rebinding the same terminal between cox's `run-current` and its
mutation. If `run-current`/`run-use` are unavailable or fail, the mutation is attempted on the current binding as
before (an unreadable `run-current` is treated as "not ours", so mutate still tries to bind).

### Live fact (2026-09-15, 1.4.197)
`orca worktree ps --json` reports `agents[]` for both claude and codex workers spawned on the terminal plane: `state`
(working|idle|...), `agentType` (claude|codex), the launch prompt, and the current tool call. Verified on a codex
smoke story (agentType codex, state working while `go test` ran). Absence of an entry is still `unknown`, never gone.
The spawn warning "launch not confirmed busy within 20s" fired for both harnesses while the worker was in fact running;
liveness owned detection as designed, but the 20s composer window is too short for a cold harness start. **Fixed in
M10b:** the confirm window is now 60s (policy `backend.orca.launch_confirm_s`) and confirms as soon as an agents[] entry
appears for the pane, not only on a busy composer. The same smoke also showed codex launched with no flags stuck at
"Would you like to run the following command?" until the leader pressed Enter; M10b types `--model` and the policy
approval flags (`harness.launch.<name>`) onto the launch line, and the agents[] `waiting` state now raises a `stuck`
wake instead of hanging silently.

### Live fact (2026-09-16, 1.4.197): idle terminals are not reaped
A terminal cox created with `terminal create` and left idle for 30 minutes after the agent finished its turn was still
present (`terminal show` ok) and `worktree ps` reported `agents[].state = "done"`. Orca reaps only dispatches it owns.
`done` is a fourth agent state next to working, idle, and waiting: the agent process is alive and idle; cox should
treat it as Alive (a mapping gap at the time of writing).

### Live fact (2026-09-16, 1.4.197): `terminal show` answers ok for closed terminals
After `terminal close`, `orca terminal show --terminal <h>` still returns `ok:true` with the old title and no status
field; only `terminal list` (membership) and `worktree ps` agents[] (absence after a prior entry) reflect that the
terminal is gone. Stop confirmation must not rely on `terminal show` alone.
