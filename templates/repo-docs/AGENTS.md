# <repo-name>

<one sentence: what this service/app is and who calls it>. Part of the <project> project;
cross-repo context lives in `~/Work/<workspace>/<project>/` (catalog `docs/repos.md`, contracts `docs/contracts/`).

## Stack
<runtime, framework, package manager, Nx or not, database, queues, deploy target - one line each>

## Commands
- install: `yarn install`
- run: `<cmd>` (needs `<env vars>`; see `docs/how-to/run-locally.md`)
- test: `<cmd>` · e2e: `<cmd>` · lint: `<cmd>` · migrations: `<cmd>`

## Layout
<5-10 lines: apps/, libs/ by role, where entry points are. Point at docs/reference/architecture.md for depth.>

## Conventions that bite
<the 5-8 rules a newcomer or agent breaks first, each with the file that proves it, e.g.
- Config is read once in `*.config.ts` via `safeStr/safeNumber`, consumed via `ConfigService`; no raw `process.env` (`apps/.../configuration.ts`).>

## Branches and delivery
production `<branch>`, staging `<branch>`; feature work branches from `epic/<slug>`, PRs into it; see `docs/how-to/deploy.md`.

## Docs
Start at `docs/README.md` (topic -> owning doc). Record code-vs-doc drift in `docs/_stale-report.md`.
Decisions in `docs/decisions/`. `docs/history/` is frozen history, never cite it as current.
