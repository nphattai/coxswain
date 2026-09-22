# `policy.json` reference

`cox/policy.json` holds the workspace's behavioral defaults and the rationale for each. `cox workspace init` seeds it
from `templates/policy.json`; the authoritative shape is `internal/workspace/policy.go`. This page is kept in parity
with the Go type by `internal/workspace/docs_parity_test.go`, which fails if a field is added or renamed in Go without a
matching row here.

A `<project>/cox/policy.json` may replace whole top-level sections (see [Configuration authority](configuration.md)).
Every *justified* section (`workers_per_repo`, `waves`, `context`, `arena`, `delivery`, `harness`, `merge`) must carry
both a `why` and a `review_when`, or the file fails to load with the section named. The remaining sections
(`routing`, `backend`, `quota`, `review`) are optional: an older policy without them keeps working on code defaults.

## Justification, on every justified section

| Field | Type | Meaning |
|---|---|---|
| `why` | string | Why the rule exists: a measurement or a captain ruling. |
| `review_when` | string | The condition under which the value should be revisited. |

## Top-level sections

| Section | Meaning |
|---|---|
| `workers_per_repo` | How many workers may share one repo, and the condition that unlocks more. |
| `waves` | Wave ordering for stories in an epic. |
| `context` | Compaction thresholds in tokens. |
| `arena` | When adversarial design review is triggered. |
| `delivery` | The story delivery style and merge mode resolved into each story. |
| `harness` | Model-agnostic harness options, defaults, and launch flags. |
| `merge` | Merge posture: whether a non-captain terminal may run `cox ship merge`. |
| `routing` | The harness-routing baseline (a `review_when` default, not a hard rule). |
| `backend` | Per-backend switches (today only Orca). |
| `quota` | Observe-only quota thresholds. |
| `review` | Visual-review surface. |
| `alerts` | Out-of-band notification channel for a leader that has gone unreachable. |
| `watch` | Watcher-window overrides (optional; code defaults otherwise). |

### `workers_per_repo`

| Field | Type | Meaning |
|---|---|---|
| `value` | int | Workers allowed per repo per epic (default 1). |
| `allow_parallel_when` | object | The disjointness condition: `files_owned` (e.g. `"disjoint"`) and `enforced` (true only when a scheduler rejects an overlap). |

### `waves`

| Field | Type | Meaning |
|---|---|---|
| `value` | string | `"backend-first"` (clients depend on the backend story) or `"single"`. |

### `context`

| Field | Type | Meaning |
|---|---|---|
| `plan_compact` | int (tokens) | Plan-compact threshold: finish the phase and checkpoint. |
| `compact_now` | int (tokens) | Compact-now threshold: checkpoint immediately. |

### `arena`

| Field | Type | Meaning |
|---|---|---|
| `trigger` | array of string | The conditions that trigger a full arena, e.g. `"3+ repos"`, `"migration"`, `"money"`, `"auth"`, `"PII"`, `"captain request"`. |

### `delivery`

| Field | Type | Meaning |
|---|---|---|
| `style` | string | `"default"` (draft PR at the plan gate, push every phase) or `"pipo"` (commits stay local, one push, PR opened ready). |
| `mode` | string | Merge posture resolved into each story and printed in the brief (item 8): `"no-mistakes"` (full gates + PR + wait for merge authority), `"direct-PR"` (push + PR, no extra pipeline; the default that matches today), or `"local-only"` (a clean ready branch, no push, wait). Empty reads as `direct-PR`; any other value fails to load. |

### `harness`

Model-agnostic (decision 8): the leader is not locked to one harness.

| Field | Type | Meaning |
|---|---|---|
| `leader` | object | Leader role: `options` (allowed harnesses), `default`. |
| `worker` | object | Worker role: `options`, `default`, per-harness `models` (harness → default model id), and the legacy single `model` (read as claude's default). |
| `arena` | object | Arena roles: `adversary` (`rule`, `default`) and `reviewer` (`rule`). |
| `launch` | object | Per-harness launch flags. A top-level `<harness>` key lists a dispatched worker's autonomy flags; the nested `arena` key holds each harness's read-only arena flags. |
| `busy_verified` | bool | Opt codex into the harness-owned busy record (DESIGN wave-2 item 6). Default false: codex is not armed at dispatch and writes no busy record until this is set, which vouches that a `codex-hook` writer is wired. `claude` and `pi` report their own state from their capability cards, so this flag governs only codex. It never selects a harness or changes routing. |

`options`, `default`, `model`, `models`, `adversary`, `reviewer`, and `rule` are the fields inside these objects.

### `merge`

Justified (item 8). `cox ship merge` is the single merge command; it merges only an open, non-draft, mergeable PR whose
every check is green at the live head, pins that head to the forge, and reads the result back.

| Field | Type | Meaning |
|---|---|---|
| `yolo` | bool | Default `false`: `cox ship merge` is refused unless the captain runs it (`--captain`), so the green-at-live-head rule is enforced rather than remembered, and a worker terminal (`COX_STORY` set) never merges. `true` lets a non-captain terminal merge; the captain owns that risk. |

### `routing`

| Field | Type | Meaning |
|---|---|---|
| `default` | string | `"policy"`: fall back to the harness default until the baseline bar is met. |
| `review_when` | string | The baseline-row bar that unlocks a non-default choice. |

### `backend`

| Field | Type | Meaning |
|---|---|---|
| `orca` | object | Orca driving plane: `plane` (`"orchestration"` or `"terminal"`) and `launch_confirm_s` (terminal-plane spawn confirm window in seconds). |

### `quota`

Observe-only; `cox` never reroutes automatically.

| Field | Type | Meaning |
|---|---|---|
| `binary` | string | Overrides the PATH lookup for `quota-axi`. |
| `npx` | object or null | The explicit npx opt-in: `version` and `integrity`. `null` means npx is never used. |
| `low_percent` | int | Low-but-not-exhausted warning threshold. |
| `ok_percent` | int | The percentage at which a harness is considered healthy again. |
| `min_runway_hours` | int | Minimum runway before a low-quota wake. |
| `poll_minutes` | int | How often quota is polled. |
| `health_debounce_minutes` | int | Debounce window for health wakes. |

### `review`

| Field | Type | Meaning |
|---|---|---|
| `surface` | string | `"lavish"` or `"none"`. |
| `binary` | string | Overrides the PATH lookup for `lavish-axi`. |
| `npx` | object or null | The explicit npx opt-in: `version` and `integrity`. |
| `share` | bool | Whether outward-facing publishing is allowed (defaults false). |

### `alerts`

Optional. The watcher fires this channel, rate-limited to one notification per 30 minutes, when the leader terminal has
gone unreachable for three consecutive doorbell nudges (a dead handle after a restart, a closed terminal). Unset means
`off`, so `cox` never posts a notification unless the captain opts in.

| Field | Type | Meaning |
|---|---|---|
| `channel` | string | `"off"` (default), `"osascript"` (a macOS Notification Center banner), or `"command:<cmd>"` (runs `<cmd>` via `sh -c` with the alarm summary as `$1` and on stdin, for a phone or pager). |

### `watch`

Optional watcher-window overrides; an absent section keeps the code defaults.

| Field | Type | Meaning |
|---|---|---|
| `busy_turn_max_min` | int (minutes) | How long a story's busy record may stay busy - with no fresh busy event and no fresh checkpoint - before the watcher raises one routine `status` wake for the leader (DESIGN wave-2 item 6d). `<=0` or unset uses the default (60 minutes). It is a nudge, never an interrupt. |

For exact defaults, read `templates/policy.json`. For where a change belongs, see
[Configuration authority](configuration.md).
