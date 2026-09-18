# 0010 - Measure before extend: what opens routing, board, lab, and tmux

- Status: Accepted
- Date: 2026-09-15

## Context

Phase 6 (M6) ships the two measurement tools - `cox scorecard` (attempt-aware) and `cox baseline run` (replay a story
from before its solution) - and stops there. The four conditional features the plan sketches for M6 - harness routing,
the captain board, the self-improvement lab, and a tmux backend - are deliberately NOT built in M6. The plan's roadmap
(section 8, the "Hoãn / Điều kiện mở lại" table, and 8.1) lists each with a loose reopen condition, but "when there is a
number" is not specific enough to gate a merge: without a named threshold or ruling, any of them could be argued open on
a single anecdote, which is exactly the risk phase-06 flags (a small baseline read as strong evidence).

The M5 dogfood confirmed the tools are the bottleneck, not the features: the arena lite round 2 exposed operational
bugs (M6 part A), and there is still no baseline sample and no routing signal. So M6 fixes the measurement and the
operational hygiene, and this decision pins the exact bar each deferred feature must clear before it is built, so the
gate is checkable rather than a matter of taste.

## Decision

M6 builds only `internal/scorecard` and `internal/baseline` (plus their `cmd/cox` commands) and the part-A operational
fixes. Each conditional feature stays unbuilt until its named bar is met **and** an ADR records the number or ruling
that opened it. No conditional feature is merged from evidence weaker than its bar.

1. **Routing (`internal/routing/`)** - automatic harness selection by policy + quota + capability card. Bar: a baseline
   table of **>= 3 distinct stories x 2 harnesses (claude, codex) x the bare and v2 conditions**, with sha and the
   leader-fix count per row, plus a capability card per harness. The routing default must ship as a `review_when`
   default, never a hard rule, and cite the specific rows. A single replay (`n=1`, the M6 smoke) does not qualify.
2. **Board (`board/`)** - an HTML review surface for the captain. Bar: a **direct captain request**. It is never built
   as a remedy for a core bug (fix the core instead), so a bug report does not open it.
3. **Lab (`internal/lab/`)** - experiments that turn a policy rule on/off and auto-retire rules. Bar: **>= 5 epics run
   on v2 with scorecards**, so there is enough history for an experiment to mean anything.
4. **tmux backend (`internal/adapter/backend/tmux/`)** - a no-Orca/no-herdr backend. Bar: **a real external user without
   Orca or herdr** (the first GitHub issue asking for it) **or a direct captain request**. The `Backend` interface is
   already small enough to satisfy without touching the core (decision 0002).

Each bar, when met, is recorded in its own ADR that cites the concrete number or the captain ruling before any code
lands.

## Consequences

- M6 is scoped to measurement + hygiene; the four features wait for evidence, so none ships on a hunch.
- The gates are checkable: a reviewer can confirm the baseline row count, the epic count, or the captain request before
  approving a conditional feature, instead of arguing about whether there is "enough" signal.
- This decision **supersedes** the reopen conditions for these four items in plan section 8 (the "Hoãn / Điều kiện mở
  lại" table and the self-improvement-lab row) and 8.1: those rows now point here for the exact bar. The plan's roadmap
  M6 row still describes the intent; this ADR is the authority on when each opens.

## Review when

When any bar is met - a third baseline story lands, a fifth v2 epic completes, or the captain asks for the board or tmux
- open the corresponding feature's own ADR with the citation and build it. Reopen this decision only if a bar itself
proves wrong (e.g. three stories turn out to be too few to set a routing default with acceptable variance).
