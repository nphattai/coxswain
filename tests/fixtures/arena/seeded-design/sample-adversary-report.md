# Arena adversary - round 1 (sample)

This is the shape `cox arena check` expects. Both rows cite real lines (the test pins them to a real sha, shown here as
`<sha>`); both citations resolve, so `check` passes both. Claim (a) is correct and should be **accepted** in synthesis;
claim (b) cites a real line but reasons wrongly and should be **rejected**.

| claim | evidence | severity | proposal |
|---|---|---|---|
| (a) Dropping accounts.tier breaks billing: the billing reader still selects the text column, so the drop fails reads at runtime. | billing/app/billing.go:4@<sha> | epic-blocking | Keep accounts.tier until billing reads tier_id, or migrate billing first. |
| (b) The tiers backfill loses data because migrate.sql has no unique constraint on tiers.name. | billing/db/migrate.sql:2@<sha> | significant | Add a unique index on tiers.name. |

Why (b) is wrong: the backfill inserts distinct values, so duplicates never arise; the missing constraint is a
belt-and-braces nicety, not a data-loss bug. The citation is real (the line exists), which is exactly why machine-checking
citations is necessary but not sufficient - the leader still adjudicates the reasoning.
