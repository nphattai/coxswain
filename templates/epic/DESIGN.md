# {{slug}} - design

Status: proposed (captain sets active / complete)

<one paragraph: what this epic delivers>

| Alias | Repo | Does |
|---|---|---|
{{repo_rows}}

Branch: `epic/{{slug}}` in every repo (on origin). `cox epic new` creates the worktrees and the alias symlinks.
Local env: `cox env up|smoke|refresh --epic {{project}}/epics/{{slug}}` (ports and names in `epic.env`).

## Phase 0 - scout (D25)

One scout story per repo, in parallel, before the contract: every endpoint, DTO and screen in the report carries
`file:line`; the report refreshes `{{project}}/docs/architecture/<alias>.md` (stamped with the SHA it was verified
against). The leader writes the contract below from the reports.

## Contract

TODO: API shape / DTO / migration / types. Lands in the backend story first (wave 1, D24); client stories
`depends` on it.

## Stories

| Story | Repo | Wave | depends |
|---|---|---|---|
| `{{slug}}-<backend alias>` | backend | 1 | - |
| `{{slug}}-<client alias>` | client | 2 | `{{slug}}-<backend alias>` |

Two stories may share a repo only when their `files_owned` are disjoint (decision 0003). The scheduler does not yet
enforce this (`workers_per_repo.allow_parallel_when.enforced: false`): `cox story dispatch` warns when two stories in one
repo are working at once, but nothing blocks a `files_owned` overlap. Until enforcement lands, keep one worker per repo.
