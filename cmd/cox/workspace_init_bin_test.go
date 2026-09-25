package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nphattai/coxswain/skills"
)

// coxInit runs the real built cox binary's `workspace init` on root with HOME pinned to a temp dir, returning stdout
// and stderr combined. It fails the test on a non-zero exit.
func coxInit(t *testing.T, root string, args ...string) string {
	t.Helper()
	cmd := exec.Command(fmCoxBin(t), append([]string{"workspace", "init", "--root", root}, args...)...)
	cmd.Env = append(os.Environ(), "HOME="+t.TempDir())
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("cox workspace init: %v\n%s", err, out)
	}
	return string(out)
}

// B-72 against the real binary: after a driver upgrade (simulated by a stale skill file), a second init rewrites the
// file from the embed and names it on an `updated` line.
func TestWorkspaceInitBinaryRefreshesSkills(t *testing.T) {
	root := t.TempDir()
	coxInit(t, root, "--repo", "app="+t.TempDir()+":main")
	stale := filepath.Join(root, ".agents", "skills", "cox-dispatch", "SKILL.md")
	if err := os.WriteFile(stale, []byte("STALE-MARKER\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out := coxInit(t, root)
	if !strings.Contains(out, "updated "+stale+"\n") {
		t.Errorf("second init did not report the refreshed skill:\n%s", out)
	}
	want, _ := skills.FS.ReadFile("cox-dispatch/SKILL.md")
	if got, _ := os.ReadFile(stale); string(got) != string(want) {
		t.Errorf("stale skill survived init:\n%s", got)
	}
	if out := coxInit(t, root); strings.Contains(out, "updated ") {
		t.Errorf("a current workspace reported updates:\n%s", out)
	}
}
