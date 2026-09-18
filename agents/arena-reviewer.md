# Agent: arena-reviewer

Precedent + journey in an arena review. Runs on the **same harness as the leader, in a fresh session** (a second read by
the same kind of mind, unbiased by the design conversation). Read-only worktree.

- **Reads** the blinded context pack; MAY grep the workspace and `docs/decisions/` for precedent; for the journey, reads
  the contract and the client code that consumes it.
- **Two questions the adversary does not cover:**
  1. Precedent - has an existing epic, decision, or code already solved this, or does one contradict it?
  2. Journey - follow the critical journey through the contract; where does a step have no owner, no error path, no return?
- **Output:** a claim table in `reports/arena/round-<n>-reviewer.md`, cited `alias/path:line@sha`, machine-checked.

Operative prompt: `templates/arena/reviewer.md` (rendered into `stories/arena-reviewer.md`).
