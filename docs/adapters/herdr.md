# Herdr backend adapter

The optional backend (decision 0002), completed to full capability in M10 (ADR 0012). Herdr is an agent-native terminal
runtime with native per-pane agent state. Like Orca's terminal plane, herdr provides worktrees and terminals only and
cox owns the whole handoff (report/reply/wake), so herdr needs no orchestration mailbox, template, or completion cap.
Task worktrees are plain git worktrees the adapter creates (herdr does not own them); a worker runs in a herdr pane the
adapter opens in that worktree and types the harness launch into. Source: `internal/adapter/backend/herdr/`.

## Capability card (herdr 0.8.2)

| Capability | Herdr | Notes |
|---|---|---|
| roles | worker (leader possible) | firstmate uses herdr for worker panes |
| worktree create | **yes** | plain `git worktree add [-b] <branch> <path> <base>` under the workspace `worktree_base`; A11 branch reuse (reset to base only when base..branch is empty, else refuse) |
| worktree remove | **yes** | `git switch --detach` then `git -C <main> worktree remove --force <path>`; git worktree remove never deletes the branch (F01) |
| spawn | **yes** | `workspace create --cwd <wt> --label <story> --no-focus`, resolve the pane via `pane list --workspace <ws>`, then `pane send-text <pane> <launch>` + `pane send-keys <pane> Enter` |
| send (doorbell) | **yes** | `pane send-text <pane> <text>` + `pane send-keys <pane> Enter`; a herdr pane is always writable, so a successful send is a real ring |
| interrupt | **yes** | `pane send-keys <pane> C-c` |
| stop | **yes** | `pane close <pane>`, confirmed by a settled probe; an unconfirmed close keeps ownership (F03/F04/F05) |
| probe | **yes** | native `agent get <pane>` agent state |
| composer | **reduced (unknown)** | herdr's composer classifier (firstmate `bin/fm-composer-lib.sh`) is UI-shape specific and not reproduced; always "unknown", so the watcher never fires idle_no_done on herdr |
| mailbox | **reduced (none)** | no orchestration mailbox; Check is empty and the watcher wakes only from wake.jsonl, steers use the durable inbox (same as the terminal plane) |
| worker list | **reduced (refused)** | herdr has no run-scoped dispatch listing; a caller falls back to Probe |
| remote hosts | n/a | not in scope |

Verified: WorktreeCreate/Remove against a real temp git repo; Spawn/Send/Interrupt/Stop/Probe command shapes against a
fake herdr CLI that records the emitted commands. The `workspace create` / `pane list` / `agent get` JSON field names
(`result.workspace.workspace_id`, `result.panes[].pane_id`, `result.agent.agent_status`) are the ones firstmate reads
from herdr. A live Spawn smoke on herdr 0.8.2 (the server was not running on this machine during M10) is left to the
tech lead's post-merge smoke.

**Ceiling (ponytail):** Spawn creates one herdr workspace per worker; Stop closes the pane but not the workspace, so an
empty workspace is left behind. herdr's own presentation cleanup would remove it; cox does not, to avoid reproducing
firstmate's large focus-preserving projection layer. Prune stale empty workspaces with `herdr workspace list` by hand.

## Observed behavior

Command shapes and JSON field names sourced from firstmate `docs/herdr-backend.md` and `bin/backends/herdr.sh`
(read-only) and the herdr 0.8.2 CLI surface (`herdr pane|agent|workspace|tab <sub>`).

### Probe: native agent state

`herdr agent get <pane_id> --session <s> --json` returns `.result.agent.agent_status`, one of `working`, `idle`,
`done`, `blocked` (any value means a registered, live agent). Firstmate's recovery-grade classifier
(`fm_backend_herdr_agent_state`) distinguishes:

- `pane get` -> `pane_not_found` => the pane is structurally gone (firstmate: `missing`).
- `agent get` -> `agent_not_found` => the pane is alive but the agent is unregistered, e.g. a restart husk
  (firstmate: `dead`).
- `agent get` ok with any `agent_status` => a registered agent (firstmate: `alive`).
- anything else / unreadable => firstmate: `unreadable`.

coxswain liveness mapping (`classify`):

| herdr result | coxswain Liveness |
|---|---|
| `agent_status` working/idle/done/blocked | Alive |
| error code `agent_not_found` | Settled |
| error code `pane_not_found` | Settled |
| ok but no `agent_status`, or unrecognized status/code, or unreadable | Unknown (with error) |

A native **`idle`** agent is still a registered, live agent, so it is **Alive**: idleness is a semantic-busy question,
not a liveness one. Firstmate's own rule matches - it "never [treats] a native `idle` as evidence that a worker has
stopped" (docs/herdr-backend.md, Current transport behavior). A failed or unreadable probe is Unknown, never inferred
as gone (F08).

### Endpoint identity

A herdr endpoint is `window=<session>:<pane-id>` plus `herdr_session`, `herdr_workspace_id`, `herdr_tab_id`,
`herdr_pane_id`. A herdr pane id contains a colon, so any split on `window=` is on the first colon only. The adapter's
`Session` carries the session name (Handle) and pane id (ID).

## Contract tests

Live herdr tests belong behind `//go:build herdr`. The adapter is unit-tested with an injected command runner (a fake
herdr CLI that records commands) and a real temp git repo for the worktree paths (`herdr_test.go`), no herdr install
required. `cox doctor` / `cox story dispatch` still run the documented reduced mode for Composer and WorkerList.
