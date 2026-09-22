# 0017 - Delivery mode and merge authority are explicit and enforced in code

- Status: Accepted (captain, 2026-09-21)
- Date: 2026-09-22

## Context

The firstmate deep-dive (`research/reports/coxswain-vs-firstmate.md` §2.3 rows "Delivery mode", "Merge posture", "Merge
guard", "Landed-work test"; BACKLOG B-05) names the delivery and merge discipline coxswain left to a leader's memory:

- **A project mode a worker reads before it acts.** Firstmate's `bin/fm-project-mode.sh` resolves a project mode
  (no-mistakes / direct-PR / local-only) that the brief carries, so a worker knows whether to open a PR and wait, push and
  PR, or leave a clean local branch. Coxswain had only `delivery.style` (the phase/push rhythm), not a merge posture.
- **One merge command that enforces green-at-the-live-head.** Firstmate's `bin/fm-pr-merge.sh` merges only a green PR at
  the live head, pins the head with `--match-head-commit`, reads the PR back after the merge, and treats `--allow-red` as
  attended-only. The captain ruling (DESIGN "Captain rulings") is that the captain merges everything and a leader or
  worker never merges - but coxswain enforced this only by convention, so nothing stopped a worker from merging or a merge
  from landing a moved head.
- **A landed-work test for the merge sha.** `cox story done --merge <sha>` recorded any sha as evidence with no check
  that it was actually on the branch it claimed to land on.

## Decision

The delivery contract is explicit in policy and story frontmatter, and merge authority is a command that fails closed.

1. **`delivery.mode` and a justified `merge` section.** Policy gains `delivery.mode` (`no-mistakes|direct-PR|local-only`,
   default `direct-PR` to match today's behaviour; an unrecognised value fails the load) alongside the kept
   `delivery.style`, and a new justified section `merge` (`{"yolo": false, why, review_when}`). `merge.yolo` defaults
   false: a non-captain terminal may not merge.
2. **The mode is resolved once and printed in the brief.** `cox epic stories` stamps the resolved `mode` into the story
   frontmatter (overridable per story). The brief renderer prints one line, `Delivery contract: mode=<mode> yolo=<on|off>`,
   followed by a per-mode paragraph (no-mistakes: full gates + PR + wait for merge authority; direct-PR: push + PR, no
   extra pipeline; local-only: clean ready branch, no push, wait).
3. **`cox story done --merge <sha>` verifies containment.** For `no-mistakes|direct-PR` the sha must be contained in
   `origin/epic/<slug>` (a fetch first; an unknown fetch refuses); for `local-only` it must be contained in the repo's
   production branch. A refusal names the exact `git merge-base --is-ancestor` line it ran.
4. **`cox ship merge` is the single merge command.** Through the forge seam (never a shell-out to `gh` from `cmd/cox`) it
   reads the PR live and merges only an open, non-draft, mergeable PR whose base is the epic or production branch and whose
   every check is green at the live head (a named check is waived only with `--allow-red`). It passes the head sha to the
   forge pinned (GitHub's merge `sha`, the equivalent of `--match-head-commit`), reads the PR back, and accepts only a
   confirmed `merged`, then appends a `merged` event to `ledger.jsonl` (`evidence: {pr, head, method, by}`). `--check` does
   everything but the merge and the ledger write. It is ALWAYS refused from a worker terminal (`COX_STORY` set) and, while
   `merge.yolo` is false, refused unless `--captain`. Exit codes: `0` merged, `1` refused (every failing condition listed,
   not only the first), `3` unknown (a forge read failed or CI is pending).
5. **The forge seam carries the merge.** `forge.Forge` gains `Merge(pr, method)` and `forge.PR` gains `Draft`/`Mergeable`;
   the github adapter maps `gh pr view --json isDraft,mergeable` and `gh pr merge --match-head-commit`, and the fake forge
   replays a head-moved reject and a read-back so the whole decision is unit-tested without gh.

## Consequences

- The captain merges everything, and the rule is now in code: a worker terminal is refused outright, and a leader is
  refused unless `merge.yolo` is flipped, so the green-at-live-head rule is enforced rather than remembered (DESIGN AC 4).
- A push between the read and the merge can no longer slip a different head in: the pinned sha makes the forge reject it,
  and the read-back accepts only a confirmed merge, so a stale verdict never merges (F12, P5).
- `delivery.mode` and `merge` are additive: an epic policy that predates them resolves `direct-PR` and `yolo: false`, so
  no existing policy is invalidated.
- `cox ship merge` never merges a real PR in a test: the fake forge covers red/pending/head-moved/green/authority, and the
  onboarding E2E and CLI docs reference `--check` only, so the suite proves the behaviour without a live merge.

## References

- `research/reports/coxswain-vs-firstmate.md` §2.3 (rows "Task shapes", "Delivery mode", "Merge posture", "Merge guard",
  "Landed-work test").
- firstmate `bin/fm-pr-merge.sh:1-40` (green at the live head, `--match-head-commit`, read-back, `--allow-red`
  attended-only), `bin/fm-project-mode.sh:1-35` (modes), `AGENTS.md:304-307`, `:346-365` (selected delivery path).
- BACKLOG row B-05; DESIGN "Captain rulings" (the captain merges everything).
