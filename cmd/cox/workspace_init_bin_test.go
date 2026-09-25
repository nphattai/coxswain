package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nphattai/coxswain/internal/workspace"
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

// B-43 against the real binary: a workspace policy that predates the required `merge` section used to abort init with
// `policy validation failed: merge` before the hooks step. Init now writes the template section into the file, names
// the key on an `updated` line, and carries on to the hooks.
func TestWorkspaceInitBinaryWritesMissingPolicySection(t *testing.T) {
	root := t.TempDir()
	coxInit(t, root, "--repo", "app="+t.TempDir()+":main")
	pol := filepath.Join(root, "cox", "policy.json")
	b, err := os.ReadFile(pol)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	delete(m, "merge")
	b, _ = json.MarshalIndent(m, "", "  ")
	if err := os.WriteFile(pol, append(b, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}

	out := coxInit(t, root)
	if !strings.Contains(out, "updated "+pol+" (+merge from template)\n") {
		t.Errorf("init did not name the added key:\n%s", out)
	}
	if !strings.Contains(out, "hooks: ") {
		t.Errorf("init stopped before the hooks step:\n%s", out)
	}
	if _, err := workspace.LoadPolicy(root); err != nil {
		t.Errorf("policy still invalid after init: %v", err)
	}
}

// B-55a against the real binary: the second init reports the Pi leader extension as already current, and no line tells
// the captain to "load with -e" (the workspace-level extension is auto-discovered by pi).
func TestWorkspaceInitBinaryPiExtensionAlreadyCurrent(t *testing.T) {
	root := t.TempDir()
	first := coxInit(t, root, "--repo", "app="+t.TempDir()+":main")
	if !strings.Contains(first, "installed cox pi extension") {
		t.Fatalf("first init did not install the pi extension:\n%s", first)
	}
	second := coxInit(t, root)
	if strings.Contains(second, "installed cox pi extension") || !strings.Contains(second, "already current (pi leader, unbound)") {
		t.Errorf("second init re-reported the pi install instead of already current:\n%s", second)
	}
	if strings.Contains(first+second, "load with -e") {
		t.Errorf("init told the captain to load an auto-discovered leader extension with -e:\n%s%s", first, second)
	}
}
