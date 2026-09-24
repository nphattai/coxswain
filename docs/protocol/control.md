# Control verbs

`cox control <story> interrupt|park|relaunch` and `cox story park|resume` run through `internal/protocol/control`,
ported from firstmate's `bin/fm-control.sh` @1e0e773 (exit is park: Stop closes the terminal, ADR 0012).

## Invariants

- Every verb takes the story's lifecycle lock `<epic>/.cox/sessions/.control-<story>.lock` before it reads any state
  and holds it to its last write; a second action is refused ("another lifecycle action is already running"). A dead
  holder's lock is broken. `control.LockWait` is the blocking form for a durable writer.
- The story must be recorded (it has events), the session must be bound to it, and the harness must have verified
  control mechanics (`control.ResolveHarness` refuses an unknown name).
- Interrupt refuses a settled agent before any key, sends the backend keystroke only to a harness that honours it
  (otherwise only the inbox interrupt record), and revalidates the agent after delivery: an interrupt must leave the
  agent running. Cancellation is recorded `unconfirmed`.
- Park refuses an agent it cannot classify, treats a settled agent as already stopped (idempotent), and completes only
  when the stopped agent reads settled within the exit wait (30s); otherwise the story stays `pending_external`.
- Relaunch refuses, before any record or agent is touched: a closed story, missing instructions, an empty note, a
  missing or non-git worktree, an uninspectable HEAD or status, an unclassified prior agent, and pending or unproven
  composer text. It records the checkpoint (`worktree_head`, `worktree_dirty`), proves the prior agent stopped, retires
  the prior wiring, arms and spawns; any failure after the arm retires the replacement's busy record.
