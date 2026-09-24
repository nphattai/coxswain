# Status lines and open decisions

A worker's status history is an append-only event log. Reading it last-event-wins cannot represent "an earlier
decision is still open after a later, unrelated event", so cox folds the whole history into the set of decisions still
open. The grammar and the fold are ported verbatim from firstmate's `bin/fm-classify-lib.sh` (pinned `1e0e773`,
cox-supervision-port wave 2). Executable owner: `internal/protocol/decision` (pure, no I/O) and its tests; the drain
rendering is `internal/wake/present.go`.

## Status-line grammar

```text
<verb> [name=value]...: <note>
needs-decision [key=api-shape]: pick REST or RPC
needs-decision: [key=api-shape] pick REST or RPC      # note-head key, equivalent position
blocked corr=c44897ee2db4326b [key=creds]: waiting on the deploy token
resolved [key=api-shape] [at=1788576000]: answered: use REST
```

- The verb is the text before the first colon, ended at the first bracket tag. Tags come in any order.
- `[key=<slug>]` names the decision. The slug is `A-Za-z0-9._-`. A malformed slug is rejected: the line moves no
  decision and is never rewritten to `default`. A line with no key token uses `default`. A `[key=...]` deeper in the
  note is prose.
- `corr=<16 hex>` after the verb, and `[corr=...]`, are correlation metadata. A token-first line keeps its token, so the
  word after it cannot impersonate a verb.
- `[at=<epoch>]` is the optional emission time: unsigned decimal, at most 12 digits, one tag. Anything else is unknown
  time. A time tag never moves the head/note separator and never decides state.

## The fold

- `needs-decision` and `blocked` open their key. `resolved` and `captain-held` close it.
- A transition needs a head/note colon. A colonless line needs a complete `[key=]` token. Continuation prose moves
  nothing.
- A `done:` or `failed:` line on a `ship` or `scout` story supersedes every open decision. For any other kind it
  supersedes none.
- Reserved keys (`pending-reply-`) move only when the line's note uses that namespace's own vocabulary.
- `ClosingVerb` reports the verb that last moved a key: the opening verb while the key is open, then `resolved`,
  `captain-held`, or the ship/scout terminal once it closes.

## Cox status history

Each worker status event in `.cox/events.jsonl` (written by `status.Report`) becomes one line, in log order:

| Event | Line |
|---|---|
| `status` / any phase | the note itself |
| `done` | `done: <note>` unless the note already declares `done:` |
| `stuck` | `blocked: <note>` unless the note already declares `blocked:`, `needs-decision:` or `failed:` |
| `question` (`asked qNNN`) | `needs-decision [key=qNNN]: <question body>`, followed by `resolved [key=qNNN]: answered` once an answer exists |

The story kind comes from the `kind:` frontmatter of `stories/<story>.md`. An unset kind reads as `ship` (ADR 0018). A
story with no story file is `unknown`.

## Classification (`wake.Classify`)

A status message's subject is a status line; each body line counts only when it is itself a recognized event.

| Line | Wake kind |
|---|---|
| `needs-decision` / `blocked` (declared) | `input_required` |
| `done:` in the subject | `worker_done` |
| `failed:` | `stuck` |
| legacy verbless `PR ready`, `checks green`, `ready in branch`, `merged` | `pr_ready` |
| `working`, `resolved`, `captain-held`, `paused`, `note`, prose | `status` |

## Drain sections

`cox wake drain` prints the unacked wakes, then two sections on every drain, including an empty queue:

- `STATUS OUTCOME BACKSTOP (...)`: for each story, the newest captain-facing event when its wake was never handled.
  An event counts as handled when an acknowledgement named its row while the row existed (`.cox/wake.handled`), or when
  the row is being shown in this drain. Parseable decisions are left to the fold. A receipt (`.cox/wake.backstop`,
  stored as a history position) is committed only after the output is written.
- `OPEN DECISIONS (...)`: every open decision across the epic, rendered `<story> [key=<k>] <verb>: <note>`, followed by
  the answering hint.

Each item is cut to 219 characters with ` [truncated]`. Each section's items share a 4000-byte budget, and anything
left out is reported as `N more omitted (byte cap)`.
