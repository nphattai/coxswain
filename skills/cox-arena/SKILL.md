---
name: cox-arena
description: Run an adversarial design review (arena v3) before signing an epic's DESIGN.md - trigger, blinded context pack, headless read-only roles, evidence tiers, machine-checked and verified claims, synthesis with gates, and the signature. Use when the captain says "arena <slug>", "review this design", or before signing a hard epic's design.
---

# cox-arena

Adversarial review for a hard epic's design (arena v3, ADR 0013). Roles read a blinded pack, raise cited claims with an
evidence tier and a runnable check, and the leader adjudicates each into a synthesis before the captain signs. Citations
are checked and claims are verified by machine, not trusted by eye (G10). Full flow and formats: `docs/arena.md`.

## 1. Trigger and run
```
cox epic arena --epic <epic> [--lite] [--reason "<why>"] [--round <n>] [--terminal]
```
This decides the level from policy and the design, builds `reports/arena/context-pack.md` (blinded, every citation
scout-checked at HEAD), resolves each role's harness, and **runs each role headless by default**: a read-only subprocess
in the leader checkout (claude `-p --permission-mode plan --output-format json`, codex `exec --json -s read-only`). cox
extracts the report from a fenced ```report block in the JSON and writes `reports/arena/round-<n>-<role>.md`.

- **full** (3+ repos, a sensitive keyword - migration/money/auth/identity/pii - in DESIGN.md, or a `--reason`): adversary
  + reviewer, plus a domain role for a sensitive trigger.
- **lite**: one adversary. `--lite` forces this even when the trigger is full.
- **none**: nothing to review.

Harness is never hard-coded: the adversary is **not-leader**, the reviewer is **same-as-leader, new session** (from
`cox/policy.json harness.arena`). `--terminal` runs each role on its own read-only worktree+terminal instead; cox switches
to terminal automatically when the leader checkout is dirty or a role template declares `needs_worktree`.

## 2. Check every report
```
cox arena check --epic <epic> --round <n>
```
Exit 1 lists every problem by line: a citation that does not resolve at its pinned sha, a tier outside 1-5, a confidence
outside 0-100, a tier-5 claim marked epic-blocking, or an epic-blocking/significant claim with no check. Fix the report;
do not sign around it. A v2 report reads with a "v2 report" warning.

## 3. Verify the claims
```
cox arena verify --epic <epic> --round <n>
```
Runs each claim's `check` in a temporary worktree detached at the cited sha (allowlist: go test/vet, git show/log/grep/
diff, grep, rg, cox read-only; no network; 5-minute timeout) and writes `reports/arena/verify-round-<n>.json` with
pass/fail/unknown. Never runs in the leader checkout.

## 4. Synthesize and adjudicate
```
cox arena synth --epic <epic> --round <n>
```
Writes `reports/arena/synthesis.md`, auto-filling each claim's `tier` and `verified`. Fill the six sections (adopted
decision, decisive evidence, rejected alternatives, preserved locked decisions, remaining uncertainty, verification
gates) and every verdict: `accepted` | `rejected` | `unresolved` | `captain_decision`. A rejected claim stays with its
reason. For a `captain_decision`, the captain answers by filling `captain agrees`.

Optional visual input (M13): `cox arena synth --html --round <n>` renders the claims under `reports/visual/` with a
`data-claim-id` input on each captain_decision. `cox review open`/`poll` it; when the captain sends
`answer <claim-id> <yes|no|text>`, cox writes the cell through `cox arena answer` after a sidecar sha check (a stale
artifact is refused). This is an alternative input, not a second write path - `cox arena answer` is the only writer,
from chat or artifact. See `cox-visualize`.

## 5. Round 2 - only when needed
A second round runs ONLY when an `epic-blocking` claim is left `unresolved`, or two roles conflict on a fact. Re-run with
`--round 2` (max 3; `--round 4` is refused). The round-2 pack carries the prior round's verified claims and provisional
verdicts as opposition.

## 6. Sign
Update DESIGN.md for every accepted claim, then:
```
cox epic design --sign --epic <epic>
```
Refuses a v3 synthesis with an empty section, an accepted epic-blocking claim not verified pass, an open
captain_decision, a round past 3, or an unresolved epic-blocking claim. Records `design_signed` with the DESIGN.md and
synthesis shas. A later DESIGN.md change: `cox epic design --amend --reason "<what and why>" --epic <epic>`.

## What you never do
Never sign over an unchecked or unverified report, a blank verdict, or an empty section. Never delete a rejected claim.
Never hard-code a role's harness - it comes from policy.
