<!-- Parent: ../../AGENTS.md -->
# CMD/COX - THE CLI

## OVERVIEW

`package main` for the `cox` binary. `main.go` holds the usage text and the verb switch; each verb lives in its own
`<verb>.go` with its flag parsing and wiring. The engine is in `internal/`; a verb file stays thin.

## WHERE TO LOOK

| Task | Location | Notes |
|---|---|---|
| Add a verb | `main.go` (usage + `switch args[0]`), new `<verb>.go` | also add it to `docs/reference/cli.md` |
| Story dispatch, park, resume, done | `story.go` | |
| Worker model and harness authorization | `cox.go` | `resolveWorkerModel`, `authorizeWorker` |
| Leader hooks (session start, stop, prompt submit, precompact) | `hook.go` | hook shapes in `hooks/leader.json` |
| Epic verbs | `epic.go` | engine in `internal/epic` |
| PR audit / ship facts | `audit.go`, `ship.go` | three-state, engine in `internal/verdict` |
| Replies and questions | `reply.go`, `question.go` | terminal plane vs orchestration plane |
| Wake drain / wait / ack | `wake.go` | |
| Watcher process | `watch.go` | engine in `internal/watch` |

## CONVENTIONS

- Stdlib `flag` per verb (`flag.NewFlagSet`), no framework.
- Exit codes: 0 ok, 1 failure or divergence, 2 usage error, 3 unknown (a wait that timed out, a check that could not
  run). Callers and skills branch on them, so never collapse 3 into 1.
- `--json` output is a contract other tools read; add fields, do not rename them.
- Tests sit next to the verb (`<verb>_test.go`) and call the verb's function with fakes.

## ANTI-PATTERNS

- Business logic in a verb file: move it into the owning `internal/` package.
- Printing a guess when a backend or forge call failed: report unknown instead.
