# DESIGN: account tier migration

## Goal
Move `accounts.tier` from a free-text column to a normalized `tiers` table, so tier changes are auditable.

## Plan
1. Add table `tiers(id, name)`; backfill from the distinct values in `accounts.tier`.
2. Add `accounts.tier_id` referencing `tiers(id)`; backfill from the text column.
3. **Drop `accounts.tier`** once `tier_id` is populated.
4. Point the API at `tier_id`.

## Seeded flaw (for the arena fixture)
Step 3 drops `accounts.tier` in the same release as step 4. But the billing client still reads `accounts.tier`
directly (it was never migrated), so the drop breaks billing at read time. A correct plan keeps `accounts.tier` until
every reader is on `tier_id`, or migrates the billing reader first.

## Notes
This fixture is the design under review. The billing reader that still uses the dropped column is in the repo the arena
test builds; the sample adversary report cites it. The seeded flaw is real: an adversary should catch it with a citation
to the reader line, and a plausible-but-wrong claim (below) should be rejected in synthesis.
