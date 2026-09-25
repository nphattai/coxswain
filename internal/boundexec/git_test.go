package boundexec

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// stubGit puts a fake `git` running script first on PATH, and execs it once untimed (`git warm` exits at once): macOS
// may scan a freshly written script for seconds before its first exec, which must not be charged to a bound under test.
func stubGit(t *testing.T, script string) {
	t.Helper()
	bin := t.TempDir()
	path := filepath.Join(bin, "git")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n[ \"$1\" = warm ] && exit 0\n"+script), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := exec.Command(path, "warm").Run(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// Every git verb cox runs has one bound by its class: local reads 10s, ref writes and unlisted verbs 30s, remote or
// whole-tree verbs 120s.
func TestGitBoundByVerbClass(t *testing.T) {
	for args, want := range map[string]time.Duration{
		"-C p rev-parse --path-format=absolute --git-common-dir": GitReadBound,
		"-C p rev-list --count a..b":                             GitReadBound,
		"-C p show-ref --verify --quiet refs/heads/b":            GitReadBound,
		"-C p merge-base --is-ancestor a b":                      GitReadBound,
		"-C p branch --show-current":                             GitActBound,
		"-C p branch -m b":                                       GitActBound,
		"-C p fetch --quiet origin +refs/heads/b:r":              GitTreeBound,
		"-C p ls-remote --heads origin b":                        GitTreeBound,
		"-C p switch --detach":                                   GitTreeBound,
		"-C p reset --hard base":                                 GitTreeBound,
		"-C r worktree add p b":                                  GitTreeBound,
		"-c k=v -C p status":                                     GitReadBound,
		"":                                                       GitActBound,
	} {
		if got := GitBound(strings.Fields(args)); got != want {
			t.Errorf("GitBound(%q) = %s, want %s", args, got, want)
		}
	}
}

// A git that hangs past its bound returns a timed-out error within the bound (+ Grace), never the hang.
func TestGitHangReturnsTimedOut(t *testing.T) {
	defer func(r time.Duration) { GitReadBound = r }(GitReadBound)
	GitReadBound = 300 * time.Millisecond
	stubGit(t, "sleep 30\n")
	start := time.Now()
	_, err := Git("-C", t.TempDir(), "rev-parse", "HEAD")
	if el := time.Since(start); el > 2*time.Second {
		t.Fatalf("the bound did not stop the hung git: %s", el)
	}
	if err == nil || !strings.Contains(err.Error(), "git rev-parse: timed out after 300ms") {
		t.Fatalf("want a timed-out error naming the verb, got %v", err)
	}
}

// Stdout and a non-zero status pass through; git is never allowed to prompt.
func TestGitPassesStatusAndOutput(t *testing.T) {
	stubGit(t, "echo \"prompt=$GIT_TERMINAL_PROMPT\"; exit 3\n")
	out, err := Git("status")
	if err == nil || err.Error() != "exit status 3" || strings.TrimSpace(string(out)) != "prompt=0" {
		t.Fatalf("out=%q err=%v", out, err)
	}
}
