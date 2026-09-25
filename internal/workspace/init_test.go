package workspace

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nphattai/coxswain/skills"
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
	for _, rule := range []string{"cox/workspace.json", "cox/.cache/", "**/.cox/", "**/.cox.closed/", ".pi/extensions/", "**/epics/*/app"} {
		if !strings.Contains(string(gi), rule) {
			t.Errorf(".gitignore missing rule %q:\n%s", rule, gi)
		}
	}
	if len(rep.Created) == 0 {
		t.Error("first scaffold should report created files")
	}
	// The registry must not carry null projects/services (PR#3 review finding 7).
	wsBytes, _ := os.ReadFile(filepath.Join(root, "cox", "workspace.json"))
	if strings.Contains(string(wsBytes), "null") {
		t.Errorf("workspace.json must emit [] not null:\n%s", wsBytes)
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

// The embedded template policy lists pi as a leader and worker option (the reference the stale-options notice compares
// an on-disk policy against).
func TestTemplatePolicyListsPi(t *testing.T) {
	tmpl, err := TemplatePolicy()
	if err != nil {
		t.Fatal(err)
	}
	if !contains(tmpl.Harness.Leader.Options, "pi") {
		t.Errorf("template harness.leader.options should list pi: %v", tmpl.Harness.Leader.Options)
	}
	if !contains(tmpl.Harness.Worker.Options, "pi") {
		t.Errorf("template harness.worker.options should list pi: %v", tmpl.Harness.Worker.Options)
	}
}

// StaleOptionNotices reports one notice per harness the template lists but the on-disk policy lacks (DESIGN item 6):
// missing options only, none when current.
func TestStaleOptionNotices(t *testing.T) {
	var tmpl Policy
	tmpl.Harness.Leader.Options = []string{"claude", "codex", "pi"}
	tmpl.Harness.Worker.Options = []string{"claude", "codex", "pi"}
	// On-disk policy predates pi in the leader list (still lists it for workers).
	var onDisk Policy
	onDisk.Harness.Leader.Options = []string{"claude", "codex"}
	onDisk.Harness.Worker.Options = []string{"claude", "codex", "pi"}

	notices := StaleOptionNotices(&onDisk, &tmpl)
	if len(notices) != 1 {
		t.Fatalf("want exactly one notice, got %d: %v", len(notices), notices)
	}
	if !strings.Contains(notices[0], `harness.leader.options is missing "pi"`) {
		t.Errorf("notice should name the missing pi leader option: %q", notices[0])
	}
	// A current policy (template vs itself) produces no notices, and on-disk extras are never reported.
	if n := StaleOptionNotices(&tmpl, &tmpl); len(n) != 0 {
		t.Errorf("a current policy must produce no notices, got %v", n)
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

// An invalid seed (e.g. a repo with an empty production, as a --from-repos-md row can produce) is rejected before any
// write, so it never wedges the workspace on disk.
func TestScaffoldRejectsInvalidSeedBeforeWrite(t *testing.T) {
	root := t.TempDir()
	if _, err := Scaffold(root, []Repo{{Alias: "a", Path: t.TempDir()}}); err == nil { // no production
		t.Fatal("scaffold with an invalid seed must fail")
	}
	if _, err := os.Stat(filepath.Join(root, "cox", "workspace.json")); !os.IsNotExist(err) {
		t.Error("an invalid seed must not be written to disk")
	}
}

// AddRepo extends .gitignore with the new alias's per-epic symlink rule.
func TestAddRepoExtendsGitignore(t *testing.T) {
	root := t.TempDir()
	if _, err := Scaffold(root, []Repo{{Alias: "app", Path: t.TempDir(), Production: "main"}}); err != nil {
		t.Fatal(err)
	}
	if err := AddRepo(root, Repo{Alias: "web", Path: t.TempDir(), Production: "main"}); err != nil {
		t.Fatal(err)
	}
	gi, _ := os.ReadFile(filepath.Join(root, ".gitignore"))
	if !strings.Contains(string(gi), "**/epics/*/web") {
		t.Errorf(".gitignore missing the new alias rule:\n%s", gi)
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

// Validate rejects a repo alias that is not a single path-safe component (it is joined into the epic dir, so "..", a
// separator, or a space would escape it) - PR#3 review round 3, finding 1.
func TestValidateRejectsUnsafeAlias(t *testing.T) {
	for _, bad := range []string{"..", ".", "a/b", "../evil", "a b", "sub/../x"} {
		ws := &Workspace{Repos: []Repo{{Alias: bad, Path: "/x", Production: "main"}}}
		if err := ws.Validate(); err == nil {
			t.Errorf("unsafe alias %q must be rejected", bad)
		}
	}
	if err := (&Workspace{Repos: []Repo{{Alias: "app-1.web_2", Path: "/x", Production: "main"}}}).Validate(); err != nil {
		t.Errorf("a safe alias must pass: %v", err)
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

// B-72: a re-run of Scaffold after a driver upgrade rewrites every embedded skill file whose content differs from the
// embed and reports it in Updated; a file the embed does not carry is left alone, and a current tree reports nothing.
func TestScaffoldRefreshesStaleSkills(t *testing.T) {
	root := t.TempDir()
	if _, err := Scaffold(root, []Repo{{Alias: "app", Path: t.TempDir(), Production: "main"}}); err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(root, ".agents", "skills", "cox-dispatch", "SKILL.md")
	if err := os.WriteFile(stale, []byte("STALE-MARKER\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	own := filepath.Join(root, ".agents", "skills", "my-own", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(own), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(own, []byte("mine\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	rep, err := Scaffold(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	want, _ := skills.FS.ReadFile("cox-dispatch/SKILL.md")
	if got, _ := os.ReadFile(stale); string(got) != string(want) {
		t.Errorf("stale skill not refreshed from the embed:\n%s", got)
	}
	if len(rep.Updated) != 1 || rep.Updated[0] != stale {
		t.Errorf("Updated = %q, want [%s]", rep.Updated, stale)
	}
	if got, _ := os.ReadFile(own); string(got) != "mine\n" {
		t.Errorf("a file outside the embed was touched: %q", got)
	}

	rep, err = Scaffold(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Updated) != 0 {
		t.Errorf("current skills re-run reported Updated %q", rep.Updated)
	}
}
