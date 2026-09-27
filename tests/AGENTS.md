<!-- Parent: ../AGENTS.md -->
# TESTS

## OVERVIEW

Unit tests live next to their package. This folder holds what spans the repo.

## WHERE TO LOOK

| Kind | Location | Notes |
|---|---|---|
| Repo guards | `integration/` | docs links, setup commands, codemap freshness, import boundary, CI workflow shape, rule text in AGENTS.md and skills |
| Hermetic onboarding E2E | `e2e/onboarding.sh` | fake orca + fake harness; runs in CI |
| Live E2E | `e2e/{dispatch,arena,review}-live.sh`, `e2e/codex-terminal-steer.sh` | need real Orca and harnesses; guarded by `COX_E2E=1`; leave branches behind on purpose |
| Pi extension suites | `pi-extension/run.sh` | against the pinned Pi version |
| Fixtures | `fixtures/{arena,migrate,verdict}` | |

## CONVENTIONS

- A rule that must not drift from prose (e.g. the terminal-plane rules in AGENTS.md) gets an `integration/` test that
  reads the prose.
- Live scripts create their epic under `$TMPDIR`, never in the repo, and never delete a branch.
