# 0018 - Story kind ship|scout, with a report gate and promotion

- Status: Accepted (captain, 2026-09-21)
- Date: 2026-09-22

## Context

The firstmate deep-dive (`research/reports/coxswain-vs-firstmate.md` §2.3 row "Task shapes"; BACKLOG B-05) names a task
shape coxswain could not express: a **scout** - an exploration whose deliverable is a written report, not a pull request.
Coxswain modelled every story as a PR-bearing ship story, so:

- A scout still had to open a PR to be "done", or the leader hand-waved completion.
- `cox audit pr` and `cox state` probed the forge for every story, so a scout with no PR produced a `gh pr view` error
  the leader had to read past (B-05: the no-PR error is noise, not a fact).
- There was no first-class way to turn a scout's findings into deliverable work while a worker was still warm.

## Decision

A story carries a `kind` (`ship` | `scout`, default `ship`), and the tooling treats a scout as a report, not a PR.

1. **`kind` in frontmatter, rendered from the spec.** `cox epic stories --story id=repo:scout` renders a scout story
   (default kind is `ship`). The scout brief carries a "report only, no PR: your deliverable is
   `<epic>/reports/<id>.md`" notice, so a scout worker never opens a PR.
2. **`cox story done` gates a scout on its report.** Completing a scout is refused unless `<epic>/reports/<id>.md`
   exists, and a scout never requires `--merge` (the report is the evidence, recorded as `evidence.report`).
3. **`cox audit pr` and `cox state` skip the forge for a scout.** Both short-circuit to `kind=scout
   report=<path|missing>` without calling the forge, so a scout never triggers a `gh pr view` error (B-05) and its state
   line reports whether the report has landed.
4. **`cox story promote <id> --epic <dir> --mode <m>` turns a scout into a ship story.** It flips `kind` to `ship` in the
   frontmatter, sets the delivery `mode`, and appends a "Superseding contract (promoted <date>)" section carrying the
   delivery contract line (item 8) to the story file. It prints the `cox steer` command that would deliver the change to a
   running worker, but never sends the steer itself - the leader decides when to interrupt.

## Consequences

- A scout is a first-class, cheap task: it reports and is done, with no PR ceremony and no forge noise, and the leader
  sees `kind=scout report=<path|missing>` at a glance in `cox state` and `cox audit pr` (DESIGN item 9, B-05).
- `kind` is additive: a story with no `kind` reads as `ship`, so every existing story keeps its behaviour.
- Promotion keeps the story file the single source of truth: the superseding contract is appended in place with the
  delivery line, and delivery to a live worker stays an explicit leader steer rather than an automatic interrupt
  (consistent with "steer, never type into a worker").

## References

- `research/reports/coxswain-vs-firstmate.md` §2.3 (row "Task shapes"); BACKLOG row B-05.
- firstmate `bin/fm-promote.sh:1-11` (scout -> ship), `AGENTS.md:304-307`, `:346-365` (ship vs scout, selected delivery
  path).
- ADR 0017 (the delivery contract the superseding section carries).
