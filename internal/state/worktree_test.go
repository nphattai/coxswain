package state

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadWorktreeRecord(t *testing.T) {
	epic := t.TempDir()
	if err := os.MkdirAll(filepath.Join(epic, ControlDir, "wt"), 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(story, body string) {
		t.Helper()
		if err := os.WriteFile(WorktreePath(epic, story), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("json", `{"path":"/wt/a","attempt":2}`)
	write("legacy", "/wt/b\n")
	write("empty", "")
	write("badjson", `{"path":`)
	write("nopath", `{"attempt":1}`)

	for _, c := range []struct {
		story, path string
		attempt     int
		wantErr     bool
	}{
		{"json", "/wt/a", 2, false},
		{"legacy", "/wt/b", 0, false},
		{"empty", "", 0, false},
		{"absent", "", 0, false},
		{"badjson", "", 0, true},
		{"nopath", "", 0, true},
	} {
		r, err := ReadWorktreeRecord(epic, c.story)
		if (err != nil) != c.wantErr || r.Path != c.path || r.Attempt != c.attempt {
			t.Errorf("%s: got (%+v, %v), want path %q attempt %d err %v", c.story, r, err, c.path, c.attempt, c.wantErr)
		}
		if err != nil && !strings.Contains(err.Error(), WorktreePath(epic, c.story)) {
			t.Errorf("%s: error %q does not name the file", c.story, err)
		}
		if got := ReadWorktree(epic, c.story); got != c.path {
			t.Errorf("%s: ReadWorktree = %q, want %q", c.story, got, c.path)
		}
	}
}

func TestSessionStory(t *testing.T) {
	for name, want := range map[string]string{"s1.json": "s1", "s1.busy.json": "", "s1.json.tmp": "", "README": ""} {
		got, ok := SessionStory(name)
		if got != want || ok != (want != "") {
			t.Errorf("SessionStory(%q) = (%q, %v), want %q", name, got, ok, want)
		}
	}
}
