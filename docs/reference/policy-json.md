# `policy.json` reference

`cox/policy.json` holds the workspace's behavioral defaults and the rationale for each. `cox workspace init` seeds it
from `templates/policy.json`; the authoritative shape is `internal/workspace/policy.go`. This page is kept in parity
with the Go type by `internal/workspace/docs_parity_test.go`, which fails if a field is added or renamed in Go without a
matching row here.

A `<project>/cox/policy.json` may replace whole top-level sections (see [Configuration authority](configuration.md)).
Every *justified* section (`workers_per_repo`, `waves`, `context`, `arena`, `delivery`, `harness`) must carry both a
`why` and a `review_when`, or the file fails to load with the section named. The remaining sections
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
| `delivery` | The story delivery style resolved into each story. |
| `harness` | Model-agnostic harness options, defaults, and launch flags. |
| `routing` | The harness-routing baseline (a `review_when` default, not a hard rule). |
| `backend` | Per-backend switches (today only Orca). |
| `quota` | Observe-only quota thresholds. |
| `review` | Visual-review surface. |

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

### `harness`

Model-agnostic (decision 8): the leader is not locked to one harness.

| Field | Type | Meaning |
|---|---|---|
| `leader` | object | Leader role: `options` (allowed harnesses), `default`. |
| `worker` | object | Worker role: `options`, `default`, per-harness `models` (harness → default model id), and the legacy single `model` (read as claude's default). |
| `arena` | object | Arena roles: `adversary` (`rule`, `default`) and `reviewer` (`rule`). |
| `launch` | object | Per-harness launch flags. A top-level `<harness>` key lists a dispatched worker's autonomy flags; the nested `arena` key holds each harness's read-only arena flags. |

`options`, `default`, `model`, `models`, `adversary`, `reviewer`, and `rule` are the fields inside these objects.

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

For exact defaults, read `templates/policy.json`. For where a change belongs, see
[Configuration authority](configuration.md).
