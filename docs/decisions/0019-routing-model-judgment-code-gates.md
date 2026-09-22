# 0019 - Routing: the rule match is a model's judgment, the gates and ranking are code

- Status: Accepted (captain, 2026-09-21)
- Date: 2026-09-22

## Context

The firstmate deep-dive (`research/reports/coxswain-vs-firstmate.md` §2.6; BACKLOG "harness: auto guesswork") shows how
firstmate routes a worker: `AGENTS.md` §4 makes the rule *match* a model's judgment from the task's content, while
deterministic code owns validation, the gates, the ranking, and the spawn safeguards
(`.agents/skills/quota-array-dispatch/SKILL.md`, `bin/fm-dispatch-resolve.sh`, `bin/fm-quota-choose.sh`). Coxswain had
only the baseline ladder (ADR 0011): card-fit then a policy default, with quota observe-only. `harness: auto` was a
guess with no posture behind it, and a story dispatched onto a harness with 11% weekly quota and projected exhaustion
(leader-findings.md finding 7) because nothing ranked candidates by completion economics.

The captain's standing ruling (2026-09-21): **routing must stay dynamic** - the rule match is a model's judgment (the
leader in its own turn, or firstmate's Jev typed path as one short tool turn); Go owns only validation, the three gates,
the ranking, and the spawn safeguards. Never a fixed matcher in Go.

## Decision

Worker routing is a captain-authored posture applied by a model's judgment and enforced by code (DESIGN wave-4 item 10).

1. **Rules and profile arrays live in policy.** `routing.rules[]` (a natural-language `when`, a `profiles[]` array of
   `{harness, model, effort, provider, floor}`, and `approval: none|captain`) and `routing.default_profiles[]`. They are
   validated - structurally at `policy.Load` (empty array, duplicate profile, malformed JSON, bad approval/effort/floor
   class, bad provider id) and against the capability cards at dispatch (unknown harness, an effort a card does not
   support). A malformed rule refuses dispatch naming the field; it is never selected around.
2. **The match is a model's judgment, never a Go matcher.** The default path: the leader reads the rules at
   decomposition and writes `route: rule=<n>` (or `route: override`) into the story. The opt-in typed path:
   `cox route --brief` sends the brief and each rule's `when` to typesafe.ai System One (`jev-latest`) as one Choice
   question when `TYPESAFE_API_KEY` is present and `routing.rules` is non-empty; code applies the confidence floor 0.6
   and validates the probabilities. The model never sees quota, catalogs, approvals, or profiles. Off (no key) is one
   stderr line and no network; the key is used only as a request header, never printed, logged, or placed on argv.
3. **Code owns the mechanical part.** After the match, `routing.Decide` resolves the profile array through three
   orthogonal gates - eligibility (credential attention or `exhausted_now`), reasoning-class floor, runway feasibility
   (`projected_exhaustion` below `routing.min_runway_seconds`, default 4h) - then a single `spendPriority` argmax over
   the quota-axi reading. A tie within `routing.tie_epsilon` (default 0.01), an approval-gated rule, or no rankable
   candidate returns escalate: `cox story dispatch` stops with a captain-facing question and never dispatches. Every
   candidate is accounted for, and a pin the matched rule forbids is refused.
4. **effort defaults by kind.** `routing.effort` maps a story kind to a reasoning-effort class (`scout` xhigh, `ship`
   low, `arena` high), overridable per story with `effort:` frontmatter.
5. **Evidence.** Dispatch records `evidence.route` (`rule`, `resolver` leader|jev|none, `confidence`, `harness`,
   `model`, `effort`, `spendPriority`, `candidates_considered`), so the decision is auditable.

## Consequences

- Routing is dynamic and never a fixed Go matcher: the captain writes the posture in natural language, a model matches
  it, and code enforces the economics - so a story never dispatches onto a tight or projected-to-exhaust harness, and a
  genuine tie or an approval-gated rule reaches the captain instead of a silent pick (finding 7).
- The baseline ladder (ADR 0011) stays underneath, observe-only, as the default when no rule matches: below the bar
  routing still only card-filters and keeps the default, so nothing regresses for an epic without rules.
- The gates and ranking are cheap, orthogonal, and inspectable; `spendPriority` is quota-axi's own scalar, never a
  recomputed composite. Unknown is never read as zero: a candidate with no comparable `spendPriority` is eligible but
  unrankable.
- The typed path is a convenience, not a dependency: it is off without a key, every outcome exits 0, and the leader
  match path is unchanged. The key never leaves the request header.

## References

- `research/reports/coxswain-vs-firstmate.md` §2.6; `reports/arena/synthesis.md` (Rejected alternatives 2);
  `reports/leader-findings.md` finding 7.
- firstmate `AGENTS.md:214-235`, `.agents/skills/quota-array-dispatch/SKILL.md`, `bin/fm-dispatch-resolve.sh:1-110`,
  `bin/fm-quota-choose.sh:1-45`, `docs/configuration.md:443-540`.
- ADR 0011 (the baseline ladder stays; quota observe-only), ADR 0005 (baseline deferred).
