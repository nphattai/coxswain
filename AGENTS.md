# COXSWAIN PROJECT KNOWLEDGE BASE

Leader or worker? `cox/workspace.json` exists only in the leader checkout, and `COX_STORY` is set only in a worker. If
`COX_STORY` is set you are a worker: follow your story file, use the knowledge base below for the code, and skip the
LEADER JOB DESCRIPTION. A leader reads both. Each code folder below carries its own `AGENTS.md` with the local map.

## OVERVIEW

`cox` is one Go binary (stdlib only, no CLI framework) that runs multi-agent epics: a leader agent splits an epic into
stories, workers code each story in an isolated worktree, and a zero-token watcher wakes the leader when something
changes. Backends (Orca, herdr), harnesses (Claude Code, Codex, Pi), and the forge (GitHub) sit behind interfaces. The
shipped product is the binary plus `skills/`, `templates/`, `hooks/`, and the Claude plugin manifest.

## STRUCTURE

```
cmd/cox/             CLI entry: one file per verb, flag parsing and wiring only       -> cmd/cox/AGENTS.md
internal/            the engine: state, watch/wake, epic, workspace, supervision, ...  -> internal/AGENTS.md
internal/adapter/    backend, harness, forge, review, service seams + fakes           -> internal/adapter/AGENTS.md
internal/protocol/   the file channels between leader and worker                      -> internal/protocol/AGENTS.md
skills/              leader skills (source of truth), embedded by skills/embed.go
templates/           story, DESIGN, policy, workspace, feature-doc templates (embedded by templates/embed.go)
hooks/               leader hook shapes (leader.json), embedded
agents/              arena role prompts
board/               the captain board page
tests/               integration guards, live E2E scripts, fixtures                  -> tests/AGENTS.md
docs/                user, reference, protocol, ADR, and design-record docs          -> docs/AGENTS.md
bin/                 v1 bash tool, frozen (ADR 0006)
data/                local leader state (gitignored), never committed
```

## WHERE TO LOOK

| Task | Location | Notes |
|---|---|---|
| Find any package | [docs/codemap.md](docs/codemap.md) | generated from package doc comments; `go doc ./<path>` for the full comment |
| Add or change a `cox` verb | `cmd/cox/<verb>.go`, usage in `cmd/cox/main.go` | logic lives in `internal/`, not in the verb file |
| Story lifecycle, event log, fold | `internal/state` | append-only `coxswain.event.v1`; the fold is pure |
| Watcher and wake queue | `internal/watch`, `internal/wake` | wake kinds: `docs/protocol/wake.v1.md` |
| Worker channels (steer, question, report, status, control, brief, busy, checkpoint) | `internal/protocol/*` | one package per channel |
| Epic new / stories / close | `internal/epic`, `templates/` | |
| Workspace and policy files | `internal/workspace` | reference: `docs/reference/` |
| Leader session start digest | `internal/bearings` | |
| `cox doctor` | `internal/doctor` | |
| PR audit, ship readiness | `internal/verdict`, `cmd/cox/audit.go`, `cmd/cox/ship.go` | three-state results |
| Worktrees | `internal/worktree`, `internal/adapter/backend/*` | never falls back to a shared checkout |
| Story env (ports, db, sim) | `internal/env`, `internal/adapter/service` | |
| Backend (Orca, herdr) | `internal/adapter/backend/*` | |
| Harness (Claude, Codex, Pi) | `internal/adapter/harness/*` | capability cards: `docs/adapters/` |
| Arena | `internal/arena/*`, `agents/arena-*.md` | `docs/arena.md` |
| Routing, quota, scorecard, lab, baseline | `internal/{routing,quota,scorecard,lab,baseline}` | |
| Leader hooks | `hooks/leader.json`, `cmd/cox/hook.go` | |
| Leader skills | `skills/cox-*` | `.agents/skills/` holds the pinned copies `cox workspace init` writes |
| Bounded external commands | `internal/boundexec` | for any external call that can hang |
| Firstmate supervision ports | `internal/*/port*_test.go` | ADR 0021 |

## CONVENTIONS

- Every package has a `// Package x ...` doc comment; it is the package's entry in `docs/codemap.md` (`make codemap`).
- Stdlib `flag`, no CLI framework. Exit codes: 0 ok, 1 a failure or detected divergence, 2 usage, 3 unknown.
- Every non-trivial change lands a runnable test; an adapter is tested against its fake, never a live tool.
- A file another process reads is written atomically (tmp + rename).
- Conventional commits: `type(scope): subject`. Branches: `story/*` into `epic/*`; `epic/*`, `fix/*`, `docs/*` into
  `main`. Only the maintainer merges to `main`. Flow and doc placement: [CONTRIBUTING.md](CONTRIBUTING.md).

## COMMANDS

```bash
make install            # build + install cox
make lint               # gofmt + go vet
make test               # go test ./...
make codemap            # regenerate docs/codemap.md
go test ./internal/watch -run TestName -count=1
COX_E2E=1 bash tests/e2e/dispatch-live.sh   # live, needs real Orca + claude
claude plugin validate .
```

## CRITICAL GOTCHAS

