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
wakes, audit each result, let the captain merge, then record completion and close. [First epic](first-epic.md)
provides the narrow command path. [Architecture](../ARCHITECTURE.md) explains the boundaries and [Handoff](../handoff.md)
explains the leader/worker contract.

## Inside an epic directory

An epic lives in `<workspace>/<project>/epics/<slug>/`. Its subdirectories are the durable channels from
[Handoff](../handoff.md) on disk - the leader watches these files, so nothing lives only in a chat:

| Path | Holds | Channel |
|---|---|---|
| `DESIGN.md` | The signed epic contract and captain rulings | Brief (source) |
| `stories/<id>.md` | One rendered story per repo - the worker's contract | Brief (source) |
| `repos` | The repo aliases this epic spans | - |
| `epic.env` | `EPIC` and `PROJECT` (plus the port block for a backend epic) | - |
| `<alias>` (symlink) | The worktree for each repo, on `epic/<slug>` | - |
| `inbox/<story>/NNN.msg` | Leader steers and replies, moved to `handled/` on ack | Steer, Question reply |
| `questions/<story>/qNNN*` | A worker's numbered question and its answer | Question/reply |
| `handoffs/<story>.md` | A worker's checkpoint for its future session; `_leader` for the leader | Checkpoint |
| `reports/` | Audit, scout, and visual-review reports | Status/report |
| `.cox/` | State tree: `epic.json`, the `events.jsonl` event log, the wake queue, the watcher pid | Status/report, Wake |

The `.cox/` tree and `cox/workspace.json` are machine-bound and git-ignored (`cox workspace init` writes those rules);
`DESIGN.md`, `stories/`, and `repos` are committed so the epic can be re-attached on another machine with
`cox epic attach`.
