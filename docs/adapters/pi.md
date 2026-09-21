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
| instructions | AGENTS.md + Agent Skills |

## Support contract

- Pi reads the harness-neutral job description from `AGENTS.md` and Agent Skills natively.
- Model is explicit `provider/model`; Coxswain never invents a provider from the harness name. A bare, empty, or
  provider-less model, or an unsupported `--thinking` level, fails before spawn with a bounded diagnostic.
- A dispatched worker cannot answer the interactive workspace-trust dialog, so launch marks the cox-created worktree
  trusted with `--approve` (Pi's per-run trust). Coxswain never mutates user-level Pi configuration.
- Push wake and automatic checkpoint are delivered by the project-local extension installed with
  `cox workspace hooks --harness pi` (hash-verifiable, under `.pi/extensions/`). Worker launch loads it explicitly with
  `--no-extensions -e <path>`, so worker correctness does not depend on project trust or ambient discovery.
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

The executable owners are `internal/adapter/harness/pi/` (card, launch, provider/model validation, telemetry,
extension packaging), `internal/adapter/harness/pi/extension/` (the Pi extension + its deterministic lifecycle suite),
`cmd/cox/` (argv composition, `--allow-unsandboxed`, `cox workspace hooks --harness pi`), and their tests. Dated CLI
observations are in [Pi CLI 0.85.1 compatibility evidence](../evidence/compatibility/pi-cli-0.85.1.md).

## Known ceilings

- No host-filesystem sandbox in Pi 0.85.1; OS-level confinement is follow-up scope, not claimed here.
- Real-tool leader/worker behavior (composer recognition, session association, lifecycle continuity, extension
  behavior under `/new`, `/resume`, `/fork`, compaction) is proven in the wave-2 dogfood, not in this wave.

## Next

- [Handoff](../handoff.md) for push wake and checkpoint semantics.
- [Configuration](../reference/configuration.md) for launch-policy authority and the unsandboxed dispatch gate.
