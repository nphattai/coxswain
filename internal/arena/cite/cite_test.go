package cite

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestParse(t *testing.T) {
	cases := []struct {
		in   string
		want Citation
		ok   bool
	}{
		{"cox/internal/foo.go:12", Citation{Raw: "cox/internal/foo.go:12", Alias: "cox", Path: "internal/foo.go", Line: 12}, true},
		{"cox/a/b.go:3@abc1234", Citation{Raw: "cox/a/b.go:3@abc1234", Alias: "cox", Path: "a/b.go", Line: 3, SHA: "abc1234"}, true},
		{"noslash:12", Citation{}, false},
		{"just prose", Citation{}, false},
	}
	for _, c := range cases {
		got, ok := Parse(c.in)
		if ok != c.ok || (ok && got != c.want) {
			t.Errorf("Parse(%q) = %+v,%v want %+v,%v", c.in, got, ok, c.want, c.ok)
		}
	}
}

func TestFindOnlyKnownAliases(t *testing.T) {
	text := "see cox/internal/a.go:5 and other/x.go:9 and http://host/y:80"
	cits := Find(text, map[string]bool{"cox": true})
	if len(cits) != 1 || cits[0].Alias != "cox" {
		t.Fatalf("Find = %+v, want one cox citation", cits)
	}
}

// gitRepo makes a repo with one file and returns its dir and HEAD sha.
func gitRepo(t *testing.T, relpath, content string) (dir, sha string) {
	t.Helper()
	dir = t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	run("init", "-q")
	full := filepath.Join(dir, relpath)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", ".")
	run("commit", "-qm", "init")
	out, err := exec.Command("git", "-C", dir, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	return dir, strings.TrimSpace(string(out))
}

// epicWith links alias -> repoDir under a fresh epic dir.
func epicWith(t *testing.T, alias, repoDir string) string {
	t.Helper()
	epicDir := t.TempDir()
	if err := os.Symlink(repoDir, filepath.Join(epicDir, alias)); err != nil {
		t.Fatal(err)
	}
	return epicDir
}

// An epic with a repos file mapping an alias to an absolute checkout path resolves and verifies with no symlink at all
// (the v2 dogfood bootstrap that the arena reviewer flagged as split-brained).
func TestVerifyFromReposFileNoSymlink(t *testing.T) {
	repo, sha := gitRepo(t, "internal/foo.go", "one\ntwo\nthree\n")
	epicDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(epicDir, "repos"), []byte("cox "+repo+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// No <epic>/cox symlink exists; resolution must come from the repos file path.
	if _, err := os.Lstat(filepath.Join(epicDir, "cox")); err == nil {
		t.Fatal("test setup created a symlink; it must be absent")
	}
	if err := Verify(epicDir, Citation{Raw: "cox/internal/foo.go:3@" + sha, Alias: "cox", Path: "internal/foo.go", Line: 3, SHA: sha}); err != nil {
		t.Errorf("repos-file citation should verify without a symlink: %v", err)
	}
	// An alias with no repos entry and no symlink is still unknown.
	if err := Verify(epicDir, Citation{Raw: "web/x.go:1", Alias: "web", Path: "x.go", Line: 1}); err == nil {
		t.Error("unknown alias should error")
	}
}

func TestVerify(t *testing.T) {
	repo, sha := gitRepo(t, "internal/foo.go", "one\ntwo\nthree\n")
	epicDir := epicWith(t, "cox", repo)

	if err := Verify(epicDir, Citation{Raw: "cox/internal/foo.go:3", Alias: "cox", Path: "internal/foo.go", Line: 3}); err != nil {
		t.Errorf("valid HEAD citation: %v", err)
	}
	if err := Verify(epicDir, Citation{Raw: "cox/internal/foo.go:3@" + sha, Alias: "cox", Path: "internal/foo.go", Line: 3, SHA: sha}); err != nil {
		t.Errorf("valid sha citation: %v", err)
	}

	// Wrong line, wrong file, wrong sha, unknown alias each fail with a clear message.
	bad := []Citation{
		{Raw: "cox/internal/foo.go:99", Alias: "cox", Path: "internal/foo.go", Line: 99},
		{Raw: "cox/internal/missing.go:1", Alias: "cox", Path: "internal/missing.go", Line: 1},
		{Raw: "cox/internal/foo.go:1@deadbeef", Alias: "cox", Path: "internal/foo.go", Line: 1, SHA: "deadbeef"},
		{Raw: "web/x.go:1", Alias: "web", Path: "x.go", Line: 1},
	}
	for _, c := range bad {
		if err := Verify(epicDir, c); err == nil {
			t.Errorf("expected error for %s", c.Raw)
		}
	}
}
