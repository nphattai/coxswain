---
name: cox-epic
description: Start a cross-repo epic - creates the epic worktrees on epic/<slug> in every repo, the epic dir with repos/symlinks/DESIGN.md and epic.env, then guides the captain-direct design and the per-repo stories. Use when the captain says "new epic", "start epic <slug>", or names a feature spanning repos.
---

# cox-epic

Port of crewkit-new to the `cox` binary. The captain owns lifecycle: never open an epic the captain has not named.

## 0. Gate
List existing epics' `Status:` lines (`grep -H '^Status:' <ws>/*/epics/*/DESIGN.md`). Do not start a new epic while another is active unless the captain explicitly says to continue.

## 1. Create the epic
```
cox epic new <project> <slug> --repo <alias> [--repo <alias> ...] [--backend <alias>]
```
`--repo` aliases resolve from `<ws>/cox/workspace.json` (name or absolute path); `alias=ref` registers an ad-hoc repo. This creates the epic dir, one worktree per repo on `epic/<slug>` (cut from the repo's production branch), the alias symlinks, `epic.env` (a fresh port block), `DESIGN.md`, and `.cox/epic.json`. It publishes `epic/<slug>` to origin unless `--no-push`. Check `epic.env`: set `BACKEND`, `BACKEND_APP`, `SEED`, `SIM_BASE` if the epic needs them.

## 2. Epic environment (only if the epic runs a backend)
Write the service adapter `<ws>/cox/services/<alias>.sh` (four verbs; see `docs/adapters/service.md`), then:
```
cox env up --epic <project>/epics/<slug>
cox env smoke --epic <project>/epics/<slug>
```
`smoke` is three-state: exit 1 on a fail, exit 3 on an unknown.

## 3. Phase 0 - scout (D25)
One scout story per repo, in parallel, before the contract. Output shape: `templates/scout-report.md`; every path carries `file:line`. Each report refreshes `<project>/docs/architecture/<alias>.md`, stamped with the SHA it was verified against.

## 4. Design (captain-direct, never delegated)
Write `DESIGN.md` from the reports with a concrete contract. Wave rule (D24): the contract lands in the backend story; every client story `depends` on it.

Before signing, offer the captain a visual review (M13): `cox epic design --html --epic <epic>` renders DESIGN.md, the decisions in force, and the latest arena synthesis under `reports/visual/`; `cox review open` it and `cox review poll` for the captain's feedback (lands as `_leader` inbox records + wakes). Fold the feedback into DESIGN.md, then the captain signs. See `cox-visualize`. The review is optional - signing still works from chat.

## 5. Stories
```
cox epic stories --epic <project>/epics/<slug> [--story <id>=<alias> ...]
```
Renders one story per repo (or the explicit `--story` list) from `templates/story.md` with the project policy resolved once: delivery style, context thresholds, and harness/model come from `cox/policy.json` (plus a `<project>/cox/policy.json` override), and `policy_source:` records the file and sha. Fill each story's Goal, Scope, Acceptance criteria, Verification table, and Files touched.

## 6. Commit and hand off
Commit the epic dir. Hand off to `cox-dispatch`.
