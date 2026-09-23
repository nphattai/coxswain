<span id="harness-adapter-pi" aria-hidden="true"></span>

# Pi harness adapter

Pi (`@earendil-works/pi-coding-agent`) supports leader and worker roles. A project-local Coxswain extension provides
push wake delivery and automatic checkpoint capture; those card values are release gates proven by the extension
lifecycle tests and real-tool dogfood, not assumptions. Session-JSONL telemetry is optional evidence: an absent,
ambiguous, or unreadable session remains unknown.

## Capability card

This table is checked against `internal/adapter/harness/pi.Harness.Card()` by
`tests/integration/harness_card_docs_test.go`.

| Field | Value |
|---|---|
| name | pi |
| roles | leader, worker |
| wake | push |
| checkpoint | auto |
| doorbell | true |
| interrupt | true |
| telemetry | true |
| sandbox | false |
| unsandboxed_ack | false |
| busy_record | true |
| busy_sources | pi-ext, dispatch, interrupt, recovery |
| instructions | AGENTS.md + Agent Skills |

## Support contract

- Pi reads the harness-neutral job description from `AGENTS.md` and Agent Skills natively.
- Model is explicit `provider/model`; Coxswain never invents a provider from the harness name. A bare, empty, or
  provider-less model, or an unsupported `--thinking` level, fails before spawn with a bounded diagnostic.
