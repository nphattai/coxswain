package integration

import (
	"os/exec"
	"strings"
	"testing"
)

// The core packages must not import a concrete backend (Orca or herdr): they depend only on the backend interface
// (decision 0002, P6). Verified with `go list -deps`.
func TestCoreDoesNotImportConcreteBackends(t *testing.T) {
	const mod = "github.com/nphattai/coxswain"
	forbidden := []string{
		mod + "/internal/adapter/backend/orca",
		mod + "/internal/adapter/backend/herdr",
	}
	for _, pkg := range []string{"state", "identity", "worktree", "doctor"} {
		out, err := exec.Command("go", "list", "-deps", mod+"/internal/"+pkg).Output()
		if err != nil {
			t.Fatalf("go list -deps %s: %v", pkg, err)
		}
		deps := string(out)
		for _, bad := range forbidden {
			if strings.Contains(deps, bad) {
				t.Errorf("internal/%s imports %s (core must not import a concrete backend)", pkg, bad)
			}
		}
	}
}
