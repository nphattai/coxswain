package main

import (
	"os"
	"testing"
)

// TestMain drops the supervision env a cox worker or leader session exports (COX_EPIC, COX_STORY, ORCA_TERMINAL_HANDLE,
// ...), so `go test ./cmd/cox` run from inside a dispatched worker behaves as it does in CI: a hook or a re-executed
// binary that inherits COX_EPIC acts on the live epic instead of the test's temp one. A test that needs one sets it
// with t.Setenv.
func TestMain(m *testing.M) {
	for _, k := range []string{"COX_EPIC", "COX_STORY", "COX_BUSY_GEN", "COX_PLANE", "COX_BIN", "ORCA_TERMINAL_HANDLE", "ORCA_RUN_ID"} {
		_ = os.Unsetenv(k)
	}
	os.Exit(m.Run())
}
