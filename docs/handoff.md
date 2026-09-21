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
      <source media="(max-width: 640px)" srcset="../assets/diagrams/handoff-channels-mobile.svg">
      <img src="../assets/diagrams/handoff-channels.svg" alt="Leader-to-worker, worker-to-leader, and worker-to-future-self channels stay separate and durable.">
    </picture>
  </div>
  <figcaption>Each direction has a distinct record and acknowledgement. The terminal carries only the wake-up signal. <a href="../assets/diagrams/handoff-channels.svg">Open full size</a></figcaption>
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

## Operator loop

1. Drain wakes at the start of a leader turn.
2. Handle each durable question, completion, or failure record.
3. Acknowledge through the highest handled generation.
4. When idle, use the harness delivery mode described by its capability card.

Do not poll a worker terminal for meaning and do not type instructions directly into it. Use `cox steer`, `cox reply`,
and `cox control` so the interaction survives restarts and remains auditable.

## Next

- [Operations](operations/index.md) for supervision and recovery routes.
- [Protocol model](protocol/index.md) for schema authority.
- [CLI map](reference/cli.md) for task-to-command discovery.
