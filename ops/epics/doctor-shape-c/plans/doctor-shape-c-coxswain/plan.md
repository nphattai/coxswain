# doctor-shape-c-coxswain - plan (against be67ec8)

## Phase 1/2 - `doctor.Roots` normalisation (AC 1, 2)
- `internal/doctor/env.go` `Roots`: each non-empty entry (defaults, `$COX_ROOTS`, `--root`) goes through
  `filepath.Abs` (which also Cleans); on error keep it as typed. De-dup after normalisation.
- Test: `TestRootsNormalisesAndDedupes` in `internal/doctor/env_test.go` - `COX_ROOTS` set to a relative dir via
  `t.Setenv`, extra = [".", "$PWD", "./"]: result contains cwd exactly once, the COX_ROOTS entry absolute.

## Phase 2/2 - `PolicyInRepo` exemption for a checkout that is itself a workspace (AC 3)
- `InspectWorkspace` loop: skip when `samePath(r.Path, wsRoot)` OR `exists(r.Path/cox/workspace.json)`.
  WARN wording in cmd/cox/doctor.go unchanged (noted in PR body).
- Test: extend `TestInspectWorkspaceInRepoPolicyNotFlagged` with repo "nested" carrying both
  `cox/workspace.json` and `cox/policy.json` -> still `PolicyInRepo == [other]`.
- Docs: `docs/getting-started/workspace.md` doctor section gets one sentence only if Shape C text needs it.

## Gates
`go test ./...`, `go vet ./...`, `make` CI targets; before/after `cox doctor --root .` in the Shape C root in PR body.
