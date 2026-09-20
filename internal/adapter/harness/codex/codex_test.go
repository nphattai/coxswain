package codex

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nphattai/coxswain/internal/adapter/harness"
)

// PrepareWorktree adds a `[projects."<abspath>"]` trust table with trust_level="trusted" to ~/.codex/config.toml,
// preserving existing content, and is idempotent (a second call adds no duplicate table).
func TestPrepareWorktreeTrust(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, ".codex", "config.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	// Pre-existing config that must survive.
	seed := "[projects.\"/other\"]\ntrust_level = \"trusted\"\n"
	if err := os.WriteFile(path, []byte(seed), 0o600); err != nil {
		t.Fatal(err)
	}
	h := &Harness{Home: home}
	wt := filepath.Join(home, "wt")
	if err := h.PrepareWorktree(wt); err != nil {
		t.Fatalf("PrepareWorktree: %v", err)
	}
	got := readFile(t, path)
	if !strings.Contains(got, "[projects.\"/other\"]") {
		t.Errorf("existing table not preserved:\n%s", got)
	}
	header := "[projects.\"" + wt + "\"]"
	if !strings.Contains(got, header) || !strings.Contains(got, "trust_level = \"trusted\"") {
		t.Fatalf("worktree trust table not added:\n%s", got)
	}
	// Idempotent: a second call must not add a duplicate table (which would be invalid TOML).
	if err := h.PrepareWorktree(wt); err != nil {
		t.Fatalf("second PrepareWorktree: %v", err)
	}
	if n := strings.Count(readFile(t, path), header); n != 1 {
		t.Errorf("expected exactly one trust table for the worktree, got %d", n)
	}
}

