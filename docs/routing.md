# Routing

Worker routing is a captain-authored posture applied by a model's judgment and enforced by code. Rules with a
natural-language `when` and quota-ranked profile arrays live in `policy.json`; the *match* is a model's judgment (the
leader at decomposition, or the opt-in Jev typed path); Go validates the rules, applies three gates and the
`spendPriority` ranking over the quota reading, escalates ties and tight fleets to the captain, records the route as
evidence, and never invents a match itself. The baseline ladder (ADR 0011) stays underneath as the default when no rule
matches.

## The precedence

`internal/routing.Decide` resolves the harness/model/effort in this order and stops at the first that settles it:

1. **Captain override.** `--harness`/`--model`/`--effort` on `cox story dispatch` wins outright and skips routing.
2. **Story pin.** A story that pins `harness:` (and optionally `model:`) is honored verbatim - **unless** it also carries
   a matched rule whose profile array does not contain that harness, which is a contradiction and is refused with the
   rule text.
3. **Matched rule.** The story's `route:` frontmatter (`rule=<n>`, 1-based, or `override`), written by the leader (or by
   the typed path), selects a rule; its profile array is resolved through the gates and the ranking below.
4. **Default profiles.** `routing.default_profiles` when no rule matched (or the leader wrote `route: override`).
5. **Baseline ladder.** Today's ladder (below), when there is no rule and no default array.

### The three gates, then spendPriority

Every profile in the matched (or default) array becomes a candidate; the array is resolved by three cheap orthogonal
gates, then a single quota-perspective ranker. Every candidate is accounted for with its result - none is omitted.

1. **Eligibility.** A candidate whose quota reading needs credential attention, or whose runway is `exhausted_now`, is
   ineligible with that reason.
2. **Reasoning-class floor.** A profile `floor` (one of `low`\|`medium`\|`high`\|`xhigh`\|`max`) the story's `effort` does
   not meet makes that candidate ineligible.
3. **Runway feasibility.** A `projected_exhaustion` reading with less usable runway than `routing.min_runway_seconds`
   (default 4h) is ineligible, even with the highest `spendPriority`.

Among the eligible candidates, the one with the highest known `spendPriority` wins (higher is better: paid allowance on
track to reach reset unused). A candidate with no comparable `spendPriority` is eligible but unrankable - listed, never
chosen. A tie within `routing.tie_epsilon` (default 0.01), an approval-gated rule (`approval: captain`), or no rankable
candidate returns **escalate**: `cox route` prints `ESCALATE` and `cox story dispatch` stops with a captain-facing
question instead of dispatching. The pick is never broken by array order or harness name.

### effort defaults by kind

`routing.effort` maps a story kind to a default reasoning-effort class (`scout` → `xhigh`, `ship` → `low`, `arena` →
`high` when unset). A story's own `effort:` frontmatter overrides it.

## The baseline ladder (ADR 0011)

When no rule and no default array apply, `Decide` falls back to the baseline ladder, which stays observe-only:

1. **Card fit.** The `harness.worker` options are filtered to those whose capability card fits the worker role; an option
   with no adapter/card is dropped with a reason.
2. **Quota (observe-only).** The reading is recorded but never changes the pick here.
3. **Baseline.** Only a baseline table with **>= 12 measured rows** (3 stories x 2 harnesses x 2 conditions) may pick a
   non-default harness, and it must cite the rows behind the choice. Below the bar the default is kept.
4. **Policy default.** `harness.worker.default`.

Only rows with a real result (`pass`/`fail`) count as measured; a `dry-run` row is intent, not evidence.

## Commands

```
cox route --story <id> --epic <dir>            # print the choice (or ESCALATE), reasons, and every candidate's gates
cox route --story <id> --epic <dir> --json     # the Choice as JSON
cox route --candidates claude:opus,codex:gpt --epic <dir>   # first quota-eligible candidate (or none, exit 1); no side effects
cox route --brief <story.md> --epic <dir>      # opt-in typed match (Jev); off with one stderr line when no key
```

`cox story dispatch` routes a story whose frontmatter is `harness: auto` (or that carries a `route:` match) through the
same `Decide`, records the route as `evidence.route` on the working event (`rule`, `resolver`, `confidence`, `harness`,
`model`, `effort`, `spendPriority`, `candidates_considered`), and stops without spawning when routing escalates.

### Typed resolution (opt-in)

`cox route --brief` lets typesafe.ai's System One model (Jev) make the rule *match* in one short tool turn, when
`TYPESAFE_API_KEY` is present (the environment wins over the workspace's gitignored `.env`) and `routing.rules` is
non-empty. The model is shown only the story's task sections (`## Goal`, `## Scope`, `## Acceptance criteria` under
the story's title, led by `Brief kind: scout (report only)` for a scout; the whole file when it has none of them) and
each rule's `when`; it never sees quota, catalogs, approvals, confidence floors, or profiles. Code then validates the
probabilities, applies the confidence floor (0.6 on the answer confidence, or a rule's own `min_confidence` on that
rule's probability, falling to the most probable other option that clears its own floor, printed as `fallback:`), and
runs the matched rule through the same gates and ranking (firstmate 795e4b5). Off (no key) prints one stderr line and exits 0 with the leader path unchanged; every
outcome exits 0. The key is used only as a request header - never on argv, in a log, or in output.

## Policy

```json
"routing": {
  "default": "policy",
  "review_when": "baseline rows >= 12 (3 stories x 2 harnesses x 2 conditions)",
  "rules": [
    { "when": "a database schema or migration change", "profiles": [
      { "harness": "codex", "model": "gpt-5.6-sol", "floor": "high" },
      { "harness": "claude", "model": "claude-opus-4-8" }
    ] },
    { "when": "a risky production change", "approval": "captain", "profiles": [
      { "harness": "claude" }
    ] }
  ],
  "default_profiles": [ { "harness": "claude" }, { "harness": "codex" } ],
  "effort": { "scout": "xhigh", "ship": "low", "arena": "high" },
  "min_runway_seconds": 14400,
  "tie_epsilon": 0.01
}
```

A malformed rule (unknown harness, an effort a card does not support, a duplicate profile, an empty array, malformed
JSON) is a load or dispatch error that names the field and refuses to dispatch - it is never selected around. See
[`policy.json` reference](reference/policy-json.md) for every field.

## Limits (ADR 0011 / ADR 0019)

- The rule *match* is always a model's judgment; Go owns only validation, the gates, the ranking, and the escalation - it
  never matches a `when` itself ([ADR 0019](decisions/0019-routing-model-judgment-code-gates.md)).
- The baseline ladder stays observe-only: below the bar it can only card-filter and keep the default
  ([ADR 0011](decisions/0011-captain-opens-routing-board-lab-drops-tmux.md), [ADR 0005](decisions/0005-defer-baseline-to-m6.md));
  `cox baseline run` owns the executable measurement path.
