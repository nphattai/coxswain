# Contributing

Coxswain is one Go binary (`cox`) plus a harness plugin. The core talks to small interfaces, so a new backend or harness
plugs in without touching the engine. This page covers how to build, how a change flows, and where its docs go;
[AGENTS.md](AGENTS.md) maps the code.

## Build and test

```bash
make install          # build + install cox (version from git describe)
make lint             # gofmt + go vet
make test             # go test ./... (unit tests with fakes for every adapter, the firstmate supervision corpus,
                      #  and tests/integration, which also guards the docs)
make codemap          # regenerate docs/codemap.md after adding a package or editing a package doc comment
claude plugin validate .
```

CI (`.github/workflows/ci.yml`) runs Go on macOS and Linux, the Pi extension suites, the onboarding E2E, the bash suite,
plugin validation, and a goreleaser snapshot. Every job must be green before a merge.

## Where to look

The code map for people and agents is [AGENTS.md](AGENTS.md): overview, structure, a where-to-look table, conventions,
and gotchas, with a nested `AGENTS.md` in `cmd/cox/`, `internal/`, `internal/adapter/`, `internal/protocol/`, `tests/`,
and `docs/`. Every package is listed in [docs/codemap.md](docs/codemap.md).

## Guards that fail the build

- `TestCodemapFresh`: a package without a doc comment, or a stale `docs/codemap.md`.
- `TestDocsLinks`: a relative link or `#anchor` that does not resolve on GitHub.
- `docs_setup_test.go`: a `cox` command on a setup page that the onboarding E2E does not run, or a `~` home path.
- `import_boundary_test.go`: core importing a concrete backend.

## How a change flows

Coxswain is built with coxswain. The maintainer's checkout is a Shape C workspace
([Create a workspace](docs/getting-started/workspace.md)) whose epic tree lives under `data/`, which is gitignored:
designs, stories, plans, and the ledger stay local, and only the code and the public record below reach the repo.

```
cox epic new data <slug> --repo coxswain        branch epic/<slug>, private data/epics/<slug>/
  scout (optional)  -> refreshes docs/codemap.md and this page's tables
  DESIGN.md         -> the captain signs; arena when policy triggers it
  stories           -> one worker per story on story/<slug>-*, draft PR into epic/<slug>, one commit per phase
  audit + CI green  -> merged into epic/<slug>
  ship PR           -> epic/<slug> into main, carrying the feature or bug doc; the maintainer merges
  cox epic close    -> worktrees removed, branches kept
```

A small fix skips the epic: branch `fix/<topic>` or `docs/<topic>` from `main`, open a PR, and the maintainer merges it.

- **Branches.** `story/*` merges into `epic/*`; `epic/*`, `fix/*`, and `docs/*` merge into `main`. Only the maintainer
  merges to `main`. Branches are not deleted.
- **Commits.** Conventional commits, `type(scope): subject` with `feat`, `fix`, `docs`, `test`, `refactor`, `chore`.
- **Releases.** The maintainer runs `make release-dry`, then tags `vX.Y.Z`; `.github/workflows/release.yml` runs
  goreleaser. Release notes come from the commits, so there is no hand-kept changelog.

## Where docs go

[docs/README.md](docs/README.md) is the index. A change updates the page that owns the behaviour it changes; a drift you
find but do not fix gets a row in [docs/_stale-report.md](docs/_stale-report.md).

Every epic leaves one public record, written in its ship PR from [templates/feature-doc.md](templates/feature-doc.md) and
distilled by hand from the private DESIGN.md (no worker names, ports, or wake ids):

| Folder | Holds | `status:` |
|---|---|---|
| `docs/backlog/` | a design published before it is built | `backlog` |
| `docs/features/` | a shipped feature | `done` |
| `docs/bugs/` | a shipped fix | `done` |
| `docs/archive/` | a superseded or dropped design | `superseded` |

Files are named `YYYY-MM-DD-<slug>.md` with the date the design started, never renamed, and moved between folders rather
than deleted. A lasting architectural choice also gets an ADR in [docs/decisions/](docs/decisions/index.md).

## Extension points

- **Backend adapter** (`internal/adapter/backend/`): drives terminals and worktrees. Implement the `Backend` interface;
  Orca is the reference.
- **Forge / review / quota adapters**: each is an interface with a fake in tests. Add a new one behind its interface.
- **Harness capability card** (`docs/adapters/`): declare a harness's roles, wake mode, checkpoint mode, and sandbox;
  `cox doctor` and `cox story dispatch` read it.

Read [Author an adapter](docs/contributing/adapters.md) before changing an external boundary. It maps each interface to
its fake, tests, reduced-mode rules, and evidence requirements.
