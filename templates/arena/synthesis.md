---
epic: {{.Slug}}
schema: coxswain.arena.v3
---
# Arena synthesis - {{.Slug}}

The leader writes the six sections below and fills every `verdict`/`reason`/`DESIGN.md change` before signing; `tier`
and `verified` are filled automatically by `cox arena synth`. A rejected claim stays in the table with its reason;
nothing is deleted. The captain fills `captain agrees` when reviewing, so after several arenas it is visible which roles
earn their cost (calibration, P7).

Each section below must be filled (replace the `<...>` line). `cox epic design --sign` refuses while any is empty.

## Adopted decision
<the decision the design now carries, in one paragraph>

## Decisive evidence
<the evidence that settled it, by tier and citation>

## Rejected alternatives
<each alternative considered and why it lost>

## Preserved locked decisions
<the locked decisions (ADR ids) this design keeps intact>

## Remaining uncertainty
<what is still unknown and how it will be watched>

## Verification gates
<the checks that must pass before this ships (link the verify results)>

## Claims

Verdict is one of: `accepted` | `rejected` | `unresolved` | `captain_decision`. Every row needs a non-empty verdict
before signing. For `captain_decision`, fill the `question` cell with what only the captain can decide; sign refuses
while a `captain_decision` has no `captain agrees`. An accepted `epic-blocking` claim must have `verified: pass`.

The `id` is the stable answer key: `cox arena answer <id> <yes|no|text> --by <name>` fills that row's `captain agrees`
cell (a captain_decision's answer goes there too). Do not renumber ids by hand.

| id | role | claim | evidence | tier | severity | verified | verdict | question | reason | DESIGN.md change | captain agrees |
|---|---|---|---|---|---|---|---|---|---|---|---|
{{range .Claims}}| {{.ID}} | {{.Role}} | {{.Claim}} | {{.Evidence}} | {{.Tier}} | {{.Severity}} | {{.Verified}} | | | | | |
{{end}}
## Round 2 needed?

A second round runs ONLY when one of these holds (never because roles agreed):
- an `epic-blocking` claim is left `unresolved`, or
- two roles conflict on the same fact.

Round-2 decision: <the leader writes yes/no and why here>
