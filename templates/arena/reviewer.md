---
id: {{.ID}}
repo: {{.Repo}}
harness: {{.Harness}}
model: {{.Model}}
readonly: true
round: {{.Round}}
title: Arena reviewer (precedent + journey) - round {{.Round}}
---

# Arena reviewer - precedent and journey

You cover two questions the adversary does not.

## Read
- The blinded context pack: {{.PackPath}}
- For precedent: you MAY grep the workspace and `docs/decisions/` for prior epics, decisions, or code that already solve
  or contradict this design.
- For journey: read only the contract in the pack and the client code that consumes it; walk the critical journey end to
  end and find where it does not close.

## Questions
1. Precedent: has an existing epic, decision, or piece of code already solved this, or does one contradict it? Point to
   it.
2. Journey: follow the critical user journey through the contract. Where does a step have no owner, no error path, or no
   return?

## Output - {{.EpicDir}}/reports/arena/round-{{.Round}}-reviewer.md
Write a `coxswain.arena.v3` report (frontmatter + claim table), same as every role. Evidence is `alias/path:line@sha`
citations (`;`-separated); `cox arena check` rejects a citation that does not resolve.

```
---
recommendation: <one line>
assumptions:
  - <a fact you assumed>
checks_required:
  - <something to confirm>
conflicts_with:
  - <ADR id, or omit the key>
---
| claim | evidence | tier | severity | confidence | check | proposal |
|---|---|---|---|---|---|---|
| <precedent or journey gap> | {{.Repo}}/path/to/file.go:12@<sha> | 3 | significant | 70 | grep -n foo {{.Repo}}/x.go | <proposal> |
```

- `tier`: 1 locked decision, 2 test result, 3 live code/log, 4 official docs, 5 inference. Tier 5 cannot be
  `epic-blocking`.
- `severity`: `epic-blocking`, `significant`, `minor`. `confidence`: 0-100.
- `check` is required for `epic-blocking`/`significant`: a runnable command or `alias/path:line@sha == "<text>"`.
- **Citations need the `<alias>/` prefix** (e.g. `{{.Repo}}/internal/x.go:12@sha`). **Check commands run at the repo
  root**, so do NOT prefix a command path with the alias (`grep -n x internal/x.go`, not `{{.Repo}}/internal/x.go`); only
  an assertion keeps the `alias/path:line@sha == "<text>"` syntax.

## Rules
- Read-only: your only write is the report above.
- Do not ask; append any assumption to the report and finish.