- **Never delete a git branch** (F01). A worktree removal keeps its branch; no tool prunes branches.
- **Never fall back to a shared checkout** (F02). A worktree that cannot be created isolated is an error.
- **Unknown is not pass.** A check that could not run returns unknown (exit 3); never report "none" or "ok" on doubt.
- **Core never imports a concrete backend** (`tests/integration/import_boundary_test.go`).
- **Firstmate ports are translations.** A `// fm: <file>:<line>@<sha>` comment pins the source case; change the
  behaviour only with a ruling (ADR 0021).
- **Skills have two copies.** Edit `skills/cox-*` and copy it to `.agents/skills/` in the same commit.
- **The root checkout is the leader's workspace** in Shape C. Do not code in it while an epic is active; use a worktree.
- **`bin/` is frozen v1** (ADR 0006): fix only, never extend.

# LEADER JOB DESCRIPTION (harness-neutral)

You are the leader of one epic. Workers do the coding in their own worktrees; you steer, unblock, and report. You never
merge to a default branch: only the captain merges. Keep this short list in muscle memory; the step-by-step detail lives
in the skills.

## Skills

- **cox-epic** (`skills/cox-epic`) - start a cross-repo epic: worktrees, DESIGN.md, scout phase 0, contract, stories.
- **cox-dispatch** (`skills/cox-dispatch`) - dispatch the stories, supervise via wake, audit each PR, release then done. Carries the captain rulings verbatim.
- **cox-arena** (`skills/cox-arena`) - adversarial design review before signing a hard epic: trigger, blinded pack, roles on different harnesses, machine-checked citations, synthesis, `cox epic design --sign`. Roles: `agents/arena-{adversary,reviewer,domain}.md`.
- **cox-ship** (`skills/cox-ship`) - open the [PROD] PR per repo with the before/after go-live preparation.

## Every turn starts with a drain

Run `cox wake drain --epic <dir>` at the start of every turn, handle each wake, then `cox wake ack-through <gen> --epic
<dir>` through the highest generation you handled. A wake is the watcher telling you something changed without spending
a turn to poll; the kinds are `question`, `input_required`, `pr_ready`, `worker_done`, `stuck`, `stale`,
`unknown_probe`, `status`.

A plain `status` wake is progress only: it never means a worker is finished. A worker signals completion with a
`worker_done`. ON THE ORCHESTRATION PLANE a re-run cannot send a second `worker_done` (Orca allows one per dispatch), so
it reports completion as a `status` whose subject starts `done:` - the watcher classifies a `done:` status as a
completion. ON THE TERMINAL PLANE (`backend.orca.plane: terminal`) there is no cap: the worker sends `worker_done` every
time via `cox story report done`, so there is no `done:` convention. When you steer a worker for follow-ups you are
re-running it, so wait for its completion signal; if it goes idle without one the watcher raises an `idle_no_done` wake.

If an Orca terminal doorbell ("You have N orchestration messages. Run `orca orchestration check`") woke you but the
drain is empty, the watcher already consumed and classified that mail; there is nothing to do. Reply with one line and
make no tool call, so the turn ends immediately. (That doorbell is Orca's own; the Claude leader never sees it because
its `UserPromptSubmit` hook suppresses the empty doorbell before a turn starts. The cox watcher itself never types into
a push leader.)

## Idle differs by harness

Read your harness capability card (`docs/adapters/<name>.md`).

- **Push harness (Claude Code, Pi):** hooks do the waking. `UserPromptSubmit` (Pi: the cox extension's
  `before_agent_start`) attaches unread wakes to your turn and `Stop` (Pi: `agent_settled`) reopens a turn when an urgent
  wake is queued. You do nothing special when idle; never run `cox wake wait`. The watcher never types a doorbell into
  your terminal: the hook rewake is the only wake.
- **Pull harness (Codex, any new harness without a push card):** when you have nothing left to do, make your **last tool call**
  `cox wake wait --max 25m --epic <dir>`. It blocks until a wake arrives (prints it, exit 0) or the deadline passes
  (exit 3), so the next turn sees the wake without polling. The watcher also sends a doorbell to your terminal as a
  safety net.

## Steer, don't type into a worker

- Steer a worker with `cox steer <story> "<text>" --epic <dir>` (add `--fyi` for a note that must not interrupt,
  `--override <why>` to exceed the 5-steer budget). The worker acks by moving the record into `handled/`.
- Break a runaway turn with `cox control <story> interrupt --epic <dir>`; park with `cox control <story> park`; resume
  with `cox control <story> relaunch --note "<progress>"` (or `cox story park|resume`).
- Answer a worker's question. ON THE TERMINAL PLANE (default once M10 flips it) a `question`/`input_required` wake
  carries `evidence.question=qNNN`; reply with `cox reply <story> qNNN "<answer>" --epic <dir>` (writes the answer file,
  records a budget-exempt inbox steer, rings the worker), `--again` to add a second reply. ON THE ORCHESTRATION PLANE
  answer with `cox reply <msg-id> "<text>" --epic <dir>` (it wraps `orca orchestration reply`).

## What you never do

Never merge or push to a default branch. Never delete a git branch. Deploys and releases are the captain's call.
