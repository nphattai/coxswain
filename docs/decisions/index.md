# Architecture decisions

The decisions behind coxswain, as ADRs.

- [0001 - Core language is Go, one static binary](0001-go-single-binary.md)
- [0002 - Backend: Orca default, herdr optional, one interface](0002-orca-default-herdr-optional.md)
- [0003 - One worker per repo is a policy default, not an invariant](0003-one-worker-per-repo-policy.md)
- [0004 - Arena adversary runs a different harness than the leader](0004-arena-adversary-different-harness.md)
- [0005 - Defer baseline measurement to M6; it does not block delivery](0005-defer-baseline-to-m6.md)
- [0006 - Patch four safety bugs in v1, then freeze it](0006-patch-and-freeze-v1.md)
- [0007 - Name the project coxswain, CLI cox](0007-name-coxswain-cli-cox.md)
- [0008 - Model-agnostic: leader and worker pick a harness from policy](0008-model-agnostic-harness.md)
- [0009 - Gate the removal of v1 on the downstream workspace migrating to v2](0009-v1-removal-gate.md)
- [0010 - Measure before extend: what opens routing, board, lab, and tmux](0010-measure-before-extend.md)
- [0011 - Captain ruling opens routing, board, and lab; tmux backend is dropped](0011-captain-opens-routing-board-lab-drops-tmux.md)
- [0012 - Handoff belongs to cox; backends only provide worktrees and terminals](0012-handoff-belongs-to-cox-backends-provide-terminals.md)
- [0013 - Arena v3: evidence tiers, verified claims, adversarial round 2, read-only roles](0013-arena-v3-evidence-tiers-verify-rounds.md)
- [0014 - The leader turn boundary is guarded; no turn ends blind](0014-turn-boundary-guarded.md)
- [0015 - Close and attach are fail-closed](0015-close-attach-fail-closed.md)
- [0016 - Busy state is harness-owned, with a source trust table and versioned records](0016-busy-state-harness-owned.md)
- [0017 - Delivery mode and merge authority are explicit and enforced in code](0017-delivery-mode-and-merge-authority.md)
