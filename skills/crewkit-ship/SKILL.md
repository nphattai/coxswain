---
name: crewkit-ship
description: Ship an epic to production - `[PROD]` PR `epic/<slug> -> <production>` per repo whose body carries the go-live preparation before and after the merge (env, edge, migrations, third-party consoles, pipeline gates, curl matrix, rollback). Use when the captain says "ship <slug>", "lên production", "tạo PR vào master", or after the staging soak of an epic.
---

# crewkit-ship

`$ROOT` = `git rev-parse --show-toplevel`. Runbook section: `$ROOT/docs/workflow.md#8-ship`. Branch names come from
`<project>/docs/repos.md` (Production / Staging columns). The captain merges every PR; this skill never merges,
never pushes to `epic/<slug>` or a production branch, and never deletes a branch.

0. Gate: the captain asked for the ship by name; every story in `DESIGN.md` is merged; the epic tip is in the staging
   branch (`bin/ship-facts.sh` prints `epic in <staging>: yes`). A ship with stories still open is a question, not a PR.
1. Facts: `$ROOT/bin/ship-facts.sh <project>/epics/<slug>`. Per repo it prints the tips, ahead/behind against staging
   and production, conflicts, production commits the epic lacks, open PRs into production, PRs merged into the epic,
   and the ops surfaces of the diff. Then READ every listed surface on the epic branch against production:
   `.env*` samples and `configuration.ts` (new, removed, renamed vars; which are required at boot), CI workflows
   (secrets a job reads, environment gates, what runs on push to production), migrations (additive or not, seeds
   that are load-bearing, `down`), Dockerfiles, deploy docs and changelog entries, `app.config` / `eas.json`, and
   anything the code moved from env to runtime settings (system settings, feature flags, admin endpoints). Also grep
   the repo's docs for a DevOps report of the epic (`plans/reports/devops-*`); it is often already the env list.
2. Production drift: if production has commits the epic lacks, or `conflicts with <production>` is not `none`, open a
   sync PR INTO the epic, never onto the production PR: throwaway worktree from `origin/epic/<slug>`, branch
   `chore/sync-<production>-<slug>`, `git merge --no-ff origin/<production>`, resolve by the epic's intent on the surfaces
   it changed and production's everywhere else, lockfile re-resolved without linking (`yarn install --mode=update-lockfile`,
   expect no delta), commit `chore(epic): sync <production> into epic/<slug>`, PR into `epic/<slug>` (precedent webapp
   #568, #612). Never merge the staging branch into the epic (the `release-staging-pr` rule: staging-merge commits
   must not reach production). Remove the worktree afterwards.
3. Body per repo from `$ROOT/templates/ship-pr-body.md`. The two sections that make the PR a go-live document:
   - "Chuẩn bị TRƯỚC khi merge": env as ONE TABLE PER service / app / GitHub Environment (captain 2026-09-09), columns
     Biến | Required | Example | Ghi chú, ONLY the vars that are new, change value, or must not exist (captain
     2026-09-09: an unchanged var listed is noise that confuses the operator; a service with nothing new gets one
     sentence). Required / Optional with the default, a realistic example value, what fails without it; one line for
     an existing var whose value must now satisfy something new; then a line of vars the code no longer reads. Find
     them by DIFFING the `.env*` samples, `configuration.ts` and `grep -o 'secrets\.[A-Z_0-9]*' <workflow>` against
     the production branch, never by listing what the samples contain. Then edge rules the code assumes, third-party console steps (which
     project maps to which environment), and the decisions only the captain can take (version bump, `plans/` dirs
     riding along). Name the owner of every section.
   - "Sau khi merge": pipeline gates in order, the boot log line that proves the config, the DB query that proves the
     seed, a curl matrix from the internet and from the VPC, the runtime knobs that need no deploy, what to watch for
     24 h, and rollback (previous revision, migration reversibility, which other repo rolls back with it).
   Also: cross-repo merge order (backend first when clients need new routes; say whether the old client keeps working
   on the new backend), the other open PRs into production and what the second merge resolves, the PR table of what
   ships, migrations, verification with dates, the staging QA checklist as unticked boxes, follow-ups, session link.
   Plain dash, Vietnamese like the team's release PRs, identifiers verbatim, no placeholder left.
4. Open: `gh pr create --base <production> --head epic/<slug> --title "[PROD] <what ships>" --body-file <body>` from
   the repo's epic worktree, backend first so the client body can cite its number; then `gh pr edit` the backend
   body with the client number. `gh pr view --json mergeable,mergeStateStatus` on each: a client PR is CONFLICTING
   until its sync PR merges, and the body says so.
5. Record: DESIGN.md Ship line (PR numbers, sync PR, order), `plans/dispatch-log.md` entry, memory. These edits stay
   uncommitted until the captain has read them. Report the PR URLs, the merge order, and the pre-merge lines that
   need a DevOps or captain action before anything is mergeable.
