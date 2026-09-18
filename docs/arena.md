# Arena v3: adversarial design review

Arena is coxswain's design-review mechanism for hard or large epics. It runs a small set of specialist roles against a
blinded copy of the design, machine-checks and verifies their claims, and hands the leader a synthesis to adjudicate and
the captain a signature gate. Arena v3 (ADR 0013) closes the six gaps between arena v2 and the mentor's arena workflow
(`plans/reports/260916-arena-vs-mentor/`): evidence tiers, the full specialist return, claims verified by running a
check, an adversarial second round, a synthesis with the adopted decision and gates, and read-only roles.

## When it runs

`cox epic arena --epic <dir>` triggers from policy: 3+ repos, a design that mentions migration/money/auth/identity/PII,
or a captain `--reason`. Above the bar it is a **full** arena (adversary + reviewer, plus domain when the trigger is
sensitive); below it, **lite** runs a single adversary. `--lite` forces a single adversary; a design with nothing to
review is `none`.

## The three modes

Headless is the default (captain ruling 2026-09-16): a specialist is a function call, not a worker.

1. **Headless (default).** Each role runs as a read-only subprocess in the leader checkout (the epic's first repo):
   - claude: `claude -p --model <m> --permission-mode plan --output-format json --no-session-persistence` (prompt on stdin)
   - codex: `codex exec --json -s read-only -m <m> -` (prompt on stdin)

   The prompt is the blinded pack plus the role template plus a directive to emit the report inside a fenced ```report
   block. cox parses the JSON, extracts the block, writes `reports/arena/round-N-<role>.md`, and records a `headless`
   event (attempt, kind, the leader-checkout HEAD sha). No worktree, no terminal, no watcher, no backend.

2. **Terminal (`--terminal`).** Each role runs on its own worktree at that sha with its own terminal, launched with the
   `harness.launch.arena.<h>` flags. In terminal mode a role must be able to write its report into its own worktree, so
   the flags are one notch above read-only: claude `--permission-mode acceptEdits`, codex `-a never -s workspace-write`
   (plan mode and the read-only sandbox cannot write, even with `--add-dir`, so they are the headless path only). The role
   writes its report in the worktree and `cox arena collect` brings it home. cox types no `--add-dir` for an arena role
   (it writes only its report, in its own worktree), so a read-only codex never exits on an incompatible writable root.
   cox switches to terminal automatically (with a notice) when the leader checkout is dirty (a headless role reads that
   checkout, so uncommitted changes would leak in) or when a role template declares `needs_worktree: true`. The dirty
   check runs `git status -uall` and ignores everything under the epic dir (the arena regenerates the pack, role stories,
   and reports there on every run), so only a change to the leader checkout outside the epic dir switches the mode.

Headless is always read-only (claude `--permission-mode plan`, codex `-s read-only`): cox writes the report from the
JSON. See [docs/adapters/claude.md](adapters/claude.md) for the `--permission-mode plan` / `--add-dir` finding.

## The flow

```
cox epic arena          trigger, build blinded pack, run roles (headless), write round-N-<role>.md
cox arena check         machine-verify each report: citations, tiers, confidence, check presence
cox arena verify        run each claim's check in a detached worktree -> verify-round-N.json
cox arena synth         assemble the synthesis, assign claim ids, auto-fill tier + verified, record the synthesis sha
  (leader fills the six sections and every verdict; captain calibrates)
cox arena answer <id> <yes|no|text> --by <name>   fill a claim's captain-agrees cell by id (the captain write path)
cox arena verify        (re-run if the leader edited checks)
cox epic design --sign  refuse unless the v3 gates pass, else record design_signed
```

Re-running `cox epic arena --round N` skips any role that already finished the round (its report is present, or its story
completed it) with a notice, so a full re-run fills only the missing roles; `--force` re-runs a role regardless, and
`--role <name>` runs a single role of the active set.

A second round (`--round 2`, max 3) rebuilds the pack with the prior round's verified claim table and provisional
verdicts under "Round N claims (verified)" and "Answer these", so round-2 roles oppose rather than restate. `--round 4`
is refused.

## The report (coxswain.arena.v3)

A role writes frontmatter then a claim table:

```
---
recommendation: keep the column until billing reads tier_id
assumptions:
  - billing.Price is the only reader of accounts.tier
checks_required:
  - confirm no other caller reads the column
conflicts_with:
  - 0002
---
| claim | evidence | tier | severity | confidence | check | proposal |
|---|---|---|---|---|---|---|
| Dropping accounts.tier breaks billing which still reads it | billing/app/billing.go:4@a1b2c3d | 3 | epic-blocking | 90 | grep -n "accounts.tier" billing/app/billing.go | keep the column until billing reads tier_id |
```

- **tier** - evidence hierarchy: 1 locked decision (ADR/ruling), 2 test or experiment result, 3 live code/log/tool
  output, 4 official docs, 5 reasoned inference. A tier-5 claim cannot be `epic-blocking`. On a conflict the lower tier
  number wins unless the leader records why not.
- **severity** - `epic-blocking | significant | minor`.
- **confidence** - 0-100.
- **check** - required for an epic-blocking or significant claim: a runnable command (from the verify allowlist) or an
  assertion `alias/path:line@sha == "<expected text>"`.

`cox arena check` rejects a report whose citation does not resolve, whose tier is outside 1-5, whose confidence is
outside 0-100, whose tier-5 claim is epic-blocking, or whose epic-blocking/significant claim has no check. A v2 report
(no frontmatter, four columns) still reads, with a "v2 report" warning.

Two format rules have hints. A citation whose leading segment is not a repo alias (the author dropped the `<alias>/`
prefix) is a format error naming the known aliases. A command check that carries an `<alias>/` prefix on a path is a
**warning**, not a failure: verify runs a command at the repo root and strips a leading alias, so the report is usable,
but the author should drop the prefix (only an assertion keeps the `alias/path:line@sha == "<text>"` syntax).

## Verify

`cox arena verify --round N` runs each claim's check and records pass/fail/unknown in `reports/arena/verify-round-N.json`.
Every check runs in a **temporary git worktree detached at the cited sha**, never in the leader checkout, so verifying
never mutates or reads the working tree. Commands are limited to a tight allowlist - `go test`, `go vet`, `git
show|log|grep|diff`, `grep`, `rg`, and cox's read-only verbs (`state`, `arena check`, `doctor`, `route`, `quota`) - run
without a shell (so `;`/`|`/`&&` cannot chain), under a 5-minute timeout, with the network off (`GOPROXY=off`). A command
outside the allowlist, a timeout, or a worktree that cannot be built is `unknown`, never a silent pass. A command runs at
the repo root, so a path argument carrying the claim's own `<alias>/` prefix is stripped first (`grep -n x
cox/internal/a.go` runs as `grep -n x internal/a.go`); an assertion keeps its `alias/path:line@sha` syntax.

## The synthesis

`cox arena synth --round N` assembles the checked-clean reports into `reports/arena/synthesis.md` (a symlink to
`synthesis-round-N.md`). It gives each claim a stable `id` (`<role>-<round>-<n>`, or the role's own id when the report
carries one), auto-fills each claim's `tier` (from the report) and `verified` (from verify-round-N.json), and records the
synthesis content sha under `.cox/arena/synthesis-<round>.sha`; the leader writes the six sections and every verdict:

1. Adopted decision, 2. Decisive evidence, 3. Rejected alternatives, 4. Preserved locked decisions,
5. Remaining uncertainty, 6. Verification gates.

Verdict is `accepted | rejected | unresolved | captain_decision`. A rejected claim stays in the table with its reason;
nothing is deleted. `captain_decision` marks a question evidence cannot settle - the captain answers by filling the
`captain agrees` cell.

The captain fills a `captain agrees` cell with `cox arena answer <claim-id> <yes|no|text> --by <name> --epic <dir>`,
which targets the row by its id (a captain_decision's answer goes in the same cell, which the sign gate reads). It refuses
when `synthesis.md` no longer matches the sha recorded at synth (a re-synth can shift ids), so an answer never lands on
the wrong row; a successful answer re-records the sha. This is the single write path for the captain cells.

## Signing

`cox epic design --sign` refuses a v3 synthesis when:

- any of the six sections is still empty (a `<...>` placeholder counts as empty),
- an accepted epic-blocking claim is not `verified: pass`,
- a `captain_decision` has no `captain agrees` answer,
- the round is past 3, or
- an epic-blocking claim is left `unresolved` (round 2 required).

On success it records `design_signed` with the DESIGN.md and synthesis shas. A later DESIGN.md change is recorded with
`cox epic design --amend --reason <why>`. A legacy v2 synthesis keeps its original P7 gate (a `captain agrees` cell on
every row), so the already-signed epics still read.

## Against the mentor workflow

| Mentor protocol | Arena v3 |
|---|---|
| shared compact context pack | blinded context pack (authorship/model/discussion stripped) |
| evidence hierarchy | tier 1-5 per evidence item; tier-5 cannot block |
| specialist returns assumptions, confidence, checks, conflicts | v3 frontmatter + confidence column |
| claims verified by running something | `cox arena verify` runs each check, records pass/fail/unknown |
| second round only on material disagreement, with the opposing claims | `--round 2` carries the verified claims + verdicts as opposition; max 3 |
| orchestrator owns synthesis, captain owns business decisions | leader writes the six sections + verdicts; `captain_decision` + sign gate |
| specialists as function calls | headless is the default mode |

## Calibration

The `captain agrees` column is the calibration record: after several arenas it shows which roles the captain keeps.
ADR 0013's review-when fires after five v3 runs - check whether tier-5 was ever decisive, whether verify checks were
mostly `unknown`, and which roles earned their cost.
