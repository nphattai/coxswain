# Operations

Use this section after installation. It follows the decisions a captain or leader makes, from design through release
and recovery.

## Run an epic

| Need | Route |
|---|---|
| Create and dispatch a first story | [Run your first epic](../QUICKSTART.md) |
| Decide whether a design needs adversarial review | [Arena](../arena.md) |
| Supervise workers and answer questions | [Handoff](../handoff.md) |
| Inspect all stories without changing them | [Board](../board.md) |
| Audit a visual plan or synthesis | [Visual review](../review.md) |
| Close and release an epic | `cox ship facts`, the `cox-ship` skill, and `cmd/cox/ship.go` |

## Recover safely

| Symptom | First action | Why |
|---|---|---|
| Story is `pending_external` | Run `cox reconcile` in dry-run mode | Recovery must prove the external state before writing |
| Worker is running away | Use `cox control <story> interrupt` | Control verbs are durable and bounded |
| Worker must pause | Use `cox story park` | Parking requires a fresh checkpoint |
| Worker is waiting for a decision | Answer with `cox reply` | The answer becomes a durable question/reply record |
| Backend or forge fact is unavailable | Preserve `unknown` and inspect the adapter | Missing evidence is not success or failure |
| Wake arrived but state looks stale | Read [Handoff](../handoff.md), then inspect `cox state` | Wakes notify; the event log and resolver own state |

[FAQ and troubleshooting](../faq.md) covers common setup failures. Adapter-specific ceilings belong in
[Adapters](../adapters/index.md).

## Park and resume a worker

Park a worker only after it has written a checkpoint for the current attempt and git head. Resume starts attempt N+1
in the same worktree and injects that verified checkpoint:

```bash
cox story park <story> --epic <epic-dir>
cox story resume <story> --epic <epic-dir>
```

[Handoff](../handoff.md) owns the checkpoint, stop-confirmation, and recovery semantics. The exact flags remain owned
by `cox --help`.

## Tune only with evidence

- [Routing](../routing.md) explains the harness-choice ladder and why quota does not route automatically.
- [Quota](../quota.md) explains the observe-only quota contract and manual reroute boundary.
- [Lab](../lab.md) runs policy experiments without editing policy.
- [Evidence](../evidence/index.md) separates dated compatibility results from evergreen operating guidance.

Policy values are not self-justifying defaults. Behavioral sections record `why` and `review_when`, and the captain
owns any change. See [Configuration](../reference/configuration.md) for the resolution chain.

## Captain route

1. Approve the design and any arena resolution.
2. Let the leader dispatch and supervise stories.
3. Review audit evidence and unresolved unknowns.
4. Merge each repository change yourself.
5. Authorize release and epic closure.

## Leader route

1. Read the active epic's `DESIGN.md`, story records, and wake queue.
2. Dispatch bounded stories and use durable handoff channels.
3. Audit each worker result against the accepted contract.
4. Escalate scope, risk, and merge decisions to the captain.
5. Never merge, delete a branch, or infer success from unavailable evidence.
