# Harness adapter: codex

Codex has no hooks, so wakes are delivered on the pull path and checkpoints are written by the worker itself. This card
is mirrored by `internal/adapter/harness/codex` and checked by a test that reads `Card()` and compares it to this table.

## Capability card

| Field | Value |
|---|---|
| name | codex |
| roles | leader, worker |
| wake | pull |
| checkpoint | manual |
| doorbell | true |
| interrupt | true |
| telemetry | false |
| sandbox | true |
| instructions | AGENTS.md + markdown skills |

## Observed behavior

- **Wake pull.** No hooks: the leader drains at the start of every turn and, when idle, ends its turn on
  `cox wake wait --max 25m`, which blocks until a wake arrives or the deadline passes. The watcher also sends a
  backend doorbell to the leader terminal as a safety net.
- **Checkpoint manual.** No `PreCompact`/`SessionStart` hooks: the worker writes its checkpoint at each phase boundary,
  and a relaunch injects it via the brief's opening line (`cox checkpoint inject`) rather than automatically.
- **Telemetry.** Codex exposes no session log to read, so `Telemetry()` is always Unknown (never 0, F11).
- **Reduced mode risk.** Because the pull path depends on AGENTS.md discipline, a leader codex that does not drain can
  let wakes sit past `WAKE_BATCH`. The backend doorbell is the safety net; if it still lags, the card is the place to
  record reduced mode and to reconsider a codex hook when one exists.
- **Package.** Codex reads AGENTS.md natively; skills are delivered as Markdown out of band.
- **Launch autonomy flags (captain-owned risk).** A dispatched worker cannot answer a local approval prompt, so cox
  types autonomy flags onto the launch line (`backend.LaunchLine`) from policy `harness.launch.codex`. The default,
  verified against codex-cli 0.154.0 on 2026-09-15, is `-a never -s workspace-write` (`--ask-for-approval never
  --sandbox workspace-write`): this machine's interactive `codex` has no `--full-auto` flag, so the pair is what stops
  the "Would you like to run the following command?" prompt that hung the first live codex smoke. **The captain owns
  this risk** (the worker runs shell without per-command approval, sandboxed to workspace-write). Tighten it in policy:
  set `harness.launch.codex` to a stricter pair (e.g. `-a on-failure -s read-only`), or to `[]` to type no flags and
  restore interactive approval (which a dispatched worker cannot answer, so only for a hand-driven terminal). The model
  is also typed (`--model <id>`); see docs/adapters/orca.md for the terminal-plane launch line and the 60s confirm window.
