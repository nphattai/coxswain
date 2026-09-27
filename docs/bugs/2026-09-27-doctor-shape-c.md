---
title: cox doctor stops false PolicyInRepo warnings on an in-repo (Shape C) workspace
slug: doctor-shape-c
type: fix
status: done
created: 2026-09-27
updated: 2026-09-27
related:
  - docs/getting-started/workspace.md
---

# cox doctor stops false PolicyInRepo warnings on an in-repo (Shape C) workspace

## Context & Problem

`cox doctor` warns when a registered repo checkout carries its own `cox/policy.json`, since that usually means a policy
copy drifted out of the workspace. The first time coxswain ran cox on itself as a Shape C workspace (the repo checkout
is the workspace), doctor warned about the workspace's own policy in two ways:

1. **Relative `--root`.** `cox doctor --root .` listed the workspace twice, once as `.` and once by the absolute path
   from `cox/workspace.json`, and warned on the `.` entry. `doctor.Roots` kept `--root` entries as typed, and the
   `samePath` exemption resolves symlinks but never makes a path absolute, so `.` never matched the registered path.
2. **A registered repo that is itself a workspace.** A second workspace registering coxswain as a repo warned about
   coxswain's `cox/policy.json`, which is coxswain's own workspace policy, not a drifted copy. The exemption only knew
   "same path as my root".

## Solution Overview

`doctor.Roots` makes every entry absolute before removing duplicates, and the `PolicyInRepo` check skips a checkout that
carries `cox/workspace.json`, since that checkout is a workspace of its own.

## Key Decisions

- Normalise in `Roots`, where both `--root` and `$COX_ROOTS` pass, instead of patching the one comparison.
- Detect "is a workspace" by `cox/workspace.json`, the same marker every other command uses, rather than a new flag.

## Scope & Non-Goals

No change to `cox/workspace.json`, the policy schema, or any other doctor check.

## Acceptance

- `cox doctor --root .` in a Shape C checkout lists the workspace once with no `PolicyInRepo` warning.
- `TestRootsNormalisesRelative` and `TestInspectWorkspaceInRepoPolicyNotFlagged` (with a nested-workspace repo) pass.

## Contracts

None.

## As built

2026-09-27. PR #56 (commits 191aa7d, 49d3931) into `epic/doctor-shape-c`; PR #57 shipped it to `main`. Verified after
install: `cox doctor --root .` prints the workspace once and no warning.

## Open Questions

None.
