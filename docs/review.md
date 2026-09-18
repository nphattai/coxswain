<span id="visual-review-m13" aria-hidden="true"></span>

# Visual review

cox turns the artifacts a captain must judge - an epic DESIGN.md, the arena synthesis, phase plans, the target
architecture - into review pages, opens them in an optional browser surface, and records the reviewer's feedback as
durable cox files. The decision (arena round 1, `epics/m13-lavish-visualize/DESIGN.md`) is Option C corrected: **cox owns
the artifact and the record; `lavish-axi` is an optional review surface behind an adapter, never forked**, and the one
authoritative write for a synthesis cell stays `cox arena answer`.

## The artifact contract (`coxswain.artifact.v1`)

A review page lives at `<epic>/reports/visual/<name>.html` with a sidecar `<name>.artifact.json`:

```json
{ "schema": "coxswain.artifact.v1", "kind": "design|plan|arena|board|comparison",
  "sources": [{ "path": "DESIGN.md", "sha256": "..." }],
  "generated_at": "<RFC3339 timestamp>", "generator": "cox|leader", "synthesis_sha": "<12-hex, kind=arena>" }
```

`cox artifact list --epic <dir>` lists the pages and their sidecars.

## Generators (cox-owned, self-contained)

All render a self-contained page (the board's CSS inlined, no CDN) plus a sidecar under `reports/visual`:

| Command | Page | Notes |
|---|---|---|
| `cox epic design --html --epic <dir>` | `design.html` | DESIGN.md, decisions in force (from the first repo's `docs/decisions`), latest arena synthesis |
| `cox plan --html <plan-dir> --epic <dir>` | `plan-<slug>.html` | plan.md and each `phase-*.md` with its status |
| `cox arena synth --html --round N --epic <dir>` | `arena-synth-round-N.html` | claims by role with tier, verified, verdict; a `data-claim-id` answer input on a captain_decision or unanswered row; sidecar carries `synthesis_sha` |
| `cox plan compare --html <plan-dir> <architecture.html> --epic <dir>` | `plan-compare.html` | architecture sections vs plan phases, flagging either side with no counterpart |
| `cox board --out <file> --epic <dir>` | (board) | also writes a `kind=board` sidecar |

Markdown to HTML is a minimal stdlib renderer (heading, list, table, code, link, bold, blockquote). Its ceiling: no
nested lists, reference links, inline images, HTML passthrough, or setext headings - the epic files use none. Extend the
renderer (`internal/artifact/markdown.go`), never add a dependency. Every source string is HTML-escaped and
`javascript:` links are dropped.

Richer pages the generators cannot express are **hand-authored by the leader** per a lavish playbook
(`lavish-axi playbook diagram|comparison|...`); a hand-authored page still carries a sidecar with `generator: leader`.
See `skills/cox-visualize`.

## Open, poll, record

- `cox review open <artifact> --epic <dir>` runs `lavish-axi <file>` so the reviewer opens it in a browser. Without
  lavish it prints the path and how to open it, exit 0 - the review degrades to path plus chat. When Lavish is
  available, cox also prints a loopback `http://127.0.0.1:<port>/session/<id>` URL. Feedback is delivered only after
  the reviewer selects **Send to Agent** in the page.
- `cox review poll <artifact> --epic <dir> [--max 25m]` runs one **bounded** `lavish-axi poll --timeout-ms <max>`
  (a separate subprocess, never inside the watcher). Every delivered feedback item becomes an `inbox.v1` **fyi** record
  for story `_leader` (schema `coxswain.review-feedback`) carrying the opaque anchor, the quoted text, the message, and
  the tag, plus a wake:
  - `review_feedback` (routine) for a comment;
  - `review_decision` (urgent) for a `decision` or `answer` tag.
- `cox review reply "<message>" <artifact> --epic <dir> [--max 25m]` mirrors the leader's response into the page with
  `lavish-axi poll --agent-reply`, then waits for the next delivery. It writes the same records and wakes and returns
  the same exit codes as `review poll`.
- Exit codes: `0` feedback delivered, `3` timed out, `4` session ended, `5` browser disconnected. Final feedback sent
  with **Send & End** is recorded and returns `4`, so callers stop polling. A terminal case also writes a record and a
  routine wake.

### Review conversation loop

Keep the conversation visible in both places. After feedback arrives, handle it in the agent chat. If the reviewer
asked a question about the artifact's content, answer it from chat and mirror that answer into the page while waiting
for the next response:

```sh
cox review open reports/visual/design.html --epic "$EPIC"
cox review poll reports/visual/design.html --epic "$EPIC" --max 25m
# Answer the delivered question in chat, then mirror that answer into Lavish:
cox review reply "The reviewer role checks evidence quality; the adversary tries to break the proposal." \
  reports/visual/design.html --epic "$EPIC" --max 25m
```

Repeat `cox review reply` after each response until it returns `4`. A timeout (`3`) is resumable. A plain browser
disconnect (`5`) is also resumable after the captain reopens the page; a disconnect that ends the session arrives as
final feedback and returns `4`.

### Answering a synthesis claim from the artifact

The arena artifact renders `data-claim-id` inputs. The lavish SDK may not transfer a form, so the fallback is the
`input` playbook: the captain sends a structured prompt `answer <claim-id> <yes|no|text>`. `cox review poll` detects it
(tag `answer`, or that message shape) and writes the cell **only** through `cox arena answer <id> <value> --by captain`.
Before writing it refuses a **stale** artifact: the sidecar's `synthesis_sha` must equal the current `synthesis.md`
content sha, so a captain never answers against a shifted table. A refusal reports the reason on an urgent
`review_decision` wake and writes nothing. Chat still works: the leader can run `cox arena answer` directly.

## Known loss window

`lavish-axi poll` acks by **delivery** and clears its queue (`session-store.js` `session.prompts = []`); there is no
separate ack, so "append before ack" is impossible. cox writes the record synchronously in the poll process; if the
write fails, cox reports the loss with the raw feedback (it cannot be re-polled) rather than losing it silently. An
upstream request for feedback ids with explicit ack is the fix.

## Sharing (outward-facing)

`cox review share <artifact> --epic <dir>` publishes to ht-ml.app, a third-party host (public by default). It is
**refused** unless policy `review.share` is true or `--share` is passed, and prints an outward-facing warning either
way. cox never shares on its own.

## Policy

```json
"review": { "surface": "lavish", "binary": "lavish-axi", "npx": null, "share": false, "why": "...", "review_when": "..." }
```

`binary` overrides the PATH lookup; `npx` is the opt-in fallback (exact version + integrity, same rule as quota);
`share` defaults false. The section is optional - an epic without it uses the code defaults and lavish stays off.
