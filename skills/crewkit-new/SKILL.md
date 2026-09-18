---
name: crewkit-new
description: Start a cross-repo epic in this project repo - creates the Orca worktrees on epic/<slug> on both hosts, the epic dir with repos/symlinks/DESIGN.md - then guides the captain-direct design. Use when the captain says "new epic", "start epic <slug>", or names a feature spanning repos.
---

# crewkit-new

`$ROOT` = `git rev-parse --show-toplevel`. Full runbook: `$ROOT/docs/workflow.md`.

0. Gate: an epic exists only when the captain asked for it by name. List `<project>/epics/*/DESIGN.md` `Status:`
   lines first; if another epic in the project is `active` and the captain did not explicitly close it or say
   "start <new> while <old> stays open", stop and ask. Never open a follow-up epic on your own initiative.
1. Project = a top-level dir of the workspace (its `AGENTS.md` lists them); repo names from `<project>/docs/repos.md`
   (Repo column). Then, from this Orca terminal:
   `$ROOT/bin/epic-new.sh <project> <slug> <alias>=<repo> [...]` (`--hosts=local` to skip the remote host). It writes
   `DESIGN.md` from the template and `epic.env` (ports allocated); check `BACKEND_APP`, `SEED`, `SIM_BASE` in `epic.env`.
2. Epic layer: `$ROOT/bin/infra.sh up` (once per machine), then `$ROOT/bin/local-env.sh <project>/epics/<slug> up`
   and `smoke`. Secrets the backend needs beyond the worktree's `.env` go in `~/.config/<workspace>/<slug>.env`.
3. Phase 0 (D25): one scout story per repo from `$ROOT/templates/story.md`, output shape
   `$ROOT/templates/scout-report.md`, every path with `file:line`; dispatch them in parallel, grep each reported
   path into the repo before reading, refresh `<project>/docs/architecture/<alias>.md` with the verified SHA.
4. Design with the captain from the reports: `DESIGN.md` with a concrete contract. Wave rule (D24): the contract
   lands in the backend story; every client story `depends` on it. Never delegate the design.
5. One story per repo from `$ROOT/templates/story.md` into `stories/`, in the captain's words, absolute
   "Read first" paths (including `<epic>/.env.<story-id>`), the Verification table (tests), Files touched,
   `device: true` for simulator stories, `host:` left empty unless pinned, `agent`/`model` per `$ROOT/docs/agent-routing.md`.
6. Fill the repo table in `DESIGN.md`, commit the epic dir, hand off to `crewkit-dispatch`.
