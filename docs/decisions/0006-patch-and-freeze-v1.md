# 0006 - Patch four safety bugs in v1, then freeze it

- Status: Accepted
- Date: 2026-09-15

## Context

Three epics (base, main-2, legacy PIPO) run on v1 while v2 is built. v1 has destructive-on-failure behaviors: it can
force-delete an unpushed branch (F01), fall back a supposedly isolated worker into the shared checkout (F02), lose
ownership of a running worker on a failed park (F05), and rearm the idle leader for an already-parked story. A broad v1
rewrite would compete with the v2 core and risk the running epics; leaving v1 as-is risks those epics.

## Decision

Apply exactly four safety patches to v1, each with a failing-first test, then freeze v1: no further v1 feature work, and
`bin/` is deleted at M5 once epics migrate to the binary. v1 state is not migrated by these patches. The four: F01 (no
branch deletion), F02 (worktree failure stops dispatch), F05 (park keeps ownership on failure), and the parked-story
rearm count.

## Consequences

- The three active epics keep running on v1 with the destructive failure modes closed.
- No behavior other than the four fixes changes in v1; tests pin the intended behavior.
- v1 is end-of-life: new capability goes into the v2 core, not `bin/`.

## Review when

Superseded at M5 when v1 is removed. Reopen earlier only if a fifth v1 safety bug threatens a running epic before the
core can replace the affected command.
