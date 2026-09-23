package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nphattai/coxswain/internal/adapter/harness/pi"
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

// init with pi in the policy's leader options installs the unbound Pi leader extension (hash-verifiable, NO epic
// marker), ignores .pi/extensions/ in .gitignore, preserves a pre-existing .gitignore line, and is idempotent
// (DESIGN item 3). The template policy lists pi as a leader option.
func TestWorkspaceInitInstallsUnboundPiExtension(t *testing.T) {
	root := t.TempDir()
	// A pre-existing user .gitignore line must survive (AC 1: existing lines are never rewritten).
	mustWrite(t, filepath.Join(root, ".gitignore"), "my-custom-ignore/\n")
	if code := cmdWorkspaceInit([]string{"--root", root, "--repo", "app=" + t.TempDir()}); code != 0 {
		t.Fatalf("init exit %d", code)
	}
	extDir := filepath.Join(root, ".pi", "extensions")
	if _, ok := pi.VerifyExtension(root); !ok {
		t.Fatalf("init did not install a verifiable pi extension under %s", extDir)
	}
	if _, err := os.Stat(filepath.Join(extDir, "cox-pi.epic")); !os.IsNotExist(err) {
		t.Error("init must install the pi leader extension WITHOUT an epic marker (unbound)")
	}
	gi, _ := os.ReadFile(filepath.Join(root, ".gitignore"))
	if !strings.Contains(string(gi), ".pi/extensions/") {
		t.Errorf(".gitignore missing .pi/extensions/:\n%s", gi)
	}
	if !strings.Contains(string(gi), "my-custom-ignore/") {
		t.Errorf("init rewrote/dropped a pre-existing .gitignore line:\n%s", gi)
	}
	// Idempotent: a second run keeps it verifiable, still unbound, and leaves .gitignore byte-identical.
	giBefore := string(gi)
	if code := cmdWorkspaceInit([]string{"--root", root}); code != 0 {
		t.Fatalf("re-run init exit %d", code)
	}
	if _, ok := pi.VerifyExtension(root); !ok {
		t.Error("re-run left the pi extension unverifiable")
	}
	if _, err := os.Stat(filepath.Join(extDir, "cox-pi.epic")); !os.IsNotExist(err) {
		t.Error("re-run must not add an epic marker")
	}
	if giAfter, _ := os.ReadFile(filepath.Join(root, ".gitignore")); string(giAfter) != giBefore {
		t.Error("re-run rewrote .gitignore")
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

// parseRepoFlag splits alias=ref[:production] on the last ':', so a production branch with slashes (release/2026) is
// kept whole and a slashless path has no production split (PR#3 review round 3, finding 3).
func TestParseRepoFlagProductionWithSlashes(t *testing.T) {
	r, err := parseRepoFlag("app=/abs/path:release/2026")
	if err != nil {
		t.Fatal(err)
	}
	if r.Path != "/abs/path" || r.Production != "release/2026" {
		t.Errorf("got path=%q production=%q, want /abs/path and release/2026", r.Path, r.Production)
	}
	// A name ref with a slashed production.
	n, err := parseRepoFlag("svc=org/repo:release/2026")
	if err != nil {
		t.Fatal(err)
	}
	if n.Name != "org/repo" || n.Production != "release/2026" {
		t.Errorf("got name=%q production=%q, want org/repo and release/2026", n.Name, n.Production)
	}
	// No production given: no split, production is detected/defaulted (main for a non-git path).
	p, err := parseRepoFlag("web=/abs/nogit")
	if err != nil {
		t.Fatal(err)
	}
	if p.Path != "/abs/nogit" || p.Production != "main" {
		t.Errorf("got path=%q production=%q, want /abs/nogit and main", p.Path, p.Production)
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
