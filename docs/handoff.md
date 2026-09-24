---
hide:
  - toc
---

<span id="handoff-how-a-leader-and-a-worker-hand-work-to-each-other" aria-hidden="true"></span>

# Handoff

Coxswain treats handoff as durable coordination, not conversation history. A terminal may receive a knock, but the
instruction, acknowledgement, or result always has a disk-backed owner under the epic directory.

<span id="five-channels-one-job-each" aria-hidden="true"></span>

## Channel map

| Channel | Direction | Purpose | Acknowledgement |
|---|---|---|---|
| Brief | leader to worker | Establish the immutable story contract and resolved context for an attempt | Replayed on relaunch |
| Steer | leader to worker | Amend execution within accepted scope | Worker moves the inbox record to `handled/` |
| Control | leader to worker | Interrupt, park, or relaunch through an allowlisted verb | Backend confirmation |
| Status/report | worker to leader | Record progress, completion, or a blocker | Durable event and wake |
| Question/reply | both directions | Pause for a decision and preserve its answer | Worker moves the question and answer to `handled/` |
| Checkpoint | worker to future session | Preserve intent, constraints, verified progress, and next action | Freshness check on resume |
| Wake | watcher to leader | Announce that durable state needs attention | Leader acknowledges through a generation |

<figure class="cox-diagram">
  <div class="cox-diagram__surface">
    <picture>
      <source media="(max-width: 640px)" srcset="assets/diagrams/handoff-channels-mobile.svg">
      <img src="assets/diagrams/handoff-channels.svg" alt="Leader-to-worker, worker-to-leader, and worker-to-future-self channels stay separate and durable.">
    </picture>
  </div>
  <figcaption>Each direction has a distinct record and acknowledgement. The terminal carries only the wake-up signal. <a href="assets/diagrams/handoff-channels.svg">Open full size</a></figcaption>
</figure>

The record formats and executable owners are indexed in [Protocol model](protocol/index.md). The CLI entry points are
grouped by task in [CLI map](reference/cli.md).

## Directionality is part of the safety model

Workers report facts and ask questions. Leaders steer, control, and reply. Neither side edits the other side's record
in place. This keeps authority visible and makes an interrupted write distinguishable from an acknowledged handoff.

Free text is allowed for human meaning in a brief, steer, status, or answer. Lifecycle control uses a closed verb set,
so a message cannot accidentally become an unreviewed state transition.

## Delivery is not acknowledgement

A terminal ring only says that new durable work may exist. It does not prove the agent read or acted on the record.
Each channel therefore has its own acknowledgement rule. The watcher can safely ring again when a steer remains
unhandled because the inbox record, not the ring, owns delivery.

This distinction is why handoff belongs to Coxswain rather than to an orchestration mailbox. A backend can lose a
doorbell or be replaced without losing the instruction. See [ADR 0012](decisions/0012-handoff-belongs-to-cox-backends-provide-terminals.md).

## Idle/busy is harness-owned

Whether a worker or leader is idle or busy is a fact the harness reports, not something a backend infers from a TUI. A
harness whose card sets `BusyRecord` (Claude and Pi; Codex only behind policy `harness.busy_verified`) arms a per-story
record at `<epic>/.cox/sessions/<story>.busy.json` at dispatch (incarnation gen threaded as `COX_BUSY_GEN`). Claude
reports through worker hooks cox writes into the worktree's `.claude/settings.local.json` (the per-checkout,
not-committed settings slot, excluded via `info/exclude` when not already gitignored, so the story worktree stays clean)
(`UserPromptSubmit` -> busy, `Stop` -> idle, `SessionEnd` -> retire, via `${COX_BIN:-cox} busy apply|retire ... || true`);
Pi reports through its extension. Every backend ring/composer path and the watcher's idle/blocked passes consult this record FIRST and fall back
to the backend's own signal only when the harness reports `unknown`.