// An existing project table with trust_level="untrusted" (or any non-trusted value) must be REWRITTEN to trusted, not
// left as-is on a header-presence check.
func TestPrepareWorktreeReplacesUntrusted(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, ".codex", "config.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	wt := filepath.Join(home, "wt")
	header := "[projects.\"" + wt + "\"]"
	seed := header + "\ntrust_level = \"untrusted\"\n"
	if err := os.WriteFile(path, []byte(seed), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := (&Harness{Home: home}).PrepareWorktree(wt); err != nil {
		t.Fatalf("PrepareWorktree: %v", err)
	}
	got := readFile(t, path)
	if strings.Contains(got, "trust_level = \"untrusted\"") {
		t.Errorf("untrusted trust_level must be replaced:\n%s", got)
	}
	if !strings.Contains(got, "trust_level = \"trusted\"") {
		t.Errorf("table must now be trusted:\n%s", got)
	}
}

// ensureCodexTrusted: append when absent, no-op when already trusted, rewrite when untrusted, insert when the table has
// no trust_level.
func TestEnsureCodexTrusted(t *testing.T) {
	h := "[projects.\"/wt\"]"
	// absent -> append
	out, changed := ensureCodexTrusted("", h)
	if !changed || !strings.Contains(out, h) || !strings.Contains(out, "trust_level = \"trusted\"") {
		t.Fatalf("append case: changed=%v out=%q", changed, out)
	}
	// already trusted -> no change
	if _, changed := ensureCodexTrusted(h+"\ntrust_level = \"trusted\"\n", h); changed {
		t.Errorf("already-trusted must not change")
	}
	// untrusted -> rewrite
	out, changed = ensureCodexTrusted(h+"\ntrust_level = \"untrusted\"\n", h)
	if !changed || strings.Contains(out, "untrusted") {
		t.Errorf("untrusted must be rewritten: %q", out)
	}
	// table without trust_level -> insert
	out, changed = ensureCodexTrusted(h+"\nsome_other = 1\n", h)
	if !changed || !strings.Contains(out, "trust_level = \"trusted\"") {
		t.Errorf("insert case: %q", out)
	}
	// a DIFFERENT project's trusted table must not satisfy this project
	other := "[projects.\"/other\"]\ntrust_level = \"trusted\"\n"
	out, changed = ensureCodexTrusted(other, h)
	if !changed || !strings.Contains(out, h) {
		t.Errorf("must add this project's table alongside another: %q", out)
	}
}

// PrepareWorktree creates config.toml when it does not exist.
func TestPrepareWorktreeCreatesConfig(t *testing.T) {
	home := t.TempDir()
	h := &Harness{Home: home}
	wt := filepath.Join(home, "wt")
	if err := h.PrepareWorktree(wt); err != nil {
		t.Fatalf("PrepareWorktree: %v", err)
	}
	got := readFile(t, filepath.Join(home, ".codex", "config.toml"))
	if !strings.Contains(got, "trust_level = \"trusted\"") {
		t.Fatalf("new config missing trust table:\n%s", got)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// indexOf returns the position of tok in argv, or -1.
func indexOf(argv []string, tok string) int {
	for i, a := range argv {
		if a == tok {
			return i
		}
	}
	return -1
}

func contains(argv []string, tok string) bool { return indexOf(argv, tok) >= 0 }

// LaunchArgs spells the model as `--model <id>` after the executable and types the policy approval flags, in order,
// before the prompt.
func TestLaunchArgsModelAndFlags(t *testing.T) {
	argv := New().LaunchArgs(harness.Launch{
		Role: harness.RoleWorker, Model: "gpt-5.6-sol", Flags: []string{"-a", "never", "-s", "read-only"},
		Brief: harness.Brief{StoryPath: "/e/stories/s.md"},
	})
	if argv[0] != "codex" {
		t.Fatalf("argv[0] = %q, want codex", argv[0])
	}
	if i := indexOf(argv, "--model"); i < 0 || argv[i+1] != "gpt-5.6-sol" {
		t.Fatalf("missing --model gpt-5.6-sol: %v", argv)
	}
	model, flag, prompt := indexOf(argv, "--model"), indexOf(argv, "-a"), len(argv)-1
	if !(model < flag && flag < prompt) {
		t.Fatalf("want --model < flags < prompt: %v", argv)
	}
	if !strings.Contains(argv[prompt], "read it in full") {
		t.Fatalf("last arg should be the prompt: %v", argv)
	}
}

// A codex worker under workspace-write gets the loopback network config and the writable roots (epic dir + Go build
// cache), before the prompt. Claude-style bare launches never reach this adapter.
func TestLaunchArgsWritableRootsAndNetwork(t *testing.T) {
	t.Setenv("GOCACHE", "/tmp/gocache-test")
	argv := New().LaunchArgs(harness.Launch{
		Role: harness.RoleWorker, Model: "m", Flags: []string{"-a", "never", "-s", "workspace-write"},
		Brief: harness.Brief{StoryPath: "/epics/v2/stories/m10c.md"},
	})
	if i := indexOf(argv, "-c"); i < 0 || argv[i+1] != "sandbox_workspace_write.network_access=true" {
		t.Fatalf("missing -c network config: %v", argv)
	}
	// Both roots present as `--add-dir <root>` pairs.
	for _, root := range []string{"/epics/v2", "/tmp/gocache-test"} {
		found := false
		for i, a := range argv {
			if a == "--add-dir" && i+1 < len(argv) && argv[i+1] == root {
				found = true
			}
		}
		if !found {
			t.Errorf("missing --add-dir %s: %v", root, argv)
		}
	}
	if rootAt, promptAt := indexOf(argv, "--add-dir"), len(argv)-1; !(rootAt < promptAt) {
		t.Fatalf("roots must come before the prompt: %v", argv)
	}
}

// A read-only sandbox or an arena role gets no writable roots and no network config (codex refuses extra roots under
// -s read-only, and an arena role writes only its report in its own worktree, ADR 0013).
func TestLaunchArgsNoRootsReadOnlyOrArena(t *testing.T) {
	t.Setenv("GOCACHE", "/tmp/gocache-test")
	ro := New().LaunchArgs(harness.Launch{
		Role: harness.RoleWorker, Model: "m", Flags: []string{"-a", "never", "-s", "read-only"},
		Brief: harness.Brief{StoryPath: "/epics/v2/stories/s.md"},
	})
	if contains(ro, "--add-dir") || contains(ro, "-c") {
		t.Errorf("read-only codex must type no --add-dir/-c: %v", ro)
	}
	arena := New().LaunchArgs(harness.Launch{
		Role: harness.RoleWorker, Model: "m", Arena: true, Flags: []string{"-a", "never", "-s", "workspace-write"},
		Brief: harness.Brief{StoryPath: "/epics/v2/stories/s.md"},
	})
	if contains(arena, "--add-dir") || contains(arena, "-c") {
		t.Errorf("arena codex must type no --add-dir/-c: %v", arena)
	}
}

// A codex worker in a linked git worktree gets the git common dir as a writable root so it can commit (shared objects +
// per-worktree index live under the main checkout's .git, outside the worktree sandbox).
func TestLaunchArgsGitCommonDir(t *testing.T) {
	main := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = main
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q")
	run("-c", "user.email=t@e", "-c", "user.name=t", "commit", "--allow-empty", "-q", "-m", "init")
	run("worktree", "add", "-q", "-b", "story/x", filepath.Join(main, "wt"), "HEAD")
	wt := filepath.Join(main, "wt")
	common := gitCommonDir(wt)
	if common == "" || !strings.HasSuffix(common, ".git") {
		t.Fatalf("gitCommonDir(%q) = %q, want the main checkout's absolute .git", wt, common)
	}
	argv := New().LaunchArgs(harness.Launch{
		Role: harness.RoleWorker, Model: "m", Worktree: wt, Flags: []string{"-a", "never", "-s", "workspace-write"},
		Brief: harness.Brief{StoryPath: "/epics/v2/stories/s.md"},
	})
	found := false
	for i, a := range argv {
		if a == "--add-dir" && i+1 < len(argv) && argv[i+1] == common {
			found = true
		}
	}
	if !found {
		t.Fatalf("codex worker missing git common dir writable root %q: %v", common, argv)
	}
	if g := gitCommonDir(""); g != "" {
		t.Errorf("empty worktree must resolve to no common dir, got %q", g)
	}
	if g := gitCommonDir(t.TempDir()); g != "" {
		t.Errorf("non-git dir must resolve to no common dir, got %q", g)
	}
}
