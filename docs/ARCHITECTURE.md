# Architecture

This is a distilled, English version of `plans/260914-crewkit-v2-architecture/plan.md` (sections 2-6). For the
visual version, open `plans/260914-crewkit-v2-architecture/architecture.html` in a browser.

## Design principles

A few principles run through every decision below:

- **State is a file in git, not conversation memory.** A restart or compaction is a non-event because everything is
  re-read from disk.
- **An append-only event log is the source of truth; current state is derived.** No "last value wins" file.
- **Data plane, control plane, and status plane are separate.** Lifecycle commands are a verb allowlist, never free
  text.
- **Identity is explicit**: epic, story, attempt, run, canonical worktree path, session. Nothing is inferred from a
  path prefix or a log line.
- **Results are three-state: pass, fail, or unknown.** Unknown never collapses into "clean" just because data is
  missing.
- **Every external boundary is an adapter**: backend (Orca/herdr), harness (claude/codex), forge (GitHub), service
  (per-project script). The core never imports a concrete backend or harness package.
- **coxswain is model-agnostic.** The leader and each worker pick a harness from policy; adding a harness is adding
  an adapter, not touching the core.

## The leader/worker model

A captain (a person) chats with a leader session. The leader runs coxswain's instructions (`AGENTS.md` plus skills)
and reads wakes from the watcher. The leader talks only to the `cox` CLI, which is itself harness-agnostic; `cox`
talks to adapters, and adapters talk to Orca/herdr, GitHub, or a harness CLI. Dependencies point one way: instructions
and hooks call `cox`; `cox` calls an adapter; an adapter calls the external system. Nothing calls back up.

A leader and a worker are architecturally the same thing - "one session of one harness in one directory" - they
differ only in role (what brief they were given), not in mechanism. Workers run one session per story, each in its
own isolated git worktree, and report back through the event log and mailbox; they never talk to each other directly
and never dispatch a sub-worker (Orca enforces a dispatch depth of 1).

## The `cox` CLI

One binary in Go replaces the 27 bash scripts of v1. Every command that touches data supports `--json` and returns a
meaningful exit code; nothing on a data path swallows a failure with `|| true`. Source layout:

```
cmd/cox/main.go          command dispatch
internal/state/          event log, snapshot, resolver, identity
internal/protocol/       brief, steer, control, status, checkpoint, wake (schema v1)
internal/adapter/        backend/{orca,herdr}, harness/{claude,codex}, forge, service
internal/verdict/        audit, ship, smoke
internal/env/            allocation, ownership, release
```

Go was chosen (decision 0001) for a single static binary with no runtime, a type system that catches the error
classes v1's bash scripts got wrong (swallowed exit codes, brittle text parsing), `go test` against a fake adapter
through an interface, and release via goreleaser.

## The backend interface (decision 0002)

The core depends on one small interface, never on a concrete backend:

```go
type Backend interface {
    WorktreeCreate(repo, branch, base string) (Worktree, error)
    WorktreeRemove(wt Worktree) error
    Spawn(wt Worktree, h HarnessSpec, brief Brief) (Session, error)
    Send(s Session, text string) (rang bool, err error)
    Interrupt(s Session) error
    Stop(s Session) (confirmed bool, err error)
    Probe(s Session) (Liveness, error)
    Mail() Mailbox
}
```

**Orca is the default backend; herdr is optional.** Both satisfy the same interface, and after ADR 0012 both provide
**only worktrees and terminals** - the handoff (brief, steer, control, report, question/reply, wake, watcher) is cox's
and is identical for every backend. Orca has two planes selected by `backend.orca.plane`: **orchestration** (the pre-M10
path: `task-create`/`worker-start`, the orchestration mailbox, `worker-stop`, coordinator doorbell) and **terminal**
(worktree + terminal only: `terminal create` + typed harness launch, liveness from `worktree ps` agents[], `terminal
close`; a reduced mailbox, no run binding, no completion cap). The default flips to terminal once one live E2E and one
live story pass; the orchestration path is removed a milestone later by its own ADR. Herdr sits natively on the terminal
plane and is completed to full capability in M10 (git worktrees, pane spawn, send/interrupt/stop/probe); its composer
and run-listing stay reduced with the reason. Decision 0008's "an unmet capability is an explicit reduced mode, never a
silent guess" applies to backends too. There is no tmux backend - the interface is deliberately small so a community
adapter could add one later without touching the core. See `docs/adapters/orca.md` and `docs/adapters/herdr.md` for the
verified, per-command behavior of each.

