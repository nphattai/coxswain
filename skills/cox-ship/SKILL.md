---
name: cox-ship
description: Ship an epic to production - a [PROD] PR epic/<slug> -> <production> per repo whose body carries the go-live preparation before and after the merge (env, edge, migrations, third-party consoles, pipeline gates, curl matrix, rollback). Use when the captain says "ship <slug>", "len production", "tao PR vao master", or after the staging soak.
---

# cox-ship

Port of crewkit-ship to the `cox` binary. The captain merges; this skill prepares the PR.

## 0. Gate
The captain asked for this epic by name; every story is merged into `epic/<slug>`; the epic has soaked on staging.

## 1. Facts
```
cox ship facts --epic <project>/epics/<slug> --json
```
Per-repo ahead/behind and conflicts, three-state: a failed fetch or merge-tree is `unknown` (exit 3), never a fabricated "conflicts: none". Read every go-live surface: `.env*`, config, CI workflows, migrations, Dockerfiles, the DevOps report.

## 2. Production drift
If production has commits the epic lacks, or conflicts are not confirmed none, open a sync PR INTO the epic (never onto the production PR): a throwaway worktree from `origin/epic/<slug>`, branch `chore/sync-<production>-<slug>`, `git merge --no-ff origin/<production>`, resolve there.

## 3. Body per repo (templates/ship-pr-body.md)
Two go-live sections:
- "Chuan bi TRUOC khi merge": ONE TABLE per service / app / GitHub Environment, only NEW or CHANGED vars (captain 2026-09-09: unchanged vars are not listed), Required/Optional, example, note. Third-party console steps. Captain decisions.
- "Sau khi merge": pipeline gates, the boot log line, a DB query proof, the curl matrix, runtime knobs, the 24h watch, rollback.

## 4. Open (backend first)
```
gh pr create --base <production> --head epic/<slug> --title "[PROD] <slug>"
```
Edit the client PR numbers into the backend body; state the merge order.

## 5. Record
`DESIGN.md` Ship line, the dispatch log, memory. Report the URLs and the merge order. The captain merges.
