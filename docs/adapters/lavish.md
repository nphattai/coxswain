# lavish-axi review adapter

The optional review surface for M13. It shells out to a pinned `lavish-axi` binary to open a `coxswain.artifact.v1`
page in a local browser and to poll once for the reviewer's feedback, which cox records as `inbox.v1` files and wakes.
It is an **optional** adapter: cox runs without it (`cox review open` prints the path, feedback falls back to chat,
ADR 0001). Source: `internal/adapter/review/lavish/`. lavish is never forked and never authoritative - the artifact and
the record are cox's; `cox arena answer` is the one write path for a synthesis cell.

## Capability card

| Capability | lavish adapter | Notes |
|---|---|---|
| verified version | `0.1.64` installed (`lavish-axi --version`); source read at `0.1.69` | the poll output contract (below) is stable across these |
| invocation | `lavish-axi <file>` (open), `lavish-axi poll <file> --timeout-ms <ms>` (poll), `lavish-axi poll <file> --timeout-ms <ms> --agent-reply <message>` (reply), `lavish-axi share <file>` (opt-in) | installed binary by default (`review.binary` overrides PATH); npx only via `review.npx` opt-in with an exact version |
| output format | TOON | `axi-sdk-js` always encodes with `@toon-format/toon`; there is **no** `--json` (checked `lavish-axi poll --help` and the sdk `output.js`). cox decodes the poll TOON with a stdlib decoder |
| bounded poll | `--timeout-ms` | lavish reserves `--timeout-ms` for tests, but cox's supervised bounded subprocess is exactly that case; it is the only way to get a `question.Wait`-style timeout. The process context outlives it by 30s for cold start |
| environment | clean | only `PATH` (Node shebang) and `HOME` (lavish state dir) are passed; no other inherited variable |
| shell | never | `exec` with an argv, no `sh -c` |
| ack semantics | **delivery is the ack** | `session-store.js` `session.prompts = []` on poll: no separate ack, so a crash between delivery and the synchronous record write loses that feedback. cox reports the loss with the raw payload, never silently |
| sharing | refused by default | `cox review share` refuses without policy `review.share` or `--share`; ht-ml.app is third-party and public by default (outward-facing) |

## Poll output cox reads

`lavish-axi poll` prints a TOON document. cox reads `session.status` and, for `feedback`, the `prompts` array:

- `session.status`: `feedback` | `ended` | `browser_disconnected` | `waiting`/timeout (and `session_ended`/`ended_by`
  on a `Send & End`).
- each prompt: `tag`, `selector` (opaque element anchor) and/or a structured `target`, `text` (quoted/selected text),
  `prompt` (the reviewer's message), and an optional `claim_id`/`value`.

Mapping: `feedback` -> one `inbox.v1` fyi record per prompt + a wake (`review_feedback` routine, or `review_decision`
urgent for a `decision`/`answer` tag); final feedback with `session_ended` records both feedback and the end, then exits
4; `ended` -> exit 4; `browser_disconnected` -> exit 5; timeout -> exit 3. Each terminal result writes a record and a
routine wake. An `answer <id> <value>` prompt is executed through `cox arena answer` after a sidecar sha check.

## Limits

- **ack = delivery** (above): the loss window is delivery to the synchronous record write in the same process. Filed
  upstream: stable feedback ids plus explicit ack and redelivery.
- The TOON decoder covers the poll shape only (nested objects, scalar/tabular/list arrays, TOON escapes), not a general
  TOON library. It is Go-stdlib-only.
- Whiteboard (`whiteboard` tag) and layout-warnings feedback are recorded like any prompt; cox does not act on them
  automatically (it never edits the artifact to chase a layout issue the reviewer did not queue).

## Live check (tech lead, post-merge)

`tests/e2e/review-live.sh`: generate the epic v2 design artifact, `cox review open` it, annotate once and send a
decision, and confirm the inbox record, the wake, and `cox arena answer` running. Never run `lavish-axi share`.
