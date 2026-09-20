package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// init with no --repo (and no workspace.json yet) refuses with a usage exit and writes nothing (AC 2).
func TestWorkspaceInitRefusesWithoutRepo(t *testing.T) {
	root := t.TempDir()
	if code := cmdWorkspaceInit([]string{"--root", root}); code == 0 {
		t.Fatal("init with no --repo must exit non-zero")
	}
	if _, err := os.Stat(filepath.Join(root, "cox", "workspace.json")); !os.IsNotExist(err) {
		t.Error("a refused init must not write workspace.json")
	}
}

// init --repo writes the workspace, the leader hooks for both policy leader harnesses, and is idempotent (AC 1, 3).
func TestWorkspaceInitWritesWorkspaceAndHooks(t *testing.T) {
	root := t.TempDir()
	repo := t.TempDir()
	if code := cmdWorkspaceInit([]string{"--root", root, "--repo", "app=" + repo}); code != 0 {
		t.Fatalf("init exit %d", code)
	}
	for _, p := range []string{
		filepath.Join(root, "cox", "workspace.json"),
		filepath.Join(root, "cox", "policy.json"),
		filepath.Join(root, "AGENTS.md"),
		filepath.Join(root, ".claude", "settings.json"),
		filepath.Join(root, ".codex", "hooks.json"),
		filepath.Join(root, ".agents", "skills", "cox-dispatch", "SKILL.md"),
	} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("init did not write %s: %v", p, err)
		}
	}
	// Idempotent: a second run stays exit 0 and leaves workspace.json byte-identical.
	before, _ := os.ReadFile(filepath.Join(root, "cox", "workspace.json"))
	if code := cmdWorkspaceInit([]string{"--root", root}); code != 0 {
		t.Fatalf("re-run init exit %d", code)
	}
	if after, _ := os.ReadFile(filepath.Join(root, "cox", "workspace.json")); string(after) != string(before) {
		t.Error("re-run rewrote workspace.json")
	}
}

// add-repo persists a repo into workspace.json (AC 2 shares the same code path as epic new --repo alias=ref).
func TestWorkspaceAddRepoCmd(t *testing.T) {
	root := t.TempDir()
	if code := cmdWorkspaceInit([]string{"--root", root, "--repo", "app=" + t.TempDir()}); code != 0 {
		t.Fatalf("init exit %d", code)
	}
	if code := cmdWorkspaceAddRepo([]string{"web=" + t.TempDir(), "--root", root}); code != 0 {
		t.Fatalf("add-repo exit %d", code)
	}
	b, _ := os.ReadFile(filepath.Join(root, "cox", "workspace.json"))
	if !strings.Contains(string(b), `"web"`) {
		t.Errorf("web not persisted:\n%s", b)
	}
}

// doctorExit maps aggregate check outcomes: fail -> 1, unknown (no fail) -> 3, else 0 (AC 5).
func TestDoctorExit(t *testing.T) {
	if doctorExit(true, false) != 1 {
		t.Error("fail must exit 1")
	}
	if doctorExit(true, true) != 1 {
		t.Error("fail wins over unknown")
	}
	if doctorExit(false, true) != 3 {
		t.Error("unknown must exit 3")
	}
	if doctorExit(false, false) != 0 {
		t.Error("clean must exit 0")
	}
}
