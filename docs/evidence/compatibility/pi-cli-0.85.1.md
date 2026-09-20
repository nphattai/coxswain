# Pi CLI 0.85.1 compatibility evidence

- **Observed:** 2026-09-19 (wave-1 source + fixtures); real-tool leader/worker runs are the separate wave-2 dogfood
- **Environment:** `@earendil-works/pi-coding-agent` 0.85.1 (`pi --version` -> `0.85.1`) on the maintainer machine
- **Method:** `pi --help`, installed package docs (`README.md`, `docs/extensions.md`, `docs/session-format.md`,
  `docs/security.md`, `docs/compaction.md`), and deterministic unit/lifecycle tests. No authenticated inference was run
  in wave 1.

## Results

- **Launch flags (verified via `pi --help`):** `--model <pattern>` accepts `provider/id` (and an optional `:<thinking>`
  shorthand); `--thinking <level>` accepts `off, minimal, low, medium, high, xhigh, max`; `-e, --extension <path>` loads
  an extension (repeatable); `--no-extensions` disables extension discovery while keeping explicit `-e`; `--approve, -a`
  and `--no-approve, -na` set project-local trust for one run.
- **Trust:** project trust governs resource loading, not process isolation. A dispatched worker cannot answer the
  interactive trust dialog, so cox marks the cox-created worktree trusted with `--approve` at launch. Coxswain does not
  mutate user-level Pi configuration.
- **Model:** Pi supports many providers; the model is explicit `provider/model`. cox never invents a provider from the
  harness name and rejects a bare/empty/provider-less model, or an unsupported thinking level, before spawn.
- **Sessions:** saved as JSONL under `~/.pi/agent/sessions/--<cwd>--/<timestamp>_<session-id>.jsonl`. Assistant entries
  carry a `usage` object (`input`, `output`, `cacheRead`, `cacheWrite`, `cacheWrite1h`, `totalTokens`). Telemetry sums
  the last assistant turn's input-side tokens; a missing, ambiguous (more than one session), or unreadable session is
  unknown, never zero.
- **Lifecycle events (from `docs/extensions.md`):** `session_start` (reason startup/new/resume/fork), `session_shutdown`,
  `session_before_compact`, `agent_end`, and `agent_settled` (the turn-end boundary after which Pi will not continue
  automatically). `pi.sendUserMessage(text, { deliverAs: "followUp" })` queues a message until the agent finishes.
- **Sandbox:** Pi 0.85.1 provides no host-filesystem confinement; it runs with the user's permissions and recommends
  external isolation (`docs/security.md`). The pi card stays `sandbox: false`.

## Known ceilings

- No OS-level filesystem sandbox in Pi 0.85.1; confinement is follow-up scope, not claimed here.
- Orca composer recognition, session-to-worktree association, interrupt/park/resume/relaunch continuity, and live
  extension behavior (push delivery, `/new`/`/resume`/`/fork`, compaction) are unknown until the wave-2 dogfood; they
  are preserved as unknown rather than inferred.

## Executable evidence

- `internal/adapter/harness/pi/` (card, launch, provider/model + thinking validation, telemetry, extension packaging)
- `internal/adapter/harness/pi/extension/` (the Pi extension + `cox-supervisor.test.ts` lifecycle suite)
- `tests/pi-extension/run.sh` (deterministic lifecycle suite runner)
- `cmd/cox/workspace_hooks_test.go`, `cmd/cox/cox_test.go`, `cmd/cox/story_test.go`

The current support contract is [Pi harness adapter](../../adapters/pi.md). No credentials or user-configuration
contents appear in this record or the fixtures.
