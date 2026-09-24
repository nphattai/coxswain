package main

import (
	"os"
	"testing"
)

// TestMain stops the leader session-start seam from fork-bombing the test run: bearingsSessionStart detaches
// `<os.Executable()> bearings deferred ...`, and under `go test` that executable is cox.test, which would re-run the
// whole suite (spawning more children) as an orphan. A test binary started that way exits at once.
func TestMain(m *testing.M) {
	if len(os.Args) > 2 && os.Args[1] == "bearings" && os.Args[2] == "deferred" {
		os.Exit(0)
	}
	os.Exit(m.Run())
}
