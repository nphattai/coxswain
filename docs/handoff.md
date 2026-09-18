# Handoff: how a leader and a worker hand work to each other

Everything goes through disk, never through conversation memory. Leader and worker do not talk to each other; they
leave files for each other under `<epic>/`, and a terminal only ever receives a knock that says "go read the file".
Any session can die, compact, or restart and pick up from disk with nothing lost. This page is the map; the contracts
are in `docs/protocol/`, the code in `internal/protocol`, `internal/wake`, `internal/watch`, and the decisions in
`docs/decisions/`.

## Five channels, one job each

| Channel | Direction | Carrier | Ack |
|---|---|---|---|
| **brief** | leader -> worker | `stories/<id>.md` (the verbatim prompt) + `briefs/<id>/context.md` (paths, sha, attempt, rulings). Replayed on relaunch. | none needed |
| **steer** | leader -> worker | `inbox/<id>/NNN.msg`. Written under a per-inbox lock, to a uniquely named temp file, published by atomic rename. Budget: 5 steers per story; `--fyi` records are free and never interrupt. | worker `mv`s the file into `handled/`; the move is the ack |
| **control** | leader -> worker | Verb allowlist: `interrupt`, `park`, `relaunch`. Never free text. | the adapter confirms |
| **status** | worker -> leader | `cox status <phase> "note"` (or `cox story report status`): one event plus a wake. A log, not the state. | none |
| **report** | worker -> leader | `cox story report status\|done\|stuck --note ...`: a status event plus a wake (`status`/`worker_done`/`stuck`). Plane-independent; on the terminal plane it replaces the Orca completion mail (no `worker_done` cap). Binds `COX_STORY`, refuses another story. | none |
| **question/reply** | worker <-> leader | `cox story report question --body ...` allocates `questions/<id>/qNNN.md` and an `input_required` wake; the leader answers with `cox reply <id> qNNN "<answer>"` (answer file + budget-exempt inbox steer + ring); the worker blocks on `cox question wait qNNN --max` (exit 3 = timeout). | worker `mv`s q + answer into `handled/` |
| **checkpoint** | worker -> its future self | `handoffs/<id>.md` with frontmatter (`story`, `attempt`, `head`, `base`, `reason`) and nine sections: intent, constraints, decisions, failed attempts, verified outcomes, current state, outstanding steering, next action, open questions. | the resume hook checks freshness |

Schemas: `coxswain.inbox.v1`, `coxswain.checkpoint.v1`, `coxswain.event.v1`, `coxswain.wake.v1`, `coxswain.fleet.v1`, `coxswain.question.v1`.

### Report and question/reply belong to cox, not the backend (ADR 0012)

On the terminal plane, no backend mailbox, template, heartbeat, or completion cap takes part in the handoff. The worker
reports straight into the epic with `cox story report`, and the file-based question/reply channel replaces the Orca
message-id reply that the terminal plane never creates: a question is a durable `qNNN` under `questions/<id>/`, the reply
is `qNNN.answer.md`, and the worker's `cox question wait` never blocks inside a backend call (timeout exits 3 so it
checkpoints and parks). Both planes share the `questions/<id>/` layout, so a story migrated between planes keeps its
history. The orchestration-plane completion (`worker_done`, then a `done:` status re-run) and `cox reply <msg-id>` stay
behind `backend.orca.plane: orchestration`.

## State is derived; external effects must be confirmed

The source of truth is `<epic>/.cox/events.jsonl`, append-only. A story's state is the fold of that log and can be
rebuilt at any time (`snapshot.json` is only a cache). Every verb with an external side effect (stop a worker, spawn
one) writes two events: `working -> pending_external {intended_to: parked}` first, then the adapter call, then
`pending_external -> parked` only when the adapter confirmed. If the process dies between the two, `Reconcile` reads
`intended_to`, probes what already happened, and finishes the transition without repeating the side effect. Park also
refuses to park blind: a checkpoint with the current attempt and head must exist, otherwise it sends a
"PARK: write your checkpoint" steer and waits.

`pending_external` means a side effect is in flight and unconfirmed, so ownership is not cleared. Every write of it
carries `evidence.intended_to` (the state it is reaching) - `state.Append` refuses one without it, since `Reconcile`
would have no target. A story stuck there (a crash between the two appends) is finished by the reconciler, not by hand.

## Ending a story: done, fail, cancel

A story leaves the machine into a terminal state only through a command; the leader never hand-edits the log.

- **`cox story done <id>`** - `working|input_required -> completed`. Records `--merge <sha>` as evidence when the epic
  branch was merged.
