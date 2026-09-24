# 0021 - Supervision is translated from firstmate, case by case

- Status: Accepted (captain, 2026-09-24)
- Date: 2026-09-24

## Context

The previous port (epic `cox-firstmate`, PR #21) carried firstmate's supervision into cox from a prose comparison of
its mechanisms and marked about half of them "adapt". Cox got firstmate's shape without the incidents that shaped it.
Dogfood then hit B-50 (a first-turn stop without a report), B-51 (the Stop hook never fired and the busy record stayed
busy) and B-53 (a false runaway over a reply the worker had already consumed). Firstmate's regression suites already
pinned all three as cases. Finding and fixing them one at a time in dogfood was the wrong method.

Adopting firstmate itself was tried and rejected: one home for every project and a backlog-first model do not fit
company work across many repositories with several epics in flight.

## Decision

Captain ruling 2026-09-24 (epic `cox-supervision-port`):

1. **Only the epic/story flow and the arena are cox's own.** Everything else is supervision and follows firstmate
   verbatim. That covers:
   - the watcher's triage and wake passes;
   - the turn boundary (Stop guard, auto-arm, waiter);
   - the busy record;
   - the wake queue and drain;
   - the decision fold;
   - control;
   - session start and the curated memory notes.
2. **The source is firstmate's regression corpus, not its prose.** The pin is `kunchenguid/firstmate` at `1e0e773`
   (2026-09-23), with read-only clone paths such as `tests/fm-watch-triage.test.sh`, `tests/fm-wake-queue.test.sh`,
   `tests/fm-watcher-lock.test.sh`, `tests/fm-busy-adapter-wiring.test.sh`, `tests/fm-session-start.test.sh` and
   `docs/supervision-protocols/`. Each firstmate case becomes one `t.Run("FM/<suite>/<case>")` carrying a
   `// fm: tests/<file>.test.sh:<line>` citation. Names, thresholds, defaults and wording are ported as-is unless cox
   already had a name for them.
3. **No case is waived.** A cox ADR, policy default or invariant that a firstmate case contradicts is superseded by the
   case. The story that turns the case green adds a one-line supersession note to the ADR (listed under Consequences).
4. **n/a is explicit.** A case about a firstmate-only surface is not translated; its report row names the reason. Those
   surfaces are tmux pane churn, herdr, secondmates, relay/X, the away daemon, remote homes and calm.
5. **The nearest cox observable stands in where cox has no counterpart**, and the story says so. Examples:
   - an active no-mistakes run-step is the story PR's CI running at the live head;
   - firstmate's Pi guard cases are translated into the extension's node suite;
   - firstmate's three memory files stay three files (`cox/notes/captain.md`, `captain-shared.md`, `learnings.md`)
     with its 7,500-token budget (ceil(bytes/3), firstmate `docs/configuration.md:262-268`).

## Method

- **Wave 1** translated five firstmate suites into Go tests behind a `port` build tag:
  - 510 cases in total: 144 green, 366 red, 346 n/a;
  - the union of the red cases was the complete gap inventory (`epics/cox-supervision-port/reports/`).
- **Waves 2 and 2b** implemented to green, one story per Go package. Each story removed the tag from a port file once
  it reached zero red. The last story (`w2-watch-decisions`, PR #38) closed at 0 red with every tag gone, so
  `go test ./...` runs the whole corpus with `-count=3 -race`.
- **The kill test (epic AC 3)** used the real watcher on a temp epic. A worker whose `cox` binary is `/usr/bin/false`
  still woke the leader with an urgent `stale` wake 36 s after dispatch, inside the 300 s grace
  (`reports/evidence/kill-test/`).

## Re-diff rule

At every epic close, a scout diffs firstmate's `tests/` and `docs/` since the pin and files each new or changed case as
a backlog row. The pin moves forward only with that scout, so cox never silently drifts from, or silently adopts, a
newer firstmate.

## Consequences

Supersession notes added during the epic, each citing the firstmate case or script that decided it:

- `docs/ARCHITECTURE.md` "Unknown stays unknown":
  - unknown is never proof;
  - a quiet worker whose state stays unknown past a bound surfaces as an urgent `stale` / `unknown_probe` wake;
  - it then escalates on the wedge ladder (fm-watch-triage).
- [0014](0014-turn-boundary-guarded.md) decision 1, the supervision need:
  - the need is open stories plus registered process-event sources and custom checks;
  - healthy is an identity-matched pid plus a beacon within max(300 s, poll+60 s);
  - Claude runs the Stop auto-arm before the guard;
  - firstmate's `TURN WOULD END BLIND` banner is used.
- [0014](0014-turn-boundary-guarded.md) decision 2, the block budget: per epic and session, charged per auto-arm epoch,
  never reset by a prompt.
- [0014](0014-turn-boundary-guarded.md) decision 4, the alerts channel:
  - `alerts.channel` defaults to `auto` (on; osascript on macOS);
  - it is a directive list;
  - each invocation is bounded by a process-group timeout.
- [0016](0016-busy-state-harness-owned.md):
  - `BusyTurnMax` is 3600 s, and a crossed bound starts the wedge timer;
  - a still-busy worker escalates after `StaleMin` (240 s, firstmate `STALE_ESCALATE_SECS`);
  - a busy record is not proof of work when the endpoint is gone (B-51);
  - the armed gen lives in a `<story>.busy-gen` sidecar;
  - `busy.Classify` returns firstmate's `<state> <source>` with its reason;
  - a gen-bound progress marker records native activity apart from semantic state, and the watcher's busy-turn age
    reads it.
- [0020](0020-pi-parity-global-packages-workers-workspace-leader.md) decision 5: an installed extension hash is not
  proof it is loaded; only the running extension's marker is.
- Adapter and protocol pages:
  - `docs/adapters/claude.md`: `SessionEnd` applies idle rather than retiring the record.
  - `docs/adapters/codex.md`: the per-turn block budget.
  - `docs/protocol/wake.v1.md`:
    - the status-line grammar replaces the v1 free-text regex;
    - `stale` and `unknown_probe` are urgent;
    - corrupt rows are retired;
    - the new urgent `check` kind carries a registered check's output.

Rules that came out of the port:

- **The watcher runs registered custom checks.** `cox watch check register` binds a check to its bytes; the watcher
  runs exactly those bytes every `COX_CHECK_INTERVAL` (300 s), bounded by `COX_CHECK_TIMEOUT` (30 s), and kills the
  check's process group afterwards. Every watcher cycle's close is one record in `.cox/watch-cycle-exits.log`.
- **Captain relevance is one predicate**, `decision.CaptainRelevant`, case-insensitive as in firstmate. The watcher's
  signal pass and `cox wake drain` fold status history through the same `internal/protocol/decision`.
- **Detached children never re-exec `os.Executable()` unguarded.** A re-exec under `go test` runs the test binary
  instead (leader finding 11, B-67: 519 orphaned `cox.test` processes). A detached child resolves `$COX_BIN` first and
  refuses to detach when `testing.Testing()`.
- **Supervision behavior changes by translating a firstmate case first.** A new supervision behavior, or a change to an
  existing one, starts from a translated firstmate case (or a re-diff row). A cox-only supervision rule needs a captain
  ruling that names what it supersedes.
