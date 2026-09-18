---
id: {{.ID}}
repo: {{.Repo}}
harness: {{.Harness}}
model: {{.Model}}
readonly: true
round: {{.Round}}
title: Arena domain review - round {{.Round}} (opposition)
---

# Arena domain review - round {{.Round}}

Second round. The blinded pack now carries the previous round's claims, their `verified` result, and the leader's
provisional verdicts, under "## Round {{.Round}} claims (verified)" and "## Answer these". Re-apply your lens (STRIDE for
auth/PII/identity; invariants for money/migration; workload for capacity) to those claims and to any invariant they miss.

## Read
- The blinded context pack: {{.PackPath}}
- Code in your worktree ({{.Repo}}) to confirm a specific concern. Cite what you find at its sha.

## Answer these (for every prior claim)
- agree or reject it, with your reason
- which evidence would change the conclusion
- what stays unresolved

## Output - {{.EpicDir}}/reports/arena/round-{{.Round}}-domain.md
Write a `coxswain.arena.v3` report (frontmatter + claim table). `conflicts_with` names any prior claim you reject.
Evidence is `alias/path:line@sha` (`;`-separated); `cox arena check` rejects an unresolved citation.

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
| <agree/reject a prior claim, or an invariant that breaks> | {{.Repo}}/path/to/file.go:88@<sha> | 2 | epic-blocking | 85 | go test ./internal/x/ | <proposal> |
```

- `tier`: 1 locked decision, 2 test result, 3 live code/log, 4 official docs, 5 inference. Tier 5 cannot be
  `epic-blocking`. `severity`: `epic-blocking`/`significant`/`minor`. `confidence`: 0-100.
- `check` is required for `epic-blocking`/`significant`: a runnable command or `alias/path:line@sha == "<text>"`.
- **Citations need the `<alias>/` prefix**; **check commands run at the repo root** (no alias prefix on a command path;
  only an assertion keeps `alias/path:line@sha == "<text>"`).

## Rules
- Read-only: your only write is the report above. No credential, token, or PII value in the report.