- **`cox story fail <id> --reason "<why>"`** - `working|input_required|parked -> failed`. For a story that cannot be
  completed (the harness gave up, an unfixable blocker). `--reason` is required and recorded as `evidence.reason`.
- **`cox story cancel <id> --reason "<why>"`** - `working|input_required|parked -> canceled`. For a story dropped from
  scope, not attempted to completion. `--reason` is required.

All three share the release path: append the terminal event, best-effort `Stop` the worker, remove the session record,
and with `--close-worktree` detach and `WorktreeRemove` the worktree - which **keeps the git branch** (F01), always.
They refuse a worker whose composer is observed busy (it is still running) unless `--force`; a parked story has no live
turn, so it is never blocked.

**Re-dispatch after cancel or fail is a fresh attempt.** A `cox story dispatch` of a story whose last state is
`canceled` or `failed` runs at attempt N+1, the same bump a parked story gets on relaunch, so the new `submitted ->
working` events never collide with the terminated attempt (the scorecard keeps them apart). Every other state keeps the
current attempt, so a live `cox status` or checkpoint during a working turn still writes for the attempt it belongs to.

## Reconcile: finishing a pending_external story

`cox reconcile --epic <dir>` walks every story stuck in `pending_external` and finishes the ones a probe can confirm.
For each it reads `intended_to`, probes the saved session, and decides purely from the probe: an alive session with
`intended_to: working` is adopted as working; a gone (settled) session confirms the intended terminal state, or fails a
dispatch that never established; an unknown probe is **kept** (never inferred gone, F08). Dry-run (the default) prints
the table and writes nothing; `--apply` writes only the confirmed transitions. The watcher runs one `--apply` pass
every N ticks (policy, default 10) with the same confirm-only rule, so a crash mid-effect self-heals within a window.
Reconcile never re-issues a side effect and never removes a branch.

## Wake loop: the leader never polls

`cox watch` runs in the background and costs no tokens. It reads the mailbox of its own run, classifies mail into wakes
(`question`, `input_required`, `pr_ready`, `worker_done`, `stuck`, `runaway`, `stale`, `unknown_probe`, `status`),
appends them to `.cox/wake.jsonl` with an increasing generation, and only then acknowledges the delivery. It also runs
the ring ladder: a steer unhandled for 90 seconds gets a doorbell typed into the worker terminal when its composer is
empty; three rings with no ack raise a `stuck` wake; a worker busy for 30 minutes with an unread steer is interrupted
once per window. A failed liveness probe becomes an `unknown_probe` wake and keeps the heartbeat; it never concludes
the worker is gone.

