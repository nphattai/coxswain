# Optional adapter compatibility evidence

These observations record the versions used while implementing optional review and quota adapters. They are not a
promise that a later tool release keeps the same output.

## lavish-axi

- **Observed:** 2026-09-18
- **Observed versions:** installed 0.1.64, source reviewed at 0.1.69
- **Method:** CLI help, source inspection, fixtures, and adapter tests

- Poll output was TOON, not JSON.
- Poll delivery cleared queued prompts, so delivery acted as acknowledgement and created a loss window before Coxswain's
  synchronous record write.
- Sharing was outward-facing and therefore remained opt-in.

Executable evidence: `internal/adapter/review/lavish/`, `cmd/cox/review_test.go`, and `tests/e2e/review-live.sh`.

The complete real-browser E2E remains a deployment check. Never include `share` in that smoke.

## quota-axi

- **Observed:** 2026-09-18
- **Observed version:** 0.1.37, schema 5
- **Method:** CLI output fixtures and quota adapter tests

- The adapter accepted schema version 5 only and returned unknown on drift.
- Only the provider-neutral projection was cached. Raw output, stderr, and identity fields were excluded.
- Claude quota access could require a one-time Keychain grant.

Executable evidence: `internal/quota/quotaaxi.go`, `internal/quota/quotaaxi_test.go`, and `internal/quota/testdata/`.

The current contracts are [lavish-axi adapter](../../adapters/lavish.md) and
[quota-axi adapter](../../adapters/quota-axi.md).
