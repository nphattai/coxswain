# 0011 - Captain ruling opens routing, board, and lab; tmux backend is dropped

- Status: Accepted
- Date: 2026-09-15

## Context

Decision 0010 gates four conditional features on a named bar each: routing on a baseline table of >= 3 stories x 2
harnesses x 2 conditions, board on a direct captain request, lab on >= 5 v2 epics with scorecards, tmux on an external
user or a captain request. On 2026-09-15, after the M0-M6 progress review, the captain ruled: build routing, board, and
lab in the next round; do not build tmux.

Where the ruling and the numeric bars stand:

- Board: the bar (a direct captain request) is met by this ruling.
- Routing: the numeric bar is NOT met. The baseline table has one smoke row (n=1, `docs/baselines/claude-2026-09-15.md`);
  the three downstream stories for the real baseline are still to be chosen. The captain's ruling opens building the routing
  mechanism; it does not supply the numbers.
- Lab: the numeric bar is NOT met (one v2 epic, `epics/v2`, has scorecards). The ruling opens building the lab tooling.
- tmux: dropped by the ruling. Orca is the default and herdr the optional backend (decision 0002); the `Backend`
  interface stays small enough for a community adapter, but coxswain does not ship one.

## Decision

1. M8 builds `internal/routing`, `board/`, and `internal/lab` (phase-08). Their mechanism is opened by this ruling; their
   authority to change behaviour is still bounded by evidence:
   - Routing ships only as a `review_when` default in policy, never a hard rule, and every routing decision prints the
     rows it cites. Until the baseline table has >= 3 stories x 2 harnesses x 2 conditions, routing may only choose
     among harnesses whose capability card fits the role and must fall back to the policy default, saying so.
   - Lab computes and reports; it never edits policy or retires a rule on its own. A retirement is a proposal file the
     captain reads. With fewer than 5 v2 epics the report must print the epic count next to every number.
   - Board is read-only. It renders state; every action stays in the leader chat.
2. The tmux backend is removed from the roadmap. Plan section 7 and 8.1 and decision 0010 item 4 are superseded on this
   point; a future external request is handled as a community adapter, not a coxswain milestone.
3. M7 (phase-07) lands first: it completes the state machine (fail, cancel, reconcile), enforces capability cards, and
   wires the GitHub forge into `cox state`, scorecard, and ship. Routing depends on the enforced cards, and the board
   depends on forge facts in the fleet view, so M8 depends on M7.

## Consequences

- Three features stop being "conditional" and become scheduled work, with their behavioural limits written down here so
  a small sample cannot silently become a rule.
- Anyone reading the baseline table or the lab report sees the sample size next to the number.
- No tmux code, docs, or CI target is added.

## Review when

When the downstream baseline (>= 3 stories) lands: revisit whether routing may set a non-default choice. When the fifth v2
epic completes: revisit whether lab may propose retirements without the epic-count caveat. Reopen tmux only on a real
external request, as a community contribution.
