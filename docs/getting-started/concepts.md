---
hide:
  - toc
---

# Core concepts

| Term | What it is |
|---|---|
| **Leader** | The agent that designs each phase and reviews worker output. One leader terminal per project. Never merges without the captain. |
| **Worker** | An agent dispatched to implement one story in an isolated git worktree, one per repo. |
| **Captain** | The human. Chats with the leader, approves designs, and is the only one who merges. |
| **Epic** | A cross-repo unit of work. Lives in an epic directory with `DESIGN.md`, `repos`, and a `.cox/` state tree. |
| **Story** | One repo's slice of an epic, rendered from `templates/story.md` with the project policy resolved. |
| **Arena** | Adversarial design review: an adversary in a *different* harness raises machine-checked, verified claims against a design before it is signed. |
| **Wake protocol** | How a parked or idle leader/worker is woken - push (harness hooks) or pull (`cox wake wait`) - so no event is lost. |
| **Event log** | The append-only `.cox/events.jsonl` that is the single source of truth for story lifecycle. |

<figure class="cox-diagram">
  <div class="cox-diagram__surface">
    <picture>
      <source media="(max-width: 640px)" srcset="../../assets/diagrams/story-lifecycle-mobile.svg">
      <img src="../../assets/diagrams/story-lifecycle.svg" alt="A story moves from submitted through working, may pause for input or parking, and ends completed, failed, or canceled.">
    </picture>
  </div>
  <figcaption>The event log records each transition. Live observations refine the view but never replace that history. <a href="../../assets/diagrams/story-lifecycle.svg">Open full size</a></figcaption>
</figure>

The lifecycle is: define the epic, approve its design, render repo-scoped stories, dispatch workers, supervise through
wakes, audit each result, let the captain merge, then record completion and close. [Run your first epic](../QUICKSTART.md)
provides the narrow command path. [Architecture](../ARCHITECTURE.md) explains the boundaries and [Handoff](../handoff.md)
explains the leader/worker contract.
