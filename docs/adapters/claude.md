# Harness adapter: claude

Claude Code, driven through hooks. This card is mirrored by `internal/adapter/harness/claude` and checked by a test
that reads `Card()` and compares it to this table, so the doc never drifts from the code.

## Capability card

| Field | Value |
|---|---|
| name | claude |
| roles | leader, worker |
| wake | push |
| checkpoint | auto |
| doorbell | true |
| interrupt | true |
| telemetry | true |
| sandbox | false |
| instructions | plugin skills + AGENTS.md |

## Observed behavior

- **Wake push.** Hooks deliver wakes without a poll: `UserPromptSubmit` attaches unread wakes to the leader's turn, and
  `Stop` (asyncRewake) reopens a turn when an urgent wake is queued. The four shims in `hooks/` are three-line execs
  into `cox hook <name>`.
- **Checkpoint auto.** `PreCompact` runs `cox hook precompact`, which refreshes the `cox checkpoint facts` block in the worker's
  handoff; `SessionStart(compact|resume)` runs `cox hook session-start`, which injects the checkpoint with the
  freshness check.
- **Telemetry.** Token and turn counts are read from the newest session log under
  `~/.claude/projects/<worktree-path-with-slashes-as-dashes>/*.jsonl` (v1 `session_ctx`). No log resolves to Unknown,
  never 0 tokens (F11).
- **Package.** AGENTS.md is the harness-neutral job description; Claude reads it via `CLAUDE.md` (symlink, or a copy).
  Skills are delivered as plugin skills out of band.
- **Launch autonomy flags (captain-owned risk).** A dispatched worker cannot answer a local permission prompt, so cox
  types autonomy flags onto the launch line (`backend.LaunchLine`) from policy `harness.launch.claude`. The default,
  verified against claude 2.1.272 on 2026-09-15, is `--permission-mode bypassPermissions`. **The captain owns this
  risk** (the worker runs tools without per-call permission checks). Tighten it in policy: set `harness.launch.claude`
  to a stricter mode (e.g. `--permission-mode acceptEdits`), or to `[]` to type no flags and restore the default
  permission prompt (which a dispatched worker cannot answer, so only for a hand-driven terminal). The model is also
  typed (`--model <id>`); see docs/adapters/orca.md for the terminal-plane launch line.
- **Default model per harness.** A claude worker's default model is `harness.worker.models.claude` (`claude-opus-4-8`,
  the captain ruling), with the legacy `harness.worker.model` key still read as claude's default so a pre-map policy
  keeps working; with no policy at all, claude still falls back to `claude-opus-4-8`. Override per story with frontmatter
  `model: <id>` or per dispatch with `cox story dispatch --model <id>`; the `opus` alias resolves to `claude-opus-4-8`
  (never Opus 5, captain 2026-09-03). A cross-harness model is refused at dispatch: an explicit codex-family model
  (`gpt-*` or the o-series) at a claude worker is rejected before spawning; pass `--force-model` to override.
- **Doorbell prompt has a leading newline.** Orca types its terminal doorbell as `"\nYou have N orchestration
  message(s). Run \`orca orchestration check\` ..."`. The suppression regex in `runPromptDrain` is `^`-anchored, so it
  must match `strings.TrimSpace(prompt)`, not the raw prompt, or the stale bell slips through and costs the leader a
  turn on every worker heartbeat (A12).
- **Arena role: plan mode is headless-only; terminal uses acceptEdits (ADR 0013).** An arena role launches with the
  read-only `harness.launch.arena.claude` flags, never the worker bypass flags. Verified against claude 2.1.272 (`--help`,
  2026-09-16): `--permission-mode plan` is a read-only mode - Claude produces a plan and does not write files or run
  mutating tools - and `--add-dir` only grants a tool *read* access to extra directories; it does **not** lift plan mode's
  write restriction, so a plan-mode role cannot write its report even to the epic dir. **Plan mode is therefore the
  headless path only:** cox runs the role with `claude -p --model <m> --permission-mode plan --output-format json
  --no-session-persistence` (prompt on stdin, run in the leader checkout), reads the single-object JSON `result`, extracts
  the report from a fenced ```report block, and writes `reports/arena/round-N-<role>.md` itself. In `--terminal` mode a
  plan-mode role could only produce a plan for a human to observe, so the terminal arena flag is
  `--permission-mode acceptEdits` (`harness.launch.arena.claude`): the role writes its report in its own worktree and
  `cox arena collect` brings it home. Headless remains the default and is the read-only path.
