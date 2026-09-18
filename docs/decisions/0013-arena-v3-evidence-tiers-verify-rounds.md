# 0013 - Arena v3: evidence tiers, verified claims, adversarial round 2, read-only roles

- Status: Accepted (captain, 2026-09-16)
- Date: 2026-09-16

## Context

Arena v2 (decision 0004, M4) matched the frame of the mentor's arena workflow (`plans/reports/260916-arena-vs-mentor/`):
a shared compact context pack, an independent round 1, a second round only on material disagreement, rejected claims
kept in the synthesis, no turn limits on specialists, the orchestrator owning synthesis and the captain owning business
decisions. Three live arenas (epic v2 rounds 1-2, M10, M11) each found one accepted epic-blocking claim.

The comparison also showed six gaps against the mentor's protocol: no evidence hierarchy, a four-column claim table
instead of the specialist's full return (assumptions, confidence, checks, conflicts), citation existence checked but
claims never verified by running anything, round 2 without the opposing claims, a synthesis without the adopted
decision and gates, and roles launched with worker permissions instead of read-only plan mode. The captain approved
closing them before the quota work (M11).

## Decision

1. **Evidence tiers.** Every evidence item carries a tier: 1 locked decision (ADR or captain ruling), 2 test or
   experiment result, 3 live code, log, or tool output, 4 official documentation, 5 reasoned inference. A claim whose
   best evidence is tier 5 cannot be `epic-blocking`. When claims conflict, the lower tier number wins unless the
   leader records why not.
2. **Specialist return.** A role report has, besides the claim table: recommendation, assumptions, confidence 0-100
   per claim, checks still required, and conflicts with locked decisions by id. A conflict with a locked decision is
   surfaced, never resolved by the role.
3. **Verified claims.** An `epic-blocking` or `significant` claim carries a `check` the leader can run (a command
   with expected outcome, or a file:line assertion). `cox arena verify` runs the checks and records pass, fail, or
   unknown in the synthesis. `cox epic design --sign` refuses an accepted epic-blocking claim whose check did not pass.
4. **Round 2 with opposition.** The round N+1 pack is the original pack plus the verified round N claim table and the
   leader's provisional verdicts. Round 2 roles answer: agree, reject, which evidence changes the conclusion, what stays
   unresolved. At most 3 rounds; `--round 4` is refused.
5. **Synthesis shape.** Above the claim table: adopted decision, decisive evidence, rejected alternatives with reasons,
   preserved locked decisions, remaining uncertainty, verification gates. Verdict set gains `captain_decision` for a
   question evidence cannot settle; sign refuses while one is unanswered.
6. **Headless by default, read-only always** (captain, 2026-09-16). An arena role runs as a headless subprocess in the
   leader's checkout, like the mentor's specialist call: claude `-p --permission-mode plan --output-format json
   --no-session-persistence`, codex `exec --json -s read-only`; cox builds the prompt from the pack and the role
   template, parses the JSON, writes the report, and records the event with session kind `headless`. The pack states
   the sha the role ran at. `--terminal` keeps the story-style path (own worktree at that sha, own terminal,
   `harness.launch.arena.<harness>` read-only flags); cox switches to it automatically when the leader checkout is
   dirty or the role declares it needs a worktree. Roles never get the worker's bypass flags. `cox arena verify`
   always runs checks in a temporary detached worktree at the cited sha, never in the leader checkout.
7. **Role to harness map** stays functional (adversary, reviewer, domain) with the not-leader rule; policy may name a
   default harness and model per role. A breadth-scout role backed by grok waits for a grok adapter.
8. The next full arena (adversary plus reviewer) runs on a real downstream epic to seed the calibration column.

## Consequences

- Arena output becomes checkable by a third party: tiers, checks, and verify results are in the files.
- Signing gets stricter: unverified epic-blocking claims and open captain decisions block it.
- Roles can no longer write outside their report by accident; the report path is the only write.
- Supersedes decision 0004 on role output shape and round mechanics; keeps its not-leader rule and blinded pack.

## Review when

After five arena v3 runs: check whether tier 5 claims were ever the decisive finding (then loosen), whether verify
checks were mostly `unknown` (then tighten the check grammar), and which roles the captain agreed with (G8).
