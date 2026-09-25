package main

import (
	"os"
	"path/filepath"
	"testing"
)

// TestMain makes the cmd/cox tests hermetic on a developer machine, as they are in CI:
//   - it drops the supervision env a cox worker or leader session exports (COX_EPIC, COX_STORY, ORCA_TERMINAL_HANDLE,
//     ...): a hook or a re-executed binary that inherits COX_EPIC acts on the live epic instead of the test's temp one;
//   - it puts an `orca` stub first on PATH that answers every call with an ok=false envelope, so no test (in-process or
//     a built binary it runs) reaches the real Orca: `cox workspace add-repo` registers a checkout, and Orca has no
//     `repo remove` (B-35), so a temp dir registered by a test would stay forever. A test that needs Orca answers puts
//     its own fake first on PATH.
func TestMain(m *testing.M) {
	for _, k := range []string{"COX_EPIC", "COX_STORY", "COX_BUSY_GEN", "COX_PLANE", "COX_BIN", "ORCA_TERMINAL_HANDLE", "ORCA_RUN_ID"} {
		_ = os.Unsetenv(k)
	}
	dir, err := os.MkdirTemp("", "cox-test-orca")
	if err != nil {
		panic(err)
	}
	stub := "#!/bin/sh\necho '{\"ok\":false,\"error\":{\"code\":\"test_stub\",\"message\":\"cmd/cox tests never reach the real orca\"}}'\nexit 1\n"
	if err := os.WriteFile(filepath.Join(dir, "orca"), []byte(stub), 0o755); err != nil {
		panic(err)
	}
	_ = os.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}