Each capability card carries a `BusySources` trust table (its own hook source plus the leader-side `dispatch`,
`interrupt`, `recovery` writers). `busy.Arm` stamps the harness and its trusted sources on the record; an Apply from a
source the card does not list is rejected, and a record whose source is untrusted reads as `unknown` - a record a harness
did not write never classifies its story. A stale gen is rejected (a hook that outlived its incarnation), and
`busy.Retire` removes the record exact-gen so a `SessionEnd` or a leader release never clobbers a newer incarnation. A
working story whose record stays busy past `BusyTurnMax` (60 min, policy `watch.busy_turn_max_min`) with no fresh event
or checkpoint raises one routine `status` wake - a nudge, never an interrupt. This closes the gap where a backend derived
busy from a UI it did not recognize (dogfood F-A). `internal/protocol/busy/` owns the record; `cmd/cox/busy.go` is the
harness-neutral CLI.

## Runtime records are versioned by attempt

`sessions/<story>.json`, `wt/<story>`, and `.cox/leader` are written by temp + rename. The session and worktree records
carry the `attempt` that wrote them; a write whose attempt is lower than the one already on disk is dropped, so a
straggler from a prior incarnation (after a relaunch bumped the attempt) never clobbers the newer record. `.cox/leader`
is a JSON record `{handle, pid, ts}`; the single reader `state.LeaderHandle` accepts both it and the legacy plain-handle
text, and every reader (hooks, watcher, epic close, doctor) routes through it. The steer budget likewise counts only the
current attempt: `inbox.Write` ignores records older than the attempt's dispatch, and `cox steer` reports the older ones
as history rather than refusing a fresh steer (B-23).

## Interrupt through the harness when the backend cannot

Interrupt is an allowlisted control verb delivered by the backend keystroke. When a harness's TUI ignores that keystroke
(its card sets `BackendInterrupt: false`, e.g. Pi 0.86.1, dogfood F-C), `cox control interrupt` still sends the
keystroke as the fallback AND delivers a durable `interrupt` inbox record; the harness's own extension aborts the
running turn when it sees that record (Pi: `ctx.abort()`). The interrupt remains a `working->working` audit event, never
a state transition.

## Questions are decisions, not chat

On the terminal plane, a worker creates a numbered question and waits for its matching answer. The leader's reply also
produces a budget-free inbox record so an idle worker is rung through the normal path. `internal/protocol/question/`,
`cmd/cox/report.go`, `cmd/cox/reply.go`, and their tests own the mechanics.

There is currently no published JSON Schema file for `coxswain.question.v1`. The Go type and tests are authoritative;
[Protocol model](protocol/index.md) tracks this gap explicitly.

## Recovery model

### Session restart

A new session receives the brief and a fresh checkpoint. It must not reconstruct constraints from chat. Harness cards
declare whether checkpoint capture is automatic or manual; [Adapters](adapters/index.md) routes to those contracts.

### Unconfirmed side effect

If a process stops between recording intent and confirming an external call, the story remains `pending_external`.
Use `cox reconcile` to probe and complete only a provable transition. Never hand-edit the event log.

### Unanswered steer

The watcher rings only when the worker can receive input. Repeated lack of acknowledgement becomes a `stuck` wake.
An unknown composer or liveness result remains unknown rather than being treated as idle or gone.

### Worker blocked on a local prompt

A worker waiting on a local prompt (an approval or an input request it cannot answer itself) is alive, not gone, so the
blocked pass, not liveness, surfaces it. After the block persists past the blocked window the watcher raises a `stuck`
wake that carries the worker terminal's on-screen prompt: the question and its numbered options are captured from the
screen (never the environment, so no secret is included) into the wake note, its full text, and `evidence.prompt`. The
leader answers from the hook output without opening the terminal - `cox steer <story> "<ruling>"`, then dismiss the
prompt from the worker's terminal (`orca terminal send --enter`, or the option number). Workers never ask through a
local harness dialog; that channel is invisible to cox, so a question always goes through `cox story report question`.

### Worker completion after a follow-up

A plain status is progress, not completion. A completion signal must follow the active backend plane's contract. The
watcher classification in `internal/wake/classify.go` and integration tests own the exact compatibility behavior.

### No turn ends blind (the turn-boundary guard)

