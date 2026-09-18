# 0009 - Gate the removal of v1 on the downstream workspace migrating to v2

- Status: Accepted
- Date: 2026-09-15

## Context

Decision 0006 said `bin/` (v1) is deleted at M5. M5 delivers the packaging, the arena follow-ups, and `cox migrate`, but
the downstream workspace's live epics still run on v1, and the captain owns when each one cuts over. Deleting
`bin/` and `skills/crewkit-*` in M5, before those epics have actually run a story on v2, would strand a live epic on a kit
that no longer ships its scripts. The removal is safe only after v2 has carried real downstream work, not merely after the
migrate command exists.

## Decision

M5 does NOT remove `bin/` or `skills/crewkit-*`. It ships `cox migrate` (dry run by default, `--apply` writes the v2
control tree and renames `.run` to `.run.migrated`) and leaves v1 in place. Removal is a separate, later change, gated on
all of:

1. Every live epic in the downstream workspace has been migrated with `cox migrate --apply`, and `cox doctor` reports no
   live `.run` beside a `.cox` for any of them.
2. At least one story has been dispatched, parked/resumed, and completed on v2 in the downstream workspace (a real story,
   not the e2e smoke), proving the binary drives the workspace end to end.
3. The captain has approved the cutover timing (the epics are the captain's to schedule; migrate is never run on the
   downstream workspace by a worker on its own).

When all three hold, a follow-up change deletes `bin/`, `skills/crewkit-*`, and any v1-only templates, and supersedes both
this decision and 0006.

## Consequences

- v1 keeps running the downstream epics through M5; nothing a live epic depends on is removed early.
- `cox migrate` is available and verified (fixture test, dry run, `--apply`, doctor clean), but running it on the
  downstream workspace waits for the captain.
- The removal is a small, reviewable change once the gate is met, not bundled into M5.

## Review when

When the three gate conditions above are met (the downstream workspace fully on v2). Reopen earlier only if keeping
`bin/` on the kit blocks a v2 release the captain wants to ship.
