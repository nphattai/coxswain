# Pi CLI 0.86.1 compatibility evidence (wave-2 real-tool dogfood)

- **Observed:** 2026-09-21 (Mac mini), wave-2 `pi-harness-dogfood` real-tool run
- **Environment:** `@earendil-works/pi-coding-agent` 0.86.1 (`pi --version` -> `0.86.1`); candidate `dist/cox`
  `v2.0.0-rc.1-68-g1676877` built from `epic/pi-harness`; pinned driver on PATH `v2.0.0-rc.1-56-g4dbd96f` (unchanged).
  Model `openai-codex/gpt-5.5` (captain-authenticated provider). Backend Orca 1.4.197, terminal plane, disposable
  workspace + repo.
- **Method:** live dispatch of Pi workers and a Pi leader through the candidate `dist/cox`, plus the deterministic
  adapter/extension suites. No credentials or user-config contents appear in this record.

## Confirmed against 0.86.1 (unchanged from the 0.85.1 record)

- **Launch flags:** `--model provider/model`, `--thinking <level>`, `-e/--extension <path>` (repeatable), `--no-extensions`,
  `--approve`. The rendered worker launch line is
  `pi --model openai-codex/gpt-5.5 --approve --no-extensions -e <cox-pi.ts> '<story prompt>'` and the status bar shows
  `(<provider>) <model> • <thinking>` (e.g. `(openai-codex) gpt-5.5 • medium`).
- **Trust:** `--approve` marks per-run project trust; it is NOT persisted (`~/.pi/agent/trust.json` unchanged across the
  whole dogfood). cox never mutates user-level Pi config (`settings.json`/`trust.json`/`auth.json` untouched by cox).
- **Model validation:** a bare, provider-less, or empty model fails before spawn with a bounded diagnostic.
- **Sessions/telemetry:** JSONL under `~/.pi/agent/sessions/--<cwd>--/`; assistant `usage` carries
  `input/output/cacheRead/cacheWrite/totalTokens`; story-bound telemetry reads the worktree session (observed Known
  usage, e.g. input=5150 total=5212). Missing/ambiguous/unreadable -> Known=false.
- **Lifecycle events:** `session_start`, `session_before_compact`, `session_shutdown`, `agent_settled`;
  `pi.sendUserMessage(text,{deliverAs:"followUp"})` queues a message until the turn ends.
- **Sandbox:** no host-filesystem confinement; card stays `sandbox: false`; every Pi worker dispatch requires recorded
  `--allow-unsandboxed` authority (re-verified live, both directions).

## New real-tool observations (0.86.1)

- **Extension activation confirmed live:** the packaged extension loads via `-e`, writes `.cox-pi.activated`, and runs
  `session_start`; dispatch confirms it (no reduced-mode downgrade when it activates).
- **Fresh-session checkpoint injection fix:** on a fresh worker the extension previously delivered the "no checkpoint,
  start from the story" notice as a followUp, causing a redundant second turn and a DUPLICATE `worker_done`. Fixed in
  `internal/adapter/harness/pi/extension/cox-pi.ts` (inject only when a checkpoint exists); fail-on-old-code test in
  `cox-pi.test.ts`. Verified live: fresh run now emits exactly one completion.
- **Resume/relaunch:** attempt is bumped; a prior-attempt checkpoint is correctly refused on inject (F07, surfaced
  visibly); continuity comes from the relaunch progress note + worktree re-read. Leader restart reads its `_leader`
  checkpoint and inbox first, then picks up pending wakes with no lost/duplicate wake.
- **Leader push wake:** the leader extension's generation-scoped `cox wake wait` child delivers wakes as followUps (no
  polling, no heartbeat); drain/ack settles the queue to 0.

## Known ceilings re-confirmed live (0.86.1) - see reports/evidence/pi-dogfood/FINDINGS-for-leader.md

- **Orca does not register a Pi `agents[]` entry**, and the backend composer classifier is Claude/Codex-only, so a Pi
  pane classifies as `unknown` in every state. Consequences: (F-A) the backend doorbell/steer is never delivered to a Pi
  worker; backend liveness/composer read `unknown` while the worker is active (matches DESIGN §5, never fabricated).
- **(F-C)** `cox control interrupt` (`orca terminal send --interrupt`) is a no-op on the Pi TUI; the session stays
  healthy and no false completion fires.
- **(F-D)** the leader push loop storms nudges while a wake backlog is unacked (self-heals on ack; no lost/duplicate
  wake).
- **Pi quota** remains unknown (no authoritative source for the provider).

These are wider than the Pi adapter/extension/launch seam (shared Orca backend / Orca agent registration / wait-ack
contract) and overlap the out-of-scope L4 idle/busy gate; recorded for the leader, not fixed in this story.