- A dispatched worker cannot answer the interactive workspace-trust dialog, so launch marks the cox-created worktree
  trusted with `--approve` (Pi's per-run trust). Coxswain never mutates user-level Pi configuration.
- Push wake and automatic checkpoint are delivered by the project-local, hash-verifiable extension under
  `.pi/extensions/`. `cox workspace init` installs it once per workspace for the leader (unbound; see
  [Leader install and hooks](#leader-install-and-hooks)); `cox workspace hooks --harness pi [--epic <dir>]` (re)installs
  it. Worker launch loads it explicitly with `--approve -e <path>`, so the user's global Pi packages still load and
  worker correctness does not depend on ambient discovery.
- **Reduced mode.** If the extension is missing, its hash is unverified, or it fails to load, dispatch downgrades the
  effective card to pull/manual through the card-notice path (the leader must run `cox wake wait`; the worker must write
  `cox checkpoint facts` at each phase boundary). The reduced mode is emitted, never inferred from the static card.
- **Unsandboxed.** `sandbox: false`: Pi runs with the user's permissions and provides no host-filesystem confinement.
  Git-worktree isolation stays mandatory. Pi carries no standing unsandboxed acknowledgment, so a Pi worker dispatch
  requires an explicit `--allow-unsandboxed` recorded in dispatch evidence, with a prominent reduced-mode warning.
  `--allow-unsandboxed` authorizes an unsandboxed dispatch; it is not a sandbox and adds no writable-root enforcement.
- Telemetry reads the worktree's single Pi session JSONL. Missing, ambiguous (more than one session), or unreadable
  telemetry is unknown, never zero usage. Quota stays unknown unless the effective provider/model maps to an
  authoritative source.
- **Harness-owned busy state.** Idle/busy is a fact the harness reports, not something a backend infers from a TUI.
  Dispatch/resume/relaunch arm a per-story record at `<epic>/.cox/sessions/<story>.busy.json` (`cox busy arm`) and thread
  its incarnation gen to the worker as `COX_BUSY_GEN`. The extension Applies `busy` on `agent_start` and `idle` on
  `agent_settled` (`cox busy apply`, `source=pi-ext`); `agent_settled` fires even on abort/failure, so the idle report
  covers those paths. A stale gen (a hook that outlived its incarnation) is rejected; a missing gen means no write.
  Every backend (`ringReady`, `Composer`) and the watcher's idle/blocked passes consult this record FIRST - `idle` rings
  / reads empty, `busy` skips, and only `unknown`/absent falls back to the backend's own signal. This is why a Pi worker,
  whose TUI the Orca screen classifier does not recognize, is still steered when idle (dogfood F-A). Claude/Codex hook
  wiring for the same record is a backlog follow-up; they are not armed, so backends fall back to their existing signal.
- **Interrupt through the harness.** Pi 0.86.1's TUI ignores the backend interrupt keystroke (dogfood F-C), so the card
  carries `BackendInterrupt: false`. `cox control interrupt` still sends the keystroke (the fallback) and additionally
  delivers a durable `interrupt` inbox record; the extension aborts the running turn via the Pi extension API
  (`ctx.abort()`) when it sees that record. See [Handoff](../handoff.md).

## Leader install and hooks

The Pi leader is a workspace-level peer of a Claude/Codex leader. `cox workspace init` installs the extension into
`<ws>/.pi/extensions/` **without** an epic marker (an *unbound* leader) and adds `.pi/extensions/` to `<ws>/.gitignore`
(per-machine, never committed - captain ruling 2026-09-23). `cox workspace hooks --harness pi` does the same without
`--epic`; `--epic <dir>` binds one epic instead. A driver upgrade needs `cox workspace init` again; `cox doctor` reports
an ISSUE with the repair `cox workspace init` when the installed extension is missing or its hash differs from the
running binary's.

An unbound leader supervises **every** active epic of the workspace - the same set `cox hook` resolves - and picks up an
epic opened or closed mid-session without a restart. The extension maps Pi lifecycle events onto the same Go-owned leader
hooks the Claude/Codex leader hooks run, so the Go side stays the single owner of what a wake means:

| Pi event | cox hook | Behaviour |
|---|---|---|
| `before_agent_start` | `cox hook prompt-drain` | drain every active epic's unread wakes (a per-epic header names each) and inject them as turn context; the pending idle child is retired first so a wake is delivered once |
| `agent_settled` | `cox hook stop-rewake` | wait for a wake while idle, then reopen the turn with one visible follow-up; run the turn-boundary watcher guard (restart a dead watcher, or surface the `cox watch --replace` repair line) |
| `session_before_compact` | `cox hook precompact` | persist the per-epic leader checkpoint before compaction |
| `session_start` | `cox hook session-start` | inject the recovery checkpoint on start/resume |

The reopen loop is bounded: the extension threads a stable per-session handle (`ORCA_TERMINAL_HANDLE`, else a stable Pi
session id) so the cox-firstmate turn-boundary block budget (3 blocks per turn, then the turn ends with a warning)
applies, and it caps per-turn reopens as a client-side backstop. A hook exit 2 is always surfaced as a visible block,
never swallowed. Only one cox extension is active per Pi process: if the worktree also carries a project-local
`.pi/extensions/`, the second load stays inert - one wake child, one busy writer, one checkpoint per event.

The executable owners are `internal/adapter/harness/pi/` (card, launch, provider/model validation, telemetry,
extension packaging), `internal/adapter/harness/pi/extension/` (the Pi extension + its deterministic lifecycle suite),
`cmd/cox/` (argv composition, `--allow-unsandboxed`, `cox workspace hooks --harness pi`), and their tests. Dated CLI
observations are in [Pi CLI 0.85.1 compatibility evidence](../evidence/compatibility/pi-cli-0.85.1.md) and the wave-2
real-tool [Pi CLI 0.86.1 compatibility evidence](../evidence/compatibility/pi-cli-0.86.1.md).

## Known ceilings

- No host-filesystem sandbox in Pi 0.85.1; OS-level confinement is follow-up scope, not claimed here.
- Real-tool leader/worker behavior (composer recognition, session association, lifecycle continuity, extension
  behavior under `/new`, `/resume`, `/fork`, compaction) is proven in the wave-2 dogfood, not in this wave.

## Next

- [Handoff](../handoff.md) for push wake and checkpoint semantics.
- [Configuration](../reference/configuration.md) for launch-policy authority and the unsandboxed dispatch gate.
