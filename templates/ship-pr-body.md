<!-- templates/ship-pr-body.md - body of the production PR `epic/<slug> -> <production>` (one per repo; title `[PROD] ...`).
     Fill from `bin/ship-facts.sh <epic>` plus the ops surfaces it lists. Vietnamese like the team's release PRs; identifiers as-is.
     Every "Chuẩn bị TRƯỚC" line is a checkbox with an owner; every "Sau khi merge" line is a concrete command, log line or curl.
     Delete sections that do not apply; never leave a placeholder. End with the session link. -->

`epic/<slug>` -> `<production>`: <N> commits, <conflict với production: none | file list resolved in #sync>. Nhánh epic trên staging (`<staging>`) từ <date> (#..), sync cuối #.. (<date>). <Repo> đi kèm: <org/repo>#<n> - merge <TRƯỚC | SAU> PR này.

## Thứ tự merge

1. <sync PR into the epic, if production drifted: `chore(epic): sync <production> into epic/<slug>` #..; this PR is mergeable only after it>
2. <cross-repo order: backend first, why the old client still works on the new backend (or does not)>
3. <other open PRs into production and whether they conflict; what the second one resolves>
4. <what the production pipeline does on push: migrate, build, deploy, which apps are affected, which environment gates pause>

## Cái gì lên

<N commits, files, +/-; top dirs>

| PR | Nội dung |
|---|---|
| #.. | .. |

### <surface 1: app / service>
- <what changed for an operator: endpoints, auth mode, headers, env the code reads, third-party SDKs>

### Shared
- <libs, CI workflow changes>

## Migration (pipeline tự chạy | chạy tay: lệnh)

| File | Loại |
|---|---|
| `<timestamp>-<Name>` | <additive / seed / destructive; LOAD-BEARING nếu thiếu thì sao; có `down`?> |

## Chuẩn bị TRƯỚC khi merge

### A. Env - DevOps

Chỉ liệt kê biến MỚI hoặc ĐỔI GIÁ TRỊ (captain 2026-09-09: biến giữ nguyên không nhắc, liệt kê thêm chỉ gây confuse).
Required = thiếu thì boot fail hoặc tính năng chết; Optional = code có default. One table per service / app /
GitHub Environment; a service with nothing new gets one sentence, not a table.

**`<task-def / service / environment name>`**

| Biến | Required | Example | Ghi chú |
|---|---|---|---|
| `<NEW_VAR>` | Required | `<realistic example value>` | <where it comes from, what fails without it> |
| `<CHANGED_VAR>` | Required, ĐỔI GIÁ TRỊ | `<new value>` | <why the old value is wrong now> |
| `<OPT_VAR>` | Optional (default `<x>`) | `<example>` | <when to set it> |
| `<FORBIDDEN_VAR>` | KHÔNG ĐƯỢC CÓ | - | <what breaks if present> |

<one line for an existing var whose VALUE must satisfy something new, e.g. a key needing a new model scope>.
Không đọc nữa, xoá lúc tiện: `<VAR>`, `<VAR>`.
### B. Edge / hạ tầng - DevOps
- [ ] <ALB / Cloudflare / DNS rules the code assumes; public host value another repo needs>

### C. Bên thứ ba - <owner>
Secrets a CI workflow reads get the same table as A (one per GitHub Environment; new or changed only; names from `grep -o 'secrets\.[A-Z_0-9]*'` diffed against production).
- [ ] <Firebase / Apple / Google / vendor console steps; which project maps to which environment>

### D. Quyết định của captain
- [ ] <version bump, plans/ dirs riding along, anything the leader did not decide>
- [ ] Checklist QA staging (cuối PR) đã tick.

## Sau khi merge

1. <pipeline: which gates to approve, in which order>
2. <boot log line that proves the config>
3. <DB query that proves the migration / seed>
4. <curl matrix from the internet and from the VPC: path -> expected status>
5. <runtime knobs: admin API / feature flags, no deploy needed>
6. <what to watch for 24h: spend, 429 rate, queues, push delivery>

### Rollback
- <redeploy previous revision; are migrations reversible; which other repo must roll back with it>

## Kiểm chứng
- <CI on story PRs and on this PR; staging soak dates; evidence branches; reviewer bot>

### Checklist QA staging (tick trước khi merge)
- [ ] <flow id: what to check, on what device>

## Follow-up (ngoài PR này)
- <known gaps>

<session link>
