package workspace

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Scaffold writes the whole workspace (registry, services/, .gitignore, AGENTS.md, embedded skills) and is idempotent:
// a re-run creates nothing new and reports everything present.
func TestScaffoldWritesEverythingAndIsIdempotent(t *testing.T) {
	root := t.TempDir()
	repo := t.TempDir() // an absolute path is all Load validation needs
	rep, err := Scaffold(root, []Repo{{Alias: "app", Path: repo, Production: "main"}})
	if err != nil {
		t.Fatalf("scaffold: %v", err)
	}
	for _, p := range []string{
		filepath.Join(root, "cox", "workspace.json"),
		filepath.Join(root, "cox", "policy.json"),
		filepath.Join(root, "cox", "services"),
		filepath.Join(root, ".gitignore"),
		filepath.Join(root, "AGENTS.md"),
		filepath.Join(root, ".agents", "skills", "cox-epic", "SKILL.md"),
	} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("missing %s: %v", p, err)
		}
	}
	// .gitignore carries the machine-bound rules including the per-alias symlink line.
	gi, _ := os.ReadFile(filepath.Join(root, ".gitignore"))
	for _, rule := range []string{"cox/workspace.json", "cox/.cache/", "**/.cox/", "**/.cox.closed/", "*/epics/*/app"} {
		if !strings.Contains(string(gi), rule) {
			t.Errorf(".gitignore missing rule %q:\n%s", rule, gi)
		}
	}
	if len(rep.Created) == 0 {
		t.Error("first scaffold should report created files")
	}

	// Re-run: nothing created, everything present, files unchanged.
	wsBefore, _ := os.ReadFile(filepath.Join(root, "cox", "workspace.json"))
	rep2, err := Scaffold(root, nil)
	if err != nil {
		t.Fatalf("re-run scaffold: %v", err)
	}
	for _, c := range rep2.Created {
		t.Errorf("idempotent re-run created %q", c)
	}
	if wsAfter, _ := os.ReadFile(filepath.Join(root, "cox", "workspace.json")); string(wsAfter) != string(wsBefore) {
		t.Error("re-run rewrote workspace.json")
	}
}

// Scaffold refuses to write a placeholder: a brand-new workspace needs at least one repo.
func TestScaffoldRefusesWithoutRepo(t *testing.T) {
	root := t.TempDir()
	if _, err := Scaffold(root, nil); err == nil {
		t.Fatal("scaffold with no repo and no workspace.json must fail")
	}
	if _, err := os.Stat(filepath.Join(root, "cox", "workspace.json")); !os.IsNotExist(err) {
		t.Error("a refused scaffold must not write workspace.json")
	}
}

// AddRepo persists a repo into workspace.json and refuses a duplicate alias.
func TestAddRepoPersistsAndRefusesDuplicate(t *testing.T) {
	root := t.TempDir()
	if _, err := Scaffold(root, []Repo{{Alias: "app", Path: t.TempDir(), Production: "main"}}); err != nil {
		t.Fatal(err)
	}
	if err := AddRepo(root, Repo{Alias: "web", Path: t.TempDir(), Production: "main"}); err != nil {
		t.Fatalf("add-repo: %v", err)
	}
	ws, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := ws.Repo("web"); !ok {
		t.Error("web not persisted into workspace.json")
	}
	if err := AddRepo(root, Repo{Alias: "web", Path: t.TempDir(), Production: "main"}); err == nil {
		t.Error("add-repo must refuse a duplicate alias")
	}
}

// Validate names the offending field for each structural problem.
func TestValidateFieldNamed(t *testing.T) {
	cases := map[string]*Workspace{
		"alias is required":             {Repos: []Repo{{Path: "/a", Production: "main"}}},
		"duplicate alias":               {Repos: []Repo{{Alias: "a", Name: "x", Production: "main"}, {Alias: "a", Name: "y", Production: "main"}}},
		"needs path or name":            {Repos: []Repo{{Alias: "a", Production: "main"}}},
		"must be absolute":              {Repos: []Repo{{Alias: "a", Path: "rel/path", Production: "main"}}},
		"production branch is required": {Repos: []Repo{{Alias: "a", Name: "x"}}},
	}
	for want, ws := range cases {
		err := ws.Validate()
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("validate: want error containing %q, got %v", want, err)
		}
	}
	if err := (&Workspace{Repos: []Repo{{Alias: "a", Name: "org/x", Production: "main"}}}).Validate(); err != nil {
		t.Errorf("a valid workspace must pass: %v", err)
	}
}

// DetectProduction reads a checkout's origin/HEAD, falling back to main for a non-git or remote-less path.
func TestDetectProduction(t *testing.T) {
	if got := DetectProduction(t.TempDir()); got != "main" {
		t.Errorf("non-git path production = %q, want main", got)
	}
	repo := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q", "-b", "trunk")
	run("commit", "-q", "--allow-empty", "-m", "init")
	run("update-ref", "refs/remotes/origin/trunk", "HEAD")
	run("symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/trunk")
	if got := DetectProduction(repo); got != "trunk" {
		t.Errorf("production = %q, want trunk (from origin/HEAD)", got)
	}
}
