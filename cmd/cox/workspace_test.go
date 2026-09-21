package main

import (
	"path/filepath"
	"testing"
)

// parseRepoFlag expands a leading ~ so `--repo alias=~/path` becomes an absolute checkout path, not a backend repo name
// (finding 11). A bare ~ expands to $HOME; a path with no leading ~ is untouched; a `~/path:production` keeps its
// production branch.
func TestParseRepoFlagExpandsTilde(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	r, err := parseRepoFlag("app=~/Work/acme")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(home, "Work", "acme")
	if r.Path != want {
		t.Errorf("~ not expanded: Path=%q Name=%q, want Path=%q", r.Path, r.Name, want)
	}
	if r.Name != "" {
		t.Errorf("an expanded ~ path must be a Path, not a Name: %q", r.Name)
	}

	// A production branch after the last ':' still splits correctly with a ~ path.
	r, err = parseRepoFlag("web=~/Work/web:release")
	if err != nil {
		t.Fatal(err)
	}
	if r.Path != filepath.Join(home, "Work", "web") || r.Production != "release" {
		t.Errorf("~ path with production wrong: %+v", r)
	}

	// A bare ~ expands to $HOME.
	if r, err = parseRepoFlag("h=~"); err != nil || r.Path != home {
		t.Errorf("bare ~ = %+v (err %v), want Path=%q", r, err, home)
	}

	// An absolute path is untouched (no ~).
	abs := filepath.Join(home, "abs")
	if r, err = parseRepoFlag("a=" + abs); err != nil || r.Path != abs {
		t.Errorf("absolute path changed: %+v (err %v)", r, err)
	}
}
