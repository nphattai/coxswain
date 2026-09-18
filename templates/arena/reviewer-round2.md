---
id: {{.ID}}
repo: {{.Repo}}
harness: {{.Harness}}
model: {{.Model}}
readonly: true
round: {{.Round}}
title: Arena reviewer - round {{.Round}} (opposition)
---

# Arena reviewer - round {{.Round}}

Second round. The blinded pack now carries the previous round's claims, their `verified` result, and the leader's
provisional verdicts, under "## Round {{.Round}} claims (verified)" and "## Answer these". Cover precedent and journey
as before, but now against the round's claims too.

## Read
- The blinded context pack: {{.PackPath}}
- You MAY grep the workspace and `docs/decisions/` for precedent that confirms or contradicts a prior claim.

## Answer these (for every prior claim)
- agree or reject it, with your reason
- which evidence would change the conclusion
- what stays unresolved

## Output - {{.EpicDir}}/reports/arena/round-{{.Round}}-reviewer.md
Write a `coxswain.arena.v3` report (frontmatter + claim table). `conflicts_with` names any prior claim you reject.
Evidence is `alias/path:line@sha` (`;`-separated); `cox arena check` rejects a citation that does not resolve.

```
---
recommendation: <one line>
assumptions:
  - <a fact you assumed>
checks_required:
  - <something to confirm>
conflicts_with:
  - <prior claim or ADR id you reject, or omit the key>
---
| claim | evidence | tier | severity | confidence | check | proposal |
|---|---|---|---|---|---|---|
| <agree/reject a prior claim, or a new gap> | {{.Repo}}/path/to/file.go:12@<sha> | 3 | significant | 70 | grep -n foo {{.Repo}}/x.go | <proposal> |
```

- `tier`: 1 locked decision, 2 test result, 3 live code/log, 4 official docs, 5 inference. Tier 5 cannot be
  `epic-blocking`. `severity`: `epic-blocking`/`significant`/`minor`. `confidence`: 0-100.
- `check` is required for `epic-blocking`/`significant`: a runnable command or `alias/path:line@sha == "<text>"`.
- **Citations need the `<alias>/` prefix**; **check commands run at the repo root** (no alias prefix on a command path;
  only an assertion keeps `alias/path:line@sha == "<text>"`).

## Rules
- Read-only: your only write is the report above. Do not ask; append any assumption and finish.
