# 0004 - Arena adversary runs a different harness than the leader

- Status: Accepted
- Date: 2026-09-15

## Context

v1 arena fixed three roles across three harnesses over two rounds and re-dispatched on consensus. The review and
LLM-as-judge literature show that different personas or harnesses do not automatically produce independent errors, that
count is not independence, and that fixed thresholds are untested hypotheses. The one property worth guaranteeing is that
the adversary is not the same mind that wrote the design.

## Decision

The harness set for arena is `{claude, codex}`, extensible later. The single rule is that the adversary runs a harness
different from the one that wrote DESIGN.md (the leader); `cox` derives this from `policy.yaml` rather than hard-coding.
Default: leader claude implies adversary codex, and vice versa. Reviewer roles (precedent, journey) run the same harness
as the leader in a fresh session. A second round happens only for an unresolved `epic-blocking` claim or a factual
conflict between roles, never because roles agreed.

## Consequences

- Adversary independence is structural (different harness), not assumed from persona wording.
- Adding a harness is adding an adapter plus a policy entry; the rule does not change.
- Consensus no longer triggers re-dispatch, removing a source of wasted rounds.
- Calibration is a column in the synthesis table (which claims the captain accepted), not an automatic mechanism (P7).

## Review when

Reconsider once several real arenas have run and the calibration column shows which roles earn their cost, or if a third
harness changes the independence argument.
