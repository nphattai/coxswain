# doctor-shape-c - design

Status: active (captain 2026-09-27: "merged, do remaining todo for me"; first epic of the Shape C dogfood, opened by the leader on the first `cox doctor` finding after PR #55 landed)

`cox doctor` gives a false `PolicyInRepo` WARN for an in-repo (Shape C) workspace in two ways, both found the minute
coxswain started dogfooding itself. This epic fixes the two checks and locks each with a test. No product behaviour
beyond `cox doctor` changes.

| Alias | Repo | Does |
|---|---|---|
| coxswain | /Users/tainguyen/Work/repo/nphattai/coxswain | the `cox` CLI; `internal/doctor` holds the checks |

Branch: `epic/doctor-shape-c` (on origin). `cox epic new` created the worktree and the alias symlink.
Local env: none (CLI only; `epic.env` carries `EPIC` and `PROJECT`).

## Phase 0 - scout (D25)

Skipped: the leader located the code while reproducing the finding (single repo, two functions).

- `internal/doctor/env.go:84` `Roots(extra)` appends the `--root` flags as typed; nothing makes them absolute.
- `internal/doctor/env.go:340-346` the `PolicyInRepo` loop exempts a repo whose path `samePath`s the workspace root.
- `internal/doctor/env.go:355` `samePath` uses `EvalSymlinks`, which leaves `.` relative, so `.` never equals the
  absolute repo path from `cox/workspace.json`.
- `cmd/cox/doctor.go:150-164` parses `--root` and calls `doctor.Roots`.

## Findings (reproduced 2026-09-27 on this checkout, cox v2.0.0-8-g28f4376)

1. **Relative `--root`.** `cox doctor --root .` in the Shape C checkout prints the workspace twice (`workspace .` and
   `workspace /Users/.../coxswain` from the registry) and WARNs `repo "coxswain" checkout carries cox/policy.json` on
   the `.` entry. `cox doctor --root "$PWD"` prints it once with no WARN. Cause: the root is compared as typed.
2. **A registered repo that is itself a workspace.** The henrylab workspace registers coxswain as a repo. coxswain's
   `cox/policy.json` is its own workspace policy (it carries `cox/workspace.json` too), not a drifted copy, yet henrylab's
   scan WARNs on it. The exemption only knows "same path as my root".

## Contract

- `doctor.Roots` returns absolute, cleaned paths (relative entries resolved against the working directory); a root that
  cannot be made absolute is kept as typed. Duplicates are removed after normalisation, so `.` and its absolute form
  collapse to one workspace.
- The `PolicyInRepo` check skips a repo checkout when it is the workspace root (existing) **or** when the checkout carries
  `cox/workspace.json` (it is a workspace of its own). The WARN text can say so: "(a checkout that is itself a workspace is
  exempt)" or keep the current wording; the worker decides and notes it in the PR body.
- No change to `cox/workspace.json`, the policy schema, or any other doctor check.

## Stories

| Story | Repo | Wave | depends |
|---|---|---|---|
| `doctor-shape-c-coxswain` | coxswain | 1 | - |

Single story, single repo. Arena: not triggered (no policy trigger applies).