A dispatched worker that hits a local approval or input prompt it cannot answer (a codex "Would you like to run the
following command?", any TUI confirmation) shows up as the backend agent state `waiting` - Alive, not gone. When a
working story stays blocked on such a prompt for more than 2 minutes, the watcher raises an urgent `stuck` wake ("worker
waiting on a local prompt (approval or input); check the terminal"), once per waiting interval, so the leader unblocks
it instead of the story stalling silently. cox also launches workers with the policy approval flags
(`harness.launch.<name>`) so they run autonomously and do not stop for these prompts in the first place (M10b).

Wakes reach the leader by one of two paths, chosen from the harness capability card (`docs/adapters/<harness>.md`):

- **Push** (Claude Code, and Codex once its hooks are installed): the `UserPromptSubmit` hook attaches unread wakes to
  the turn; the `Stop` hook waits while idle and opens a new turn: urgent wakes at once, routine wakes batched for 300
  seconds, and after 55 minutes with an open story a single tick so the next turn re-arms the waiter. Codex 0.154 has the
  same hook events; `cox workspace hooks --harness codex` installs them into a project-level `.codex/hooks.json` and the
  hook commands carry `--harness codex` so `cox hook` emits codex's stdout block decision where claude exits 2
  (docs/adapters/codex.md). `cox doctor` shows the codex card as `wake=push` once they are installed.
- **Pull** (a harness with no hooks): `AGENTS.md` makes the leader run `cox wake drain` at the start of every turn,
  handle, then `cox wake ack-through <gen>`; when idle, the last tool call is `cox wake wait --max 25m`, which blocks
  until a wake arrives. This is the fallback for any harness without hooks; a codex leader whose hooks are not yet
  installed (or not yet trusted) runs this way, with the watcher's backend doorbell as the safety net.

The push hooks are leader-only: `prompt-drain` and `stop-rewake` no-op unless this terminal's `ORCA_TERMINAL_HANDLE`
matches `<epic>/.cox/leader` (written by `cox story dispatch`), so a worker whose worktree happens to carry the repo's
own `.claude/settings.json` or `.codex/hooks.json` never drains or acks the leader's wakes. When `.cox/leader` is absent
the guard keeps the old behavior.

A worker signals completion with `worker_done`. Orca allows only one `worker_done` per dispatch, so on a re-run (a
follow-up steer) the second and later completions come as a `status` whose subject starts `done:`; the watcher
classifies a `done:` status as a `worker_done` wake and clears `idle_no_done`. A plain progress `status` never
completes a story.

## When the leader sees a wake status after a steer follow-up

After steering a worker that had already sent `worker_done`, the next wake from it is a `status` (never a second
`worker_done`). The status subject is only a phase label and reads like a story name, so it misleads on its own. Read it
in three steps:

1. **Read the body, not just the subject.** `cox wake drain --full` prints the untruncated status body; the default
   drain caps it at 200 characters. The body is where the worker says what it actually did with the steer.
2. **Check the composer to tell idle from working.** `cox state <story>` prints a `composer` column (`empty` | `pending`
   | `busy` | `unknown`). `empty` means the worker finished its turn and is idle; `busy` means it is still mid-turn;
   `unknown` is never read as idle. This is also why `cox story done` refuses a `busy` worker (it is still running) - pass
   `--force` only when you mean to complete it anyway.
3. **`done:` is completion; a plain status is progress.** A status whose subject starts `done:` is the re-run's
   completion (the watcher already treats it as a `worker_done` wake and clears `idle_no_done`). Any other status is
   progress: the worker is still working, so do not mark the story done. If the worker went idle without a `done:` and a
   steer is still unanswered, the watcher raises an `idle_no_done` wake so the story is not left silently stalled.

## Quota wakes (M11, observe-only)

The watcher also polls quota every 5 minutes (through the 60s cache, under a cross-process lock) for the leader harness
and every working story's harness, and appends two wake kinds the leader drains like any other:

- `quota_low` - the harness is at or near exhaustion. It is **urgent** (rings the leader doorbell now) on `exhausted_now`
  or a percent below `quota.low_percent`, and **routine** on a `projected_exhaustion` inside `quota.min_runway_hours`. It
  fires once per `(harness, resetsAt)` window.
- `quota_health` - the automatic source (quota-axi) stayed unknown for two consecutive polls (dependency lost,
  Keychain revoked, schema drift), including from the first observation. Routine, at most once per harness within
  `quota.health_debounce_minutes` (default 60); a Known reading resets the streak but not the debounce.

Quota never re-routes a running worker or picks a harness by itself (ADR 0011). When a harness runs low the leader parks
the story and resumes it on the other harness by hand: `cox story park <id>` then `cox story resume <id> --harness
<other>` (attempt N+1, checkpoint injected, `evidence.reroute` recorded). `cox story dispatch` refuses to send new work
to an `exhausted_now` harness unless `--force-quota` is passed. See [`docs/quota.md`](quota.md).

## Restart is a non-event, proven by tests

`tests/integration/interrupt_scenarios_test.go` runs three interruptions on both the claude and codex adapters: before
the worker wrote a checkpoint, after an external effect but before its event, and in the middle of a steer. The fresh
session must keep its constraints, not repeat a failed attempt, and not repeat a side effect. Alongside: 40 concurrent
steers keep 40 files, a failing probe yields `unknown` rather than "gone", a wrong-attempt checkpoint is refused, and a
parked story never makes the idle rearm open a turn.

## Borrowed from firstmate, added on top

Borrowed: the data/control plane split, the sequence-locked inbox with ack-by-move, the generation-stamped wake queue,
the Stop hook with `asyncRewake`, and relaunch in the same worktree with a progress note. Added: a checkpoint schema
bound to attempt and head, `pending_external` with `intended_to`, a resolver that answers `unknown`, and a
model-agnostic harness with push and pull delivery.

## Day-to-day commands

```
cox story dispatch <id> --epic <dir>          # brief + worktree + spawn + event
cox story done|fail|cancel <id> --epic <dir>  # terminal transition (fail/cancel need --reason)
cox reconcile --epic <dir> [--apply]          # finish pending_external stories a probe confirms
cox steer <id> "text" [--fyi] --epic <dir>    # durable steer, doorbell
cox control <id> interrupt|park|relaunch      # verbs only
cox status <phase> "note"                     # worker side
cox checkpoint facts | inject                 # worker side, or via the claude hooks
cox wake drain [--peek] | ack-through <gen> | wait --max 25m
cox watch --epic <dir>                        # zero-token supervisor
cox state --epic <dir> --json                 # coxswain.fleet.v1 view
```
