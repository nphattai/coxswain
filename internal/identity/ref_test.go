package identity

import (
	"os"
	"path/filepath"
	"testing"
)

// A prefix is never a match: /x/story-a must not match /x/story-a-2 (F07).
func TestSamePathRejectsPrefix(t *testing.T) {
	base := t.TempDir()
	a := filepath.Join(base, "story-a")
	a2 := filepath.Join(base, "story-a-2")
	for _, d := range []string{a, a2} {
		if err := os.Mkdir(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	same, err := SamePath(a, a2)
	if err != nil {
		t.Fatal(err)
	}
	if same {
		t.Error("story-a must not match story-a-2")
	}
	same, err = SamePath(a, a)
	if err != nil || !same {
		t.Errorf("a must match itself: same=%v err=%v", same, err)
	}
}

// A symlink and its target canonicalize equal.
func TestSamePathResolvesSymlink(t *testing.T) {
	base := t.TempDir()
	real := filepath.Join(base, "real")
	if err := os.Mkdir(real, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	same, err := SamePath(real, link)
	if err != nil {
		t.Fatal(err)
	}
	if !same {
		t.Error("a symlink must match its target after canonicalization")
	}
}
