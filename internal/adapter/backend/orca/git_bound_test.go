package orca

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nphattai/coxswain/internal/boundexec"
)

// A stub git that sleeps past the bound: the orca git exec returns a timed-out error within the bound instead of hanging
// the caller (cox-refresh-recovery AC 3).
func TestGitExecIsBounded(t *testing.T) {
	defer func(r, a time.Duration) { boundexec.GitReadBound, boundexec.GitActBound = r, a }(boundexec.GitReadBound, boundexec.GitActBound)
	boundexec.GitReadBound, boundexec.GitActBound = 300*time.Millisecond, 300*time.Millisecond
	bin := t.TempDir()
	stub := filepath.Join(bin, "git")
	if err := os.WriteFile(stub, []byte("#!/bin/sh\n[ \"$1\" = warm ] && exit 0\nsleep 5\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := exec.Command(stub, "warm").Run(); err != nil { // macOS scans a fresh script on its first exec
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	start := time.Now()
	_, err := execGit("-C", t.TempDir(), "rev-parse", "HEAD")
	if el := time.Since(start); el > 2*time.Second {
		t.Fatalf("the git exec was not bounded: returned after %s", el)
	}
	if err == nil || !strings.Contains(err.Error(), "timed out after 300ms") {
		t.Fatalf("want a timed-out error, got %v", err)
	}
}
