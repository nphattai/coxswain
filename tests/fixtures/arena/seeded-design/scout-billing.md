# Scout: billing reader

The billing client reads the account tier to price an invoice. It reads the column directly:

- `app/billing.go` selects `tier` from `accounts` and switches on the string value.

It was written before the tier normalization plan and has no knowledge of `tier_id`.