`Liveness` is three-state (`Unknown` is the zero value): a failed or unreadable probe is always `Unknown`, never
inferred as "gone". `Stop` returns `confirmed=true` only when the adapter actually confirmed the stop, so a caller
that cannot confirm keeps ownership instead of assuming success.

## The event-log state model

A story's state machine (submitted -> working -> input_required/parked -> completed/failed/canceled) is never held
in memory or in a single mutable file. Every transition is one append-only JSON line in `<epic>/.cox/events.jsonl`,
schema `coxswain.event.v1`: `ts, epic, story, attempt, actor (captain|leader|worker|watcher), from, to, evidence,
external_confirmed`. `snapshot.json` is a cache that must always be able to rebuild identically from the log; if it
disagrees, the log wins.

A transition with an external side effect (stopping a worker, spawning one, removing a worktree) is recorded in two
steps: first `to: "pending_external"` with `evidence.intended_to` naming the target state, then the real transition
once the adapter confirms. If the process dies between the two steps, a reconciler reads `intended_to`, re-probes
what actually happened, and finishes the transition without repeating the side effect. This is the shared fix for
the v1 bugs where a stop, a park, or a worktree removal could silently lose or duplicate its side effect.

`cox state` is the one resolver. It folds the event log (`internal/state.Fold`), then layers in live observations -
backend liveness via `Probe`, git HEAD/dirty/ahead-behind, and (once wired) forge PR/CI state - each tagged with its
own `source` and `observed_at`, and emits `coxswain.fleet.v1` JSON. Identity resolves in a fixed order: attempt id in
the event log, then the canonical (realpath) worktree path, then the backend's dispatch/terminal handle, then the
harness's own session log. Nothing is inferred from a directory name or a path prefix.

## Worktrees (never fall back)

`WorktreeCreate` returns a verified path and branch, or an error - it never falls back to a shared checkout when
creation fails (F02). `worktree.Ensure` re-verifies the path and branch on disk before any worker is placed in it.
`WorktreeRemove` never deletes the underlying git branch: the Orca adapter detaches HEAD first (`git switch
--detach`) and only then asks Orca to remove the worktree, because a still-attached worktree is exactly what Orca's
own removal would delete the branch under. If the detach fails, removal is skipped entirely rather than risking the
branch. No coxswain command deletes a branch, ever - this is a standing rule, not a default that can be flagged away.

## The arena design-review subsystem

Arena is coxswain's adversarial design-review mechanism for hard or large epics, dispatched by `cox epic arena` and
worked through with `cox arena check|verify|collect|synth`. It triggers on policy (3+ repos, a migration, money/auth/
identity/PII, or a direct captain request); below the bar, `arena-lite` runs a single adversary. The full flow and the
report/synthesis formats are documented in [docs/arena.md](arena.md); this is the shape.

Each role reviews a **blinded context pack** (DESIGN.md, verified scout reports, applicable decisions - author names,
model names, and discussion history stripped out):

| Role | Harness | Question |
|---|---|---|
| adversary | different from the leader's harness (default codex if the leader is claude) | Where does this design break? Draw a concrete failure path. |
| precedent + journey | same harness as the leader, fresh session | Does an existing epic/decision/code already solve or conflict with this? Walk the critical journey end to end. |
| domain (optional, trigger-gated) | different from the leader's harness | STRIDE for auth/PII, invariants for money/migrations, workload for capacity. |

The one hard rule is that the adversary always runs a different harness than whichever harness wrote the design (so
model-specific blind spots are not self-reviewing); `cox` derives this from policy, never a hard-coded harness name.

