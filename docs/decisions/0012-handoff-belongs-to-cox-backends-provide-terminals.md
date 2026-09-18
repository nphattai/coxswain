# 0012 - Handoff belongs to cox; backends only provide worktrees and terminals

- Status: Accepted (captain, 2026-09-15, after arena round 1 on epics/m10-orca-plane, design_signed)
- Date: 2026-09-15

## Context

cox drove Orca through its orchestration plane (`task-create`, `worker-start`, mailbox `check --run`, `worker-stop`,
coordinator doorbell). Dogfooding M5-M9 spent six rounds of fixes coexisting with that plane: Orca's worker template
layered over the cox brief (heartbeat every 5 minutes, one worker_done per dispatch, ask through orchestration), the
"You have N orchestration messages" bell typed into the leader terminal on every message, one-run-per-coordinator
binding, Orca abandoning a dispatch on its own recovery, and finally two epics sharing one run so one epic's watcher
consumed the other's worker_done.

firstmate (upstream 2da3c5e) uses Orca only at the terminal plane: `repo add`, `worktree create`, `terminal create`,
`terminal send|read|close`, `worktree rm`. It launches the harness itself and owns inbox, doorbell, watcher, and
composer classification. None of the problems above exist there, and its herdr backend follows the same shape.

An adversarial review (codex, `epics/m10-orca-plane/reports/arena/round-1-adversary.md`) found one epic-blocking gap
in the proposal: the question/reply channel relied on Orca message ids. Accepted; the design now carries a file-based
question/reply channel (`epics/m10-orca-plane/DESIGN.md`, amendment).

## Decision

1. **Handoff is cox's.** Brief, steer, control, status, checkpoint, question/reply, wake queue, and watcher live in the
   epic directory and are identical for every backend. No backend mailbox, template, heartbeat, or completion cap
   participates in the protocol.
2. **A backend provides worktrees and terminals only**: WorktreeCreate/Remove, Spawn (create terminal, type the harness
   launch), Send (doorbell), Interrupt, Stop (close terminal), Probe and Composer (liveness). Orca moves to this plane
   in M10; herdr, which already sits on it, is completed to full capability in the same milestone.
3. **Liveness** comes from the backend's structured agent state when it exists (Orca `worktree ps` agents[], herdr
   native agent-state), else the shared composer classifier; absence of a structured entry is `unknown`, never `gone`.
4. **Compatibility switch**: `backend.orca.plane: orchestration | terminal` in policy. Default `terminal` once one
   live E2E and one live story pass on claude and codex; the orchestration path is removed one milestone later by its
   own ADR.
5. **Question/reply** is file-based (`questions/<story>/qNNN.md`, `qNNN.answer.md`), with `cox story report question`,
   `cox reply`, and `cox question wait --max` (timeout exits 3 so the worker checkpoints and parks).

## Consequences

- No Orca bell in the leader terminal, no "blocked by hook" notice, no heartbeat rule, no worker_done cap, no run
  binding, no cross-epic mailbox contamination, Orca cannot abandon a worker it does not know as a dispatch.
- Lost: Orca's durable mailbox (cox has `wake.jsonl` with generations), Orca UI dispatch rows, Orca capability tokens
  (cox binds the story id at spawn via COX_STORY and refuses a report for another story).
- Supersedes the Orca-specific rows of decision 0002's capability notes and the `done:` completion convention (F9)
  once the terminal plane is default.

## Review when

After M10 lands: if `worktree ps` cannot report codex agents, or the composer fallback misclassifies twice in a live
week, revisit liveness. Reopen the plane switch only if a capability of the orchestration plane turns out to be needed
and cannot be reproduced on files.
