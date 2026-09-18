# Scout: migration

The migration lives in `db/migrate.sql`. Relevant steps:

- Adds `tiers` and `accounts.tier_id`, backfills.
- Drops `accounts.tier` (the column billing still reads).

The drop and the API cutover ship in the same release.
