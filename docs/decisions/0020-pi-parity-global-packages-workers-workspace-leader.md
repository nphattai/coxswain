# 0020 - Pi parity: global packages in workers, workspace-level leader

- Status: Accepted (captain, 2026-09-23)
- Date: 2026-09-23

## Context

Pi (`@earendil-works/pi-coding-agent`) was a second-class harness. A Pi **worker** launched with `--no-extensions`, which
keeps only CLI `-e` paths (`extensionPaths = noExtensions ? cliEnabledExtensions : merge(...)`,
`pi-coding-agent/dist/core/resource-loader.js`), so the user's global Pi packages (`compact-adviser`, `pi-web-access`,
...) never loaded - unlike a Claude worker, which keeps its user-scope plugins (B-47). A Pi **leader** had only a
single-epic extension installed by hand with `cox workspace hooks --harness pi --epic <dir>`, and it hand-rolled a
single-epic `cox wake wait` loop with a static nudge - it did not supervise every active epic and it bypassed the
Go-owned leader hooks, unlike a Claude/Codex leader whose workspace hooks drain and rewake for every active epic (B-48).

The captain's ruling (2026-09-23): **Pi is supported fully for leader and worker, the same as Claude and Codex.** Pi on
Orca's orchestration plane stays out of scope (Orca's Spawn drops argv); the refusal stays, tracked as B-49.

## Decision

1. **Worker loads global packages.** Pi worker launch drops `--no-extensions` and emits
   `pi [--model m] [--thinking e] --approve -e <cox ext>`, so global packages load and the target repo's project
   resources (`.pi/`, `.agents/skills`) load under `--approve`, exactly as a Claude worker loads the repo's `.claude/`
   settings. Accepted risk: a repo that ships its own Pi primary extensions runs them inside the worker. Reduced mode
   (extension unverified) is unchanged: no `-e`, pull/manual notice. The default Pi worker model is
   `openai-codex/gpt-5.6-sol` (`templates/policy.json` `harness.worker.models["pi"]`). *(Implemented by the
   `cox-pi-parity-worker` story; see the [Pi adapter](../adapters/pi.md) for the launch argv.)*

2. **Workspace-level Pi leader.** `cox workspace init` installs the embedded extension into `<ws>/.pi/extensions/`
   **without** an epic marker and ensures `<ws>/.gitignore` ignores `.pi/extensions/` (idempotent, never rewriting other
   lines). The extension is per-machine and never committed; a driver upgrade needs `cox workspace init` again.
   `cox workspace hooks --harness pi` without `--epic` does the same; `--epic <dir>` still binds one epic.

3. **Unbound leader supervises every active epic.** With no epic binding the Pi leader supervises the same set `cox hook`
   resolves (`leaderEpics`/`activeEpics`), picking up epics opened or closed mid-session without a restart.

4. **Leader hook parity, Go stays the single owner.** The extension maps Pi lifecycle events onto `cox hook
   prompt-drain | stop-rewake | precompact | session-start` (the same behaviours the Claude/Codex leader hooks run),
   including the turn-boundary watcher guard. A stop-rewake reopen is delivered as one visible follow-up; a hook exit 2 is
   surfaced, never swallowed. The reopen loop is bounded by the cox-firstmate block budget (keyed on a stable per-session
   handle - `ORCA_TERMINAL_HANDLE`, else a Pi session id) plus a client-side per-turn cap. Only one cox extension is
   active per Pi process (a second load stays inert).

5. **Stale/missing extension is visible.** `cox doctor` reports an ISSUE, with the repair `cox workspace init`, when a
   workspace lists `pi` as a leader option but `.pi/extensions/` is missing or its hash differs from the running binary's.
   *Superseded in part (epic cox-supervision-port, story w2-bearings, firstmate `fm-session-start` pi_diagnostic cases):
   an installed hash is not proof the extension is loaded; the session-start digest reports `PI_LEADER_EXTENSION:
   loaded` only from the running extension's marker `<ws>/.cox/pi-leader-extension-loaded` (current version, live pid,
   turn-end guard present, not a handoff generation).*

6. **Policy parity notice.** `cox workspace init` on an existing `cox/policy.json` whose `harness.{leader,worker}.options`
   lack a harness the template lists prints one notice naming it and leaves the file byte-identical.

## Consequences

- A Pi worker is a full peer: global packages and repo project resources load, and the harness-owned busy/interrupt/
  checkpoint wiring is unchanged.
- A Pi leader is a full peer: one workspace-level, unbound terminal drains and rewakes for every active epic, with the
  Go side the single owner of what a wake means. The single-epic `cox wake wait` loop and its static nudge are removed.
- `.pi/` is per-machine and gitignored, so it is never committed to a workspace; upgrading the driver is a re-run of
  `cox workspace init`, already the rule for the embedded leader skills.
- Accepted risks: a repo's own Pi primary extensions run inside a worker (item 1); Pi remains unsandboxed, so a worker
  dispatch still requires `--allow-unsandboxed`.

## References

- Epic design: `epics/cox-pi-parity/DESIGN.md` (captain rulings 2026-09-23; Pi 0.86.1 facts).
- [Pi adapter](../adapters/pi.md), [Install](../getting-started/install.md), [Create a workspace](../getting-started/workspace.md).
- Backlog: B-47 (worker global packages), B-48 (workspace-level leader), B-49 (Pi on the Orca orchestration plane, out of scope).
- Owners: `internal/adapter/harness/pi/` and `internal/adapter/harness/pi/extension/`, `cmd/cox/workspace.go`,
  `internal/workspace/init.go`, `internal/doctor` (cmd wiring), and their tests.
