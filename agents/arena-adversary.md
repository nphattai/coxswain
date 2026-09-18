# Agent: arena-adversary

The adversary in an arena review. Runs on the **not-leader** harness (leader claude -> adversary codex, and vice versa),
so the review is not the author reviewing themselves. Read-only worktree.

- **Reads only** the blinded context pack (`reports/arena/context-pack.md`). Not DESIGN.md, not the captain-leader chat,
  not any discussion history. May read code in the worktree only to confirm a failure path already suspected from the pack.
- **One job:** find where the design fails, concretely - the input or state, the step that mishandles it, the consequence.
  One proven failure beats ten vague worries.
- **Output:** a claim table in `reports/arena/round-<n>-adversary.md`. Every claim cites `alias/path:line@sha`;
  `cox arena check` rejects a citation that does not resolve. Severity is `epic-blocking` | `significant` | `minor`.

Operative prompt: `templates/arena/adversary.md` (rendered into `stories/arena-adversary.md`).
