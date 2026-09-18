---
name: cox-visualize
description: Turn an epic's design, plan, arena synthesis, or plan-vs-architecture into a visual review page and collect the captain's feedback through lavish-axi. Use when the captain says "review this visually", "open the design in lavish", "let me annotate the plan", or before signing a design or answering arena claims.
---

# cox-visualize

Visual review of cox artifacts (M13, ADR 0008/0013). cox owns the artifact and the record; `lavish-axi` is an optional
review surface behind an adapter, never forked. The one authoritative write for a synthesis cell stays
`cox arena answer`. Full flow and limits: `docs/review.md`; adapter card: `docs/adapters/lavish.md`.

## 1. Generate the artifact (prefer a generator)

Reach for a cox generator first - it renders a self-contained page (shared board CSS, no CDN) plus a
`coxswain.artifact.v1` sidecar under `<epic>/reports/visual`, so nothing drifts and the sidecar names the sources:

```
cox epic design --html --epic <epic>                 # DESIGN.md + decisions in force + latest synthesis
cox plan --html <plan-dir> --epic <epic>             # plan.md + phase files with status
cox arena synth --html --round <n> --epic <epic>     # claims by role; data-claim-id answer inputs; synthesis_sha
cox plan compare --html <plan-dir> <arch.html> --epic <epic>   # architecture sections vs plan phases, missing marked
```

`cox artifact list --epic <epic>` shows what exists.

## 2. When to hand-author instead

When a generator is too plain for the review (a rich diagram, a side-by-side comparison, slides), hand-author the HTML
per a lavish playbook - **open the matching playbook first**: `lavish-axi playbook diagram|comparison|table|plan|input`.
Follow lavish's design source order (`lavish-axi design`): (1) a look the captain named; else (2) the subject project's
own design system (Tailwind/theme config, shared CSS variables, component library); else (3) the Lavish Tailwind v4 +
DaisyUI CDN. **Always write a sidecar** for a hand-authored page with `generator: leader` (so `cox review` and
`cox artifact list` see it and the sources are recorded).

## 3. Open and poll

```
cox review open <artifact> --epic <epic>
cox review poll <artifact> --epic <epic> --max 25m
cox review reply "<message>" <artifact> --epic <epic> --max 25m
```

`open` runs `lavish-axi <file>` (prints the path and exits 0 when lavish is absent - review then falls back to chat).
When available it also prints the loopback session URL. Feedback reaches cox only after the captain selects **Send to
Agent** in the page.
`poll` runs one **bounded** poll (a separate subprocess, never inside the watcher). Each feedback item becomes an
`inbox.v1` fyi record for `_leader` plus a wake: `review_feedback` (routine) or `review_decision` (urgent) for a
decision/answer. `reply` mirrors the leader's response into the Lavish conversation and waits for the next delivery with
the same record, wake, and exit behavior. Exit codes: 0 feedback, 3 timeout, 4 ended, 5 browser disconnected. Final
feedback from **Send & End** is recorded and returns 4.

When feedback asks a question about the artifact's content, answer it in the agent chat first, then mirror the answer
into the page so the review stays coherent in both places:

```
cox review reply "The reviewer checks evidence; the adversary tries to break the proposal." \
  <artifact> --epic <epic> --max 25m
```

Repeat `reply` after each response until exit 4. Re-run after a timeout (3), or after the captain reopens a resumable
browser disconnect (5).

## 4. Answering arena claims

The captain answers a claim by annotating it or sending `answer <claim-id> <yes|no|text>` (the `input` playbook). cox
writes the cell only through `cox arena answer --by captain`, and **refuses a stale artifact** (the sidecar's
synthesis sha must match the current synthesis.md); a refusal is an urgent `review_decision` wake. Re-generate with
`cox arena synth --html` after any re-synth, then answer again.

## What you never do

Never treat lavish feedback as authoritative - it is advisory commentary; the synthesis cell is written only by
`cox arena answer`. Never run the poll inside the watcher. Never run `cox review share` (ht-ml.app is outward-facing)
without explicit captain direction. Never hand-author without a sidecar.
