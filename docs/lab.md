# Lab

`cox lab` defines a self-improvement experiment that turns a policy rule on/off and compares a scorecard metric across
the two variants. It only computes and proposes (decision 0011): it never edits `policy.json`, and a retirement is a
draft the captain reads.

## Commands

```
cox lab new <name> --rule <policy.key> --metric <scorecard.metric> --epic <dir>
cox lab assign <name> --story <id> --epic <dir>
cox lab report <name> --epic <dir> [--json] [--no-forge]
cox lab retire <name> --epic <dir>
```

- **new** writes `cox/lab/<name>.json` (at the workspace root) with the `on|off` variants. `--metric` must be a
  scorecard metric: `wall_working_s, wall_parked_s, steers, questions, resumes, tokens_in, tokens_out, cost_usd,
  ci_wall_incl_queue_s`.
- **assign** alternates the variant by assignment order (idempotent per story), records it as `evidence.lab` on the
  epic event log, and prints the rule value to apply **by hand** - `policy.json` is not changed.
- **report** groups the metric across the assigned stories' latest attempts by variant, printing `n`, mean, and
  variance, followed by `epics on v2: <k> (bar for retirement: 5)`. Below the bar it prints the caveat that every
  number is read next to `n` and the epic count, never as a rule.
- **retire** writes `docs/decisions/draft-lab-<name>.md` with the numbers and states that `policy.json` is unchanged.

## Example

```
$ cox lab new arena-cost --rule arena.trigger --metric cost_usd --epic epics/v2
created experiment arena-cost: rule=arena.trigger metric=cost_usd variants=[on off] -> .../cox/lab/arena-cost.json
$ cox lab assign arena-cost --story m8 --epic epics/v2
lab arena-cost: m8 -> variant on
apply by hand (policy.json is NOT changed): rule arena.trigger = on
$ cox lab report arena-cost --epic epics/v2
lab arena-cost: rule=arena.trigger metric=cost_usd
  variant on   n=1 mean=... variance=...
  variant off  n=0 mean=0.000 variance=0.000
epics on v2: 1 (bar for retirement: 5)
below the bar: read every number next to n and the epic count, never as a rule (ADR 0011)
```

## Limits (decision 0011)

- The lab never edits `policy.json` and never retires a rule on its own; `retire` only drafts a proposal.
- With fewer than 5 v2 epics with scorecards, every report prints the epic count next to the numbers, so a one-epic
  experiment cannot read as evidence.