- **Default model per harness.** A codex worker's default model is `harness.worker.models.codex` (`gpt-5.6-sol`), not the
  claude ruling: typing `--model claude-opus-4-8` at codex made it reject the launch ("Model metadata for
  claude-opus-4-8 not found", then API 400 "not supported when using Codex with a ChatGPT account"), M10c. Override per
  story with frontmatter `model: <id>` or per dispatch with `cox story dispatch --model <id>`. The `opus` alias is
  claude-only, so a bare `opus` at codex is left untouched. If policy maps no default for a harness, cox launches with no
  `--model` and the harness picks its own. A cross-harness model is refused at dispatch: an explicit claude-family model
  (`claude-*`) at a codex worker is rejected before spawning; pass `--force-model` to override.
- **Sandbox writable roots (per spawn).** `-s workspace-write` confines codex's writes to the worktree, but a dispatched
  worker also writes into the epic dir (its `cox story report`, `questions/`, and checkpoint under `<epic>/`) and needs
  its Go build cache to run `go test`. A bare launch failed with `mkdir <epic>/questions: operation not permitted`, and
  `go test` failed until GOCACHE was writable (M10c, live codex smoke). So the launch composer (`backend.LaunchLine`)
  keeps the policy flags as the base and appends, per spawn, `--add-dir <epicDir> --add-dir <goCache>` - codex's
  first-class flag for directories writable alongside the workspace (the CLI form of
  `sandbox_workspace_write.writable_roots`; verified against codex-cli 0.154.0 on 2026-09-16). The Go cache resolves the
  way the go tool does: `$GOCACHE`, else `<user cache dir>/go-build`. This is terminal-plane only and codex-only; claude
  has no such sandbox.
- **Git common dir writable root (M14).** A codex worker in a linked worktree could not `git commit`: the shared git
  objects and the per-worktree index live under the main checkout's `.git` (`git rev-parse --git-common-dir`), OUTSIDE
  the worktree, so the workspace-write sandbox denied `.git/worktrees/<name>/index.lock` (M13b q001, "operation not
  permitted"). cox now appends `--add-dir <git common dir absolute>` (from `git rev-parse --path-format=absolute
  --git-common-dir` of the worktree) so the worker commits from inside the sandbox. Verified against codex-cli 0.154 on
  2026-09-16; empty (no root) when the path is not a git worktree.
- **Loopback in the workspace-write sandbox (M14).** The sandbox blocks loopback binds, so a codex worker could not run
  the repo's `httptest` suites (M13b: `httptest.NewServer` cannot bind loopback) and the leader had to rerun
  `go test -race ./...` outside it. Codex's own help states: "In `workspace-write`, network access still depends on your
  Codex configuration (for example `[sandbox_workspace_write] network_access = true`)" (codex-cli 0.154, 2026-09-16). So
  cox types `-c sandbox_workspace_write.network_access=true` on a workspace-write codex worker launch. **Captain-owned
  risk**, like the autonomy flags: it grants the sandboxed worker full network, not loopback only (codex exposes no
  loopback-only toggle). Read-only sandboxes and arena roles get neither this flag nor the writable roots.
- **Doorbell on the terminal plane (M14).** The watcher rings a codex worker terminal through Orca's structured agents[]
  state first (`idle`/`done` -> ring, `working` -> defer, `waiting` -> permission-blocked), falling back to text
  classification only when the structured state is unreadable. The text classifier now recognizes the codex composer
  footer (the `Ask Codex to do anything` placeholder is empty, the `gpt-5.6-sol . high . Context NN% used . weekly NN%
  left` status line is the status row), so an idle codex composer rings instead of classifying unknown forever. When
  Orca refuses the typed prompt with `agent_prompt_blocked` (the pane is waiting on a local approval), cox treats it as
  `skipped:permission`: the watcher bumps the ring ladder and notes "orca refused typed prompt (permission pending)", so
  an undeliverable steer still escalates to `stuck` after `ringMax` instead of deferring silently. A human typing into
  the Orca UI is not subject to Orca's permission check, so the captain can always type into the leader terminal.

## Leader hooks (verified against codex-cli 0.154, 2026-09-16)

Codex 0.154 does have hooks, so the codex leader can sleep on `Stop` exactly like the claude leader, instead of the
pull path's busy `cox wake wait`. `cox workspace hooks --harness codex --root <clone> --epic <dir>` installs them.

- **Hook events (config keys).** Verified from the codex binary and the official docs (developers.openai.com/codex/hooks,
  now learn.chatgpt.com/docs/hooks): `SessionStart`, `SessionEnd`, `PreToolUse`, `PostToolUse`, `PermissionRequest`,
  `PreCompact`, `PostCompact`, `UserPromptSubmit`, `SubagentStart`, `SubagentStop`, `Stop`, `Interrupt`. cox maps four:
  `Stop -> cox hook stop-rewake`, `UserPromptSubmit -> cox hook prompt-drain`, `SessionStart -> cox hook session-start`,
  `PreCompact -> cox hook precompact` (matcher `manual|auto`), the same handlers as the claude leader.
- **File and location.** Codex reads a project-level `<repo>/.codex/hooks.json` additively alongside the user's
  `~/.codex/hooks.json`; hooks from every trusted layer run, higher layers do not replace lower ones. So cox writes a
  cox-owned project file at `<clone>/.codex/hooks.json` and **never touches** `~/.codex/hooks.json` (the Orca and
  agentkit entries there are left alone). The install is idempotent: a re-run drops the prior `cox hook ` groups and
  writes the same four back, preserving any non-cox entries.
- **Trust (tech lead, after merge).** A project-local hooks layer runs only once codex trusts it (codex records a
  `trusted_hash` under `[hooks.state]` in `config.toml` on first trusted run). cox does not modify `~/.codex/config.toml`;
  the tech lead trusts the project hooks when installing for real. `[features] hooks = true` is not required (hooks are
  on by default in 0.154).
- **stdin payload (snake_case, differs from claude).** `UserPromptSubmit`: `session_id, transcript_path, cwd,
  hook_event_name, model, permission_mode, turn_id, prompt` (cox reads `prompt`, same field name as claude, so
  `prompt-drain` is unchanged). `SessionStart`: `+ source`. `PreCompact`: `session_id, cwd, hook_event_name, model,
  turn_id, trigger`. `Stop`: `+ stop_hook_active, last_assistant_message`. cox's hooks key off `--epic`/`--story` flags
  and `ORCA_TERMINAL_HANDLE`, not payload internals, so only the block signal (below) differs by harness.
- **Block / continue signal (adapter translation).** Claude reopens a turn with exit 2; codex reads a stdout JSON
  decision. The installed commands carry `--harness codex`, so `cox hook` emits `{"decision":"block","reason":"..."}`
  on stdout (exit 0) where the claude path would exit 2: `stop-rewake` reopens the turn with the wake instructions, and
  `prompt-drain` discards a stale doorbell. `UserPromptSubmit` stdout otherwise becomes added turn context on both
  harnesses, unchanged.
- **Sleep responsiveness.** The `Stop` hook is installed `async: true` with `timeout: 3600` (codex's analog to claude's
  `asyncRewake`), so the composer stays interactive while the hook waits and the human escape hatch (typing into the
  leader terminal) still works. The wait loop (`cox hook stop-rewake`) is the same cox binary polling the durable wake
  queue regardless of harness.
- **Guard.** Each hook self-guards via `.cox/leader` (`notLeaderTerminal`): a terminal whose `ORCA_TERMINAL_HANDLE` is
  not the recorded leader no-ops, and the checkpoint hooks additionally guard on `story == _leader`. Project-level
  discovery already scopes the hooks to the workspace cwd, so a worker worktree (no `.codex/hooks.json`) never fires them.
- **Doctor.** With the cox codex hooks present in `<cwd>/.codex/hooks.json`, `cox doctor` shows the codex card as
  `wake=push` (the `Stop` hook delivers wakes); the card default stays `pull` for a codex with no hooks installed.
- **Remaining limitation (live confirm, tech lead after merge).** Fixtures and unit tests cover the payloads and the
  block-signal translation, but the end-to-end "leader sleeps at `Stop`, a wake reopens a fresh turn" behavior is
  confirmed live by the tech lead. If an `async` `Stop` hook does not reopen the turn on some codex build, the pull path
  (`cox wake wait`) and the backend doorbell remain installed as the safety net, so wakes are never lost.
