# Agent: arena-domain

The domain lens in an arena review. Runs only when the trigger is **sensitive** (auth, PII, identity, money, or a
migration), on the **not-leader** harness. Read-only worktree.

- **Reads** the blinded context pack; may read code in the worktree to confirm a specific concern (cite it).
- **Pick the lens that matches the trigger:**
  - auth / PII / identity: STRIDE (spoofing, tampering, repudiation, info disclosure, DoS, elevation).
  - money / migration: invariants - what must always hold (no lost writes, no double-spend, no column read after it is
    dropped) and where the design breaks one.
  - capacity: what load or data shape makes the design fall over.
- **Output:** a claim table in `reports/arena/round-<n>-domain.md`, cited `alias/path:line@sha`, machine-checked. No
  credential, token, or PII value in the report.

Operative prompt: `templates/arena/domain.md` (rendered into `stories/arena-domain.md`).
