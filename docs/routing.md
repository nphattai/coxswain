# Routing

`cox route` picks the harness (and model) for a story. It is a `review_when` default, never a hard rule (decision
0011): it can filter and default, but it may only choose a non-default harness once there is enough baseline evidence,
and every decision prints the reasons and the rows it cited.

## The ladder

`internal/routing.Decide` runs these rungs in order and stops at the first that settles the harness:

1. **Story frontmatter.** A story that pins `harness:` (and optionally `model:`) is honored verbatim - no routing.
2. **Card fit.** The policy `harness.worker` options are filtered to those whose capability card fits the worker role.
   An option with no adapter/card is dropped with a reason.
3. **Quota.** Consulted when machine-readable. No harness exposes a readable quota today (claude's status line is not
   scraped), so every reading is unknown and skipped with a reason - the decision never looks like quota was consulted
   and found full.
4. **Baseline.** Only a baseline table with **>= 12 measured rows** (3 stories x 2 harnesses x 2 conditions) may pick a
   non-default harness, and it must cite the rows behind the choice. Below the bar the default is kept.
5. **Policy default.** `harness.worker.default`.

Only rows with a real result (`pass`/`fail`) count as measured; a `dry-run` row is intent, not evidence.

## Commands

```
cox route --story <id> --epic <dir>          # print the choice, reasons, and cited rows
cox route --story <id> --epic <dir> --json   # the Choice as JSON
```

`cox story dispatch` routes a story whose frontmatter is `harness: auto` through the same `Decide`, and records the
Choice as `evidence.route` on the working event.

Example, with the current n=1 baseline table:

```
$ cox route --story some-auto-story --epic epics/v2
route some-auto-story: harness=claude
  - card-fit candidates: claude, codex
  - quota for claude unknown, skipped (no machine-readable quota endpoint ...)
  - baseline rows 1 < 12: default kept
  - policy default harness=claude
```

## Policy

`policy.json` carries a `routing` section documenting the bar; it is a `review_when` default, not a behavioural rule:

```json
"routing": {
  "default": "policy",
  "review_when": "baseline rows >= 12 (3 stories x 2 harnesses x 2 conditions)"
}
```

## Limits (decision 0011)

- Routing never becomes a hard rule; the bar and the citation requirement are enforced in `Decide` and in the policy.
- Until the downstream baseline (>= 3 stories x 2 harnesses x 2 conditions) lands, routing can only card-filter and keep
  the default. See `docs/baselines/README.md`.