**Roles run headless by default** (ADR 0013): each role runs as a read-only subprocess in the leader checkout (claude
`-p --permission-mode plan --output-format json`, codex `exec --json -s read-only`), and cox writes the report from a
fenced ```report block in the JSON - the specialist is a function call, not a worker. `--terminal` keeps the older
worktree+terminal path (read-only `harness.launch.arena` flags), and cox switches to it automatically when the leader
checkout is dirty or a role template declares `needs_worktree`.

Each role writes a `coxswain.arena.v3` report `reports/arena/round-<n>-<role>.md`: frontmatter (recommendation,
assumptions, checks_required, conflicts_with) and a claim table where every claim carries `evidence alias/path:line@sha`,
an **evidence tier** (1 locked decision, 2 test result, 3 live code/log, 4 official docs, 5 inference; a tier-5 claim
cannot be epic-blocking), a severity (`epic-blocking | significant | minor`), a confidence 0-100, a runnable `check`
(required for epic-blocking/significant), and a proposal. `cox arena check` machine-verifies every citation, tier, and
confidence; `cox arena verify` runs each `check` in a temporary detached worktree at the cited sha (tight allowlist, no
network, 5-minute timeout) and records pass/fail/unknown in `verify-round-N.json`.

The leader writes the synthesis - six sections (adopted decision, decisive evidence, rejected alternatives, preserved
locked decisions, remaining uncertainty, verification gates) and a per-claim verdict (`accepted | rejected | unresolved
| captain_decision`); `synth` auto-fills each claim's tier and verified status. A second round (max 3, with the prior
round's verified claims and provisional verdicts carried into the pack as opposition) runs only for an unresolved
epic-blocking claim or a factual conflict, never because "everyone agreed". The captain signs with `cox epic design
--sign`, which refuses a v3 synthesis with an empty section, an accepted epic-blocking claim not verified pass, an open
captain_decision, or a round past 3, and records a `design_signed` event carrying the SHA of DESIGN.md and the
synthesis; a later DESIGN.md change requires `cox epic design --amend --reason <why>`, recorded as `design_amended`.

## Harness policy (decision 0008: model-agnostic)

Neither the leader nor a worker is locked to one harness. `cox/policy.json` declares, per role, the option set and
the default:

```json
"harness": {
  "leader":  { "options": ["claude", "codex"], "default": "claude" },
  "worker":  { "options": ["claude", "codex", "omp", "opencode"], "default": "claude" },
  "arena": {
    "adversary": { "rule": "not-leader", "default": "codex" },
    "reviewer":  { "rule": "same-as-leader-new-session" }
  }
}
```

Adding a new harness means adding an adapter and adding it to this list - the core never branches on a harness name.
Every harness adapter exposes a **capability card** (`Card()` in code, mirrored by a doc under `docs/adapters/`):
roles it can fill, how wake is delivered (`push` via hooks - claude, and codex once `cox workspace hooks --harness codex`
installs its project-level `.codex/hooks.json` - or `pull` via polling `cox wake wait` when a harness has no hooks),
whether checkpoints are automatic or manual, and whether it supports doorbell, interrupt, and telemetry.
`cox story dispatch` and `cox doctor` refuse a role that needs a capability its harness card does not have, or run a
documented reduced mode - never a silent, weaker guarantee.

## Routing, board, and lab (ADR 0011)

Three conditional features were opened by the captain's 2026-09-15 ruling (decision 0011); each ships with its
behavioural limit written into the code, so a small sample can never silently harden into a rule.

- **Routing** (`internal/routing`, `cox route`) picks a worker's harness and model. It runs a fixed ladder: a story
  that pins `harness:` in frontmatter is honored verbatim; otherwise the policy options are filtered by capability-card
  fit, quota is consulted (observe-only: the reading is skipped for the Choice and `cox route` prints it as an info line,
  never a non-default pick - ADR 0011), and only a baseline table of **>= 12 measured rows** (3 stories x 2 harnesses x 2
  conditions) may pick a non-default harness, citing the rows behind it. Below that bar it keeps the policy default and
  says so. Routing is a `review_when` default in `policy.json`, never a hard rule; `cox story dispatch` routes a
  `harness: auto` story and records the Choice as `evidence.route`.
- **Board** (`board/`, `cox board`) is a read-only captain surface. `--out` writes a self-contained HTML snapshot that
  opens with no network; `--serve` serves it plus a same-origin `/data.json` the page refreshes every 10s. It renders
  fleet state, per-attempt scorecard, wakes and questions, steer budget, arena state, and the decisions waiting on the
  captain. It has no form, no button, no POST; the server answers GET only. Every action stays in the leader chat.
- **Lab** (`internal/lab`, `cox lab`) defines an experiment that turns a policy rule on/off and compares a scorecard
  metric across the two variants. It only computes and proposes: `assign` records the variant (as `evidence.lab`) and
  prints the value to apply by hand, `report` groups the metric by variant with n and variance and prints the v2 epic
  count against the retirement bar of 5, and `retire` writes a draft ADR. It never edits `policy.json`.

The tmux backend is not built (decision 0011 drops it); the `Backend` interface stays small enough for a community
adapter, but coxswain ships none.

## Quota (observe-only, M11)

`internal/quota` owns `coxswain.quota.v1`, the only quota type routing, the watcher, `cox state`, and the board read.
Two adapters fill it: a pinned `quota-axi` binary (schema 5 only, unknown on any drift, projection-only cache with no
PII) and a captain-declared manual file (leader-owned, expiring, `runway` always unknown). It is observe-only:
`cox quota`/`state`/`board`/`doctor`/`route` show it, the watcher raises `quota_low` (urgent or routine) and
`quota_health` wakes on a 5-minute poll, and `cox story dispatch` refuses an `exhausted_now` harness (`--force-quota`
overrides). It never changes the routing Choice (ADR 0011); the leader reroutes by hand with `cox story park` +
`cox story resume --harness <other>`, which records `evidence.reroute`. Full details: [`docs/quota.md`](quota.md).

## Visual review (M13, ADR 0008/0013)

cox owns the review artifact and the record; `lavish-axi` is an optional external review surface behind an adapter,
never forked (arena round 1). Generators render self-contained pages (shared board CSS, no CDN) plus a
`coxswain.artifact.v1` sidecar (kind, sources with shas, generator, `synthesis_sha` for arena) under
`<epic>/reports/visual`: `cox epic design --html`, `cox plan --html`, `cox arena synth --html`, `cox plan compare --html`,
and the board (`cox board --out`). `cox review open` runs `lavish-axi <file>`; `cox review poll --max` runs one bounded
`lavish-axi poll`, and every delivered feedback item becomes an `inbox.v1` fyi record for `_leader` plus a wake -
`review_feedback` (routine) or `review_decision` (urgent) for a decision/answer. The poll is a separate bounded
subprocess, never inside the watcher Tick. An answer (`answer <id> <value>`) is written only through
`cox arena answer`, guarded by the sidecar's synthesis sha, so a stale artifact never writes a shifted cell. Lavish acks
on delivery and clears its queue, so the record is written synchronously and a write failure reports the loss with the
raw feedback (the known window). Sharing to ht-ml.app is refused without `--share` (outward-facing). Without lavish,
everything works by path and chat. Full details: [`docs/review.md`](review.md), [`docs/adapters/lavish.md`](adapters/lavish.md).

## Where to go next

- `plans/260914-crewkit-v2-architecture/architecture.html` - the same material as a self-contained diagram.
- `docs/handoff.md` - the five leader/worker channels (brief, steer, control, status, checkpoint) in detail.
- `docs/adapters/` - the verified capability card and observed behavior for each backend and harness.
- `docs/protocol/` - the JSON schemas for `event.v1`, `checkpoint.v1`, `inbox.v1`, `wake.v1`, `fleet.v1`.
- `docs/routing.md`, `docs/board.md`, `docs/lab.md` - the three ADR-0011 features with command examples and limits.
