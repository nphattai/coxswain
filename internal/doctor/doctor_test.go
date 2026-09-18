package doctor

import (
	"os"
	"path/filepath"
	"testing"
)

func touch(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}
}

// Scan finds a v1 install at level 1 and a deeper one at level 3, and reports each install's epic control dirs.
func TestScanFindsInstallsAndEpics(t *testing.T) {
	root := t.TempDir()
	// level-1 v1 install with a v1 epic
	touch(t, filepath.Join(root, "base", "bin", "lib.sh"))
	touch(t, filepath.Join(root, "base", "proj", "epics", "e1", ".run"))
	// level-3 v1 install (workspace/mount/crewkit) with a v2 epic
	touch(t, filepath.Join(root, "ws", "mount", "crewkit", "bin", "lib.sh"))
	touch(t, filepath.Join(root, "ws", "mount", "crewkit", "proj", "epics", "e2", ".cox", "events.jsonl"))

	insts := Scan([]string{root})
	if len(insts) != 2 {
		t.Fatalf("found %d installations, want 2: %+v", len(insts), insts)
	}
	byPath := map[string]Installation{}
	for _, in := range insts {
		byPath[in.Path] = in
	}
	base := byPath[filepath.Join(root, "base")]
	if base.Type != "v1" || len(base.Epics) != 1 || !base.Epics[0].HasRun {
		t.Fatalf("base install wrong: %+v", base)
	}
	deep := byPath[filepath.Join(root, "ws", "mount", "crewkit")]
	if deep.Type != "v1" || len(deep.Epics) != 1 || !deep.Epics[0].HasCox {
		t.Fatalf("deep install wrong: %+v", deep)
	}
}

// Only installations that own an epic count toward divergence.
func withEpic(p string) []Epic { return []Epic{{Path: p, HasRun: true}} }

func TestIssuesVersionMismatch(t *testing.T) {
	insts := []Installation{
		{Path: "/a", Type: "v1", Version: "aaa111", Epics: withEpic("/a/e")},
		{Path: "/b", Type: "v1", Version: "bbb222", Epics: withEpic("/b/e")},
		{Path: "/c", Type: "v1", Version: "unknown", Epics: withEpic("/c/e")}, // unknown is ignored
	}
	issues := Issues(insts)
	if len(issues) != 1 {
		t.Fatalf("want 1 issue, got %v", issues)
	}
}

func TestIssuesNoMismatchWhenAligned(t *testing.T) {
	insts := []Installation{
		{Path: "/a", Type: "v1", Version: "same", Epics: withEpic("/a/e")},
		{Path: "/b", Type: "v1", Version: "same", Epics: withEpic("/b/e")},
	}
	if issues := Issues(insts); len(issues) != 0 {
		t.Fatalf("aligned versions should have no issue, got %v", issues)
	}
}

// Dev checkouts (no epics) are excluded from the divergence check, even when their versions differ from the
// operational installs.
func TestIssuesIgnoresDevCheckouts(t *testing.T) {
	insts := []Installation{
		{Path: "/op1", Type: "v1", Version: "same", Epics: withEpic("/op1/e")},
		{Path: "/op2", Type: "v1", Version: "same", Epics: withEpic("/op2/e")},
		{Path: "/dev1", Type: "v1", Version: "wildly-different"}, // no epics -> ignored
		{Path: "/dev2", Type: "v1", Version: "also-different"},   // no epics -> ignored
	}
	if issues := Issues(insts); len(issues) != 0 {
		t.Fatalf("dev checkouts must not trigger divergence, got %v", issues)
	}
}

// A v1 mount that symlinks bin/ into a shared kit resolves its KitPath to that kit, and the mount + the kit itself
// de-duplicate to one installation (the shallower/first-seen mount path is kept).
func TestScanResolvesKitViaSymlinkAndDedups(t *testing.T) {
	root := t.TempDir()
	// real kit checkout
	kit := filepath.Join(root, "zzz-kit")
	touch(t, filepath.Join(kit, "bin", "lib.sh"))
	// mount whose bin/ symlinks into the kit's bin/
	mount := filepath.Join(root, "aaa-mount")
	if err := os.MkdirAll(mount, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(kit, "bin"), filepath.Join(mount, "bin")); err != nil {
		t.Fatal(err)
	}

	insts := Scan([]string{root})
	if len(insts) != 1 {
		t.Fatalf("mount + kit should dedup to 1 installation, got %d: %+v", len(insts), insts)
	}
	// aaa-mount sorts before zzz-kit, so it is seen first and kept.
	if insts[0].Path != mount {
		t.Fatalf("kept path = %q, want mount %q", insts[0].Path, mount)
	}
	wantKit, _ := filepath.EvalSymlinks(kit)
	if insts[0].KitPath != wantKit {
		t.Fatalf("KitPath = %q, want resolved kit %q", insts[0].KitPath, wantKit)
	}
}

func TestIssuesRunBesideCox(t *testing.T) {
	insts := []Installation{
		{Path: "/a", Type: "v1", Version: "v", Epics: []Epic{
			{Path: "/a/proj/epics/e", HasRun: true, HasCox: true},
		}},
	}
	issues := Issues(insts)
	if len(issues) != 1 {
		t.Fatalf("want 1 issue for .run beside .cox, got %v", issues)
	}
}
