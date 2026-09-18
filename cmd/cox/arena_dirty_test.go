package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// The arena mode decision must ignore changes under the epic dir itself: the run regenerates the pack and role stories
// there before deciding, and an epic that lives inside the reviewed repo would otherwise always force terminal mode.
func TestGitDirtyOutsideIgnoresEpicDir(t *testing.T) {
	repo := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q")
	run("-c", "user.email=t@t", "-c", "user.name=t", "commit", "-q", "--allow-empty", "-m", "init")
	epic := filepath.Join(repo, "epics", "e1")
	if err := os.MkdirAll(filepath.Join(epic, "reports", "arena"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(epic, "reports", "arena", "context-pack.md"), []byte("pack"), 0o644); err != nil {
		t.Fatal(err)
	}
	if gitDirtyOutside(repo, epic) {
		t.Fatalf("epic-dir change must not count as dirty")
	}
	if !gitDirty(repo) {
		t.Fatalf("plain gitDirty must still see the untracked file")
	}
	if err := os.WriteFile(filepath.Join(repo, "main.go"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !gitDirtyOutside(repo, epic) {
		t.Fatalf("a change outside the epic dir must count as dirty")
	}
}

// git status collapses a wholly-untracked directory to a single entry (the dir) unless -uall is passed; that collapsed
// entry sits above the epic dir and its prefix would not match, so the arena would wrongly see terminal-mode dirtiness.
// -uall lists each file, so every file under the epic dir is filtered; a file outside it still counts.
func TestGitDirtyOutsideUntrackedCollapse(t *testing.T) {
	repo := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q")
	run("-c", "user.email=t@t", "-c", "user.name=t", "commit", "-q", "--allow-empty", "-m", "init")

	// The whole epics/ tree is untracked, so without -uall git collapses it to "?? epics/" (above the epic dir); several
	// nested files under the epic dir exercise that collapse.
	epic := filepath.Join(repo, "epics", "e1")
	for _, rel := range []string{"reports/arena/context-pack.md", "stories/arena-adversary.md", "reports/arena/round-1-adversary.md"} {
		p := filepath.Join(epic, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if gitDirtyOutside(repo, epic) {
		t.Fatalf("untracked files under the epic dir must not count as dirty (needs -uall so they are not collapsed above it)")
	}

	// An untracked file in a sibling dir outside the epic dir still counts as dirty.
	other := filepath.Join(repo, "other")
	if err := os.MkdirAll(other, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(other, "new.md"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !gitDirtyOutside(repo, epic) {
		t.Fatalf("an untracked file outside the epic dir must count as dirty")
	}
}