Before the leader waits, the Stop and `session-start` hooks verify that every led epic with an open story has a live,
fresh watcher: `.cox/watch.pid` names a live process and `watch/lasttick` is younger than three tick intervals. A dead
watcher is restarted (a detached `cox watch --epic <dir>`). A restart that cannot take over - a live-but-wedged watcher,
or a launch error - reopens the turn with the exact repair line `cox watch --epic <dir> --replace`, bounded by a
per-turn block budget (3) so a broken watcher can never wedge the leader: once the budget is spent the turn ends loudly
instead. The guard sees an epic even when its watcher has died, so the failure that would hide the epic is the one it
fixes. See [ADR 0014](decisions/0014-turn-boundary-guarded.md).

A watcher whose epic dir, `.cox` tree, or own binary has vanished - or whose epic has a `.cox.closed` marker - evicts
itself, and `cox doctor` lists any live `cox watch` process whose epic is outside every known workspace.

### Registered checks and the cycle ledger

`cox watch check register <id> --epic <dir>` binds `<epic>/.cox/<id>.check.sh` (a private 0700 file) to its bytes. The
watcher then runs it, from a snapshot of exactly those bytes and in its own environment, every `COX_CHECK_INTERVAL`
seconds (default 300), each run bounded by `COX_CHECK_TIMEOUT` seconds (default 30). Non-empty output is an urgent
`check` wake; a check whose bytes drifted is never run and is reported instead (firstmate's check sweep). Each watcher
cycle's close - the watcher's own exit, or the stop-rewake waiter attached to it - is one record in
`<epic>/.cox/watch-cycle-exits.log`, capped by `COX_WATCH_CYCLE_LOG_MAX_BYTES` (default 262144) and
`COX_WATCH_CYCLE_LOG_KEEP_LINES` (default 1000). A signalled waiter records `reason=arm-interrupted` and exits 128+n.

### Leader reachability and the alerts channel

The watcher nudges the leader terminal for a standing unacked urgent backlog, rate-limited so an unchanged backlog is
re-nudged at most once per window (no nudge storm). When the doorbell fails three times in a row the leader is
unreachable: the watcher raises one `_leader` stuck wake, `cox doctor` raises an ISSUE, and, when `policy.alerts.channel`
is set, one out-of-band notification fires per 30 minutes. `alerts.channel` is `off` (default), `osascript` (a macOS
banner), or `command:<cmd>` (runs `<cmd>` via `sh -c` with the alarm summary as `$1` and on stdin, for a phone or
pager). See [Policy JSON](reference/policy-json.md#alerts).

## Merge authority is the captain's, enforced in code

The captain merges everything; a leader or worker never merges, pushes a default branch, or deletes a branch. `cox ship
merge --pr <n> --epic <dir>` is the single merge command, so the green-at-the-live-head rule is enforced rather than
remembered: it reads the PR live through the forge, merges only an open, non-draft, mergeable PR on the epic (or
production) branch whose every check is green at the live head, pins that head (a push between the read and the merge is
rejected), reads the result back, and appends a `merged` event to `ledger.jsonl` (`evidence: {pr, head, method, by}`). It
is refused from a worker terminal (`COX_STORY` set) and, while `merge.yolo` is false, refused unless `--captain`. `--check`
is a read-only dry run. Exit codes: `0` merged, `1` refused (every failing reason listed), `3` unknown.

Each story's **delivery mode** (`delivery.mode`, printed in the brief as `Delivery contract: mode=<mode> yolo=<on|off>`)
sets the posture: `no-mistakes` (full gates + PR + wait for merge authority), `direct-PR` (push + PR, the default), or
`local-only` (clean ready branch, no push, wait). `cox story done --merge <sha>` refuses a sha that is not landed on the
branch the mode requires (`origin/epic/<slug>`, or the production branch for `local-only`).

## Leader session start: `cox bearings`

A leader session starts from one digest, not from a hand-written handoff file. `cox bearings` (run by the
`session-start` hook; `--reemit` on a compact or clear) composes cox's own sources of truth in a fixed order, ported
verbatim from firstmate's `fm-session-start.sh`:

1. `LEADER LEASE` - `<ws>/.cox/leader-lease` names the one leader allowed to mutate. A live competing holder, or a lease
   that cannot be written, makes a `READ-ONLY SESSION`: the digest still prints, but the wake queue, the deferred forge
   checks and every repair are skipped and said so.
2. `DOCTOR` - detect-only workspace diagnostics.
3. `WAKE QUEUE` - `cox wake drain` for every active epic and the `cox wake ack-through <gen>` line; the digest never
   acknowledges.
4. `SUPERVISION OPERATING INSTRUCTIONS` - exactly one block for the leader harness; on Pi it proves the leader
   extension is loaded in the running process (`PI_LEADER_EXTENSION`), not merely installed.
5. `READ-ONCE CONTRACT` - everything below is printed in full; do not re-read it.
6. `FLEET STATE` - the compact `BACKLOG.md` listing (closed rows omitted, every in-epic, held and blocked row in full,
   other open rows bounded to 20 with the exact remainder), each story's `cox state` row, endpoint liveness and status
   tail (5 lines, 220 characters each), orphan status, then the four sections `Captain's Call`, `Recently Landed`,
   `Underway`, `Charted Next`, each with its empty-state sentence.
7. `FORGE CHECKS` - GitHub authentication and the inactive-story state reads run in a detached `cox bearings deferred`
   worker, never on the blocking path; a failed result arrives once as a `startup-forge` wake.
8. `NOTES` - the three memory files below, `ABSENT` distinguished from `(present, empty)`, and the budget line.
9. `NEXT STEP`, then the digest's own token estimate on its last line.

The whole digest runs under a 120 s bound. A stage that hangs is killed (TERM, then KILL) and the digest ends with a
`STARTUP TRUNCATED` banner naming the stage that stopped and every stage that never printed; it still exits 0. On a Pi
compact whose `AGENTS.md` changed since the session's true start, the current file is re-emitted before the fleet state.

### Startup memory: `cox/notes/`

Three files are printed at every start and budgeted together: `cox/notes/captain.md` and `cox/notes/captain-shared.md`
(default tier `pinned`: preferences, authority, standing rulings) and `cox/notes/learnings.md` (default tier `aging`:
operational facts that must re-prove themselves). History never lives here: epic outcomes stay in the epic dir and git,
product gaps are `BACKLOG.md` rows. This section owns the tier contract (firstmate's stow skill); each file's header
carries only the pointer `<!-- memory tiers: see docs/handoff.md -->`.

- **Markers** trail an entry: `<!--a:YYYY-MM-DD-->` aging and `<!--p:YYYY-MM-DD-->` perishable (the date is the last
  reinforcement; perishable prose names a checkable expiry), `<!--P-->` pinned in a non-pinned file, `<!--g-->` one
  legacy grace cycle. An entry matching its file's pinned default carries no marker.
- **Clocks**: aging is stale at 30 days, perishable at 7. With the `cox/notes-pass-horizon` presence flag a dated marker
  also carries `/N` unreinforced passes, stale at 10 (aging) or 3 (perishable); without the flag no counter is read or
  written. Pinned entries read no clock and are never moved automatically.
- **Budget**: `cox/notes-budget` holds one positive integer and one newline (default `7500`, materialized when absent);
  a malformed, symlinked, hardlinked or special file is rejected, never defaulted. The estimate is `ceil(UTF-8 bytes /
  3)` per file; an absent file counts nothing.
- **Curation** is `cox bearings curate [--reinforce "<entry>" ...]`: reinforcement (only for entries this session
  evidenced) refreshes the date, a pass ticks counters, stale entries and unconfirmed grace entries move to
  `cox/notes/memory-archive.md` under `## <date> notes pass` with provenance and reason, and an over-budget result evicts
  dated aging entries oldest-first only when that can close the gap; otherwise the receipt opens a captain decision
  (raise the budget, or trim a named pinned entry). The archive is append-only, never printed and never counted. The
  receipt reports the budget before and after, one action per file, and whether the session is reset-safe.

## Operator loop

1. Start a session from the `cox bearings` digest; do not re-read the sources it printed.
2. Drain wakes at the start of a leader turn.
3. Handle each durable question, completion, or failure record.
4. Acknowledge through the highest handled generation.
5. When idle, use the harness delivery mode described by its capability card.

Do not poll a worker terminal for meaning and do not type instructions directly into it. Use `cox steer`, `cox reply`,
and `cox control` so the interaction survives restarts and remains auditable.

## Next

- [Operations](operations/index.md) for supervision and recovery routes.
- [Protocol model](protocol/index.md) for schema authority.
- [CLI map](reference/cli.md) for task-to-command discovery.
