# 0003 - One worker per repo is a policy default, not an invariant

- Status: Accepted
- Date: 2026-09-15

## Context

v1 assumes one worker per repo. The captain sometimes wants several stories in one repo in parallel when they touch
disjoint files. Hard-coding "one per repo" blocks that; dropping it entirely invites two workers writing the same files.
The safe boundary is disjoint file ownership, not repo count.

## Decision

"One worker per repo" is the default in `policy.yaml`, not a core invariant. Multiple stories may run in one repo when
their declared `files_owned` sets do not intersect. The scheduler enforces disjointness; the policy default keeps the
simple case simple.

## Consequences

- The common case is unchanged; parallel same-repo stories are opt-in and gated by declared ownership.
- Stories must declare `files_owned` to run concurrently in one repo; the scheduler rejects overlap.
- Policy carries a "why" and "review_when" so the default is auditable (P7).

## Review when

Reconsider if overlapping-ownership conflicts appear in practice, or if declaring `files_owned` proves too coarse to
prevent collisions.
