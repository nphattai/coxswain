<!-- Parent: ../AGENTS.md -->
# DOCS

## OVERVIEW

User, reference, protocol, and decision docs. [README.md](README.md) is the index by task; [codemap.md](codemap.md) is
generated. Placement and the design-record lifecycle are in [CONTRIBUTING.md](../CONTRIBUTING.md#where-docs-go).

## WHERE TO LOOK

| Task | Location |
|---|---|
| Setup journey | `getting-started/`, `QUICKSTART.md` |
| CLI, config, policy, frontmatter reference | `reference/` |
| Record formats | `protocol/` |
| Adapter capability cards | `adapters/` |
| Why it is built this way | `decisions/` (ADRs, numbered, indexed in `decisions/index.md`) |
| Dated compatibility results | `evidence/` |
| Shipped features / fixes, pending designs | `features/`, `bugs/`, `backlog/`, `archive/` (`YYYY-MM-DD-<slug>.md`) |
| Known drift | `_stale-report.md` |

## CONVENTIONS

- Links are relative and must resolve on GitHub (`tests/integration/docs_links_test.go`).
- Every `cox` command in a fence on README or `getting-started/` must run in `tests/e2e/onboarding.sh`; home paths use
  `$HOME`, not `~` (`tests/integration/docs_setup_test.go`).
- Point to the code that owns a command, schema, or config key instead of copying its inventory.
- Never edit `codemap.md` by hand; edit the package doc comment and run `make codemap`.
