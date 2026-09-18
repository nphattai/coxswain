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

The lifecycle: `cox epic new` -> `cox epic stories` -> `cox story dispatch` -> supervise via wakes -> `cox audit pr` ->
captain merges -> `cox story done`. See [Architecture](../ARCHITECTURE.md) for the full picture and
[Handoff protocol](../handoff.md) for the leader/worker contract.
