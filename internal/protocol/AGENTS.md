<!-- Parent: ../AGENTS.md -->
# INTERNAL/PROTOCOL - FILE CHANNELS

## OVERVIEW

One package per channel between leader, worker, and watcher. Each is a versioned, durable record on disk under the
epic dir, so a restart or compaction loses nothing. Specs: [docs/protocol/](../../docs/protocol/index.md).

## WHERE TO LOOK

| Channel | Package | Direction |
|---|---|---|
| Steer (`coxswain.inbox.v1`) | `inbox` | leader -> worker, `<epic>/inbox/<story>/NNN.msg` |
| Question / reply | `question` | worker -> leader -> worker (terminal plane, ADR 0012) |
| Report (progress, done, blocker) | `report` | worker -> leader |
| Status lines | `status`, `decision` | worker -> leader |
| Control verbs | `control` | leader -> worker (interrupt, park, relaunch) |
| Brief (context pack) | `brief` | leader -> worker at dispatch |
| Busy state | `busy` | harness -> cox (ADR 0016) |
| Checkpoint | `checkpoint` | worker -> its future self |

## CONVENTIONS

- Records carry a schema version (`coxswain.<name>.v1`); a breaking change is a new version, never an edit.
- Writes are atomic (tmp + rename); a reader never sees a half-written record.
- A handled record moves to `handled/`; it is not deleted.
- Change a record shape together with its `docs/protocol/` page.
