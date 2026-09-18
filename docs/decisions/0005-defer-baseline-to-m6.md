# 0005 - Defer baseline measurement to M6; it does not block delivery

- Status: Accepted
- Date: 2026-09-15

## Context

Automatic routing by quota or model needs a measured baseline (bare harness vs coxswain on the same stories). Gathering
that baseline before shipping the core would block M1-M5 on measurement infrastructure. Measurement should run alongside
delivery, not gate it, but routing must not turn on before there are numbers (P7).

## Decision

Baseline replay and the scorecard move to M6. They do not block M0-M5. Routing, the captain board, and any rule lab stay
off until M6 produces a baseline table (>= 3 story replays) and an attempt-aware scorecard. Every later feature that
opens must cite a number or a captain ruling as its reason.

## Consequences

- The core, handoff, environment, and arena work land without waiting on measurement.
- No automatic model routing exists before M6; selection stays manual/policy until then.
- M6 owns the definition of the baseline and the scorecard.

## Review when

Reconsider the timing if a delivered milestone clearly needs a number sooner, or if the captain asks for routing before
M6 (in which case the baseline requirement still holds first).
