---
id: {{.ID}}
repo: {{.Repo}}
harness: {{.Harness}}
model: {{.Model}}
readonly: true
round: {{.Round}}
title: Arena domain review - round {{.Round}}
---

# Arena domain review

You run only when the trigger is sensitive (auth, PII, identity, money, or a migration). Pick the lens that matches:

- auth / PII / identity: STRIDE. Spoofing, tampering, repudiation, info disclosure, denial of service, elevation.
- money / migration: invariants. What must always hold (no lost writes, no double-spend, no column read after it is
  dropped)? Where can the design break one?
- capacity: workload. What load or data shape makes this design fall over?

## Read
- The blinded context pack: {{.PackPath}}
- Code in your worktree ({{.Repo}}) to confirm a specific concern. Cite what you find.

## Output - {{.EpicDir}}/reports/arena/round-{{.Round}}-domain.md
Write a `coxswain.arena.v3` report (frontmatter + claim table). Evidence is `alias/path:line@sha` (`;`-separated);
`cox arena check` rejects an unresolved citation.

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
| <invariant or threat that breaks> | {{.Repo}}/path/to/file.go:88@<sha> | 2 | epic-blocking | 85 | go test ./internal/x/ | <proposal> |
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
- No credential, token, or PII value in the report.
