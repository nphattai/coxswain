package backend

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// LaunchLine always types the resolved model as `--model <id>` after the harness name and before the prompt, so a
// terminal-plane worker never inherits the harness's ambient default (captain ruling: workers run claude-opus-4-8).
func TestLaunchLineTypesModel(t *testing.T) {
	brief := Brief{StoryPath: "/epics/v2/stories/m10b.md"}
	line := LaunchLine(HarnessSpec{Name: "claude", Model: "claude-opus-4-8"}, brief)
	if !strings.Contains(line, "--model 'claude-opus-4-8'") {
		t.Fatalf("launch line missing --model: %q", line)
	}
	// The model comes after the harness name and before the prompt.
	nameAt := strings.Index(line, " claude ")
	if nameAt == -1 {
		nameAt = strings.Index(line, " claude")
	}
	modelAt := strings.Index(line, "--model")
	promptAt := strings.Index(line, "read it in full")
	if !(nameAt < modelAt && modelAt < promptAt) {
		t.Fatalf("expected order name < --model < prompt, got name=%d model=%d prompt=%d in %q", nameAt, modelAt, promptAt, line)
	}
}

// Policy approval flags are typed after the model and before the prompt, and changing the flags changes the launch
// line (they come from HarnessSpec.LaunchFlags, which the caller fills from policy).
func TestLaunchLineTypesApprovalFlags(t *testing.T) {
	brief := Brief{StoryPath: "/e/stories/s.md"}
	line := LaunchLine(HarnessSpec{Name: "codex", Model: "m", LaunchFlags: []string{"-a", "never", "-s", "workspace-write"}}, brief)
	for _, want := range []string{"-a", "never", "-s", "workspace-write"} {
		if !strings.Contains(line, want) {
			t.Fatalf("launch line missing approval flag %q: %q", want, line)
		}
	}
	modelAt := strings.Index(line, "--model")
	flagAt := strings.Index(line, "-a")
	promptAt := strings.Index(line, "read it in full")
	if !(modelAt < flagAt && flagAt < promptAt) {
		t.Fatalf("expected --model < flags < prompt, got model=%d flag=%d prompt=%d in %q", modelAt, flagAt, promptAt, line)
	}
	// A different flag set produces a different line (the flags are not hard-coded).
	other := LaunchLine(HarnessSpec{Name: "claude", Model: "m", LaunchFlags: []string{"--permission-mode", "bypassPermissions"}}, brief)
	if strings.Contains(other, "workspace-write") || !strings.Contains(other, "bypassPermissions") {
		t.Fatalf("flags did not follow the spec: %q", other)
	}
	// No flags: none typed.
	bare := LaunchLine(HarnessSpec{Name: "claude", Model: "m"}, brief)
	if strings.Contains(bare, "--permission-mode") || strings.Contains(bare, "-a ") {
		t.Fatalf("no LaunchFlags must type no approval flags: %q", bare)
	}
}

// An empty model types no --model flag (the caller is responsible for resolving a non-empty model before Spawn; this
// keeps LaunchLine from typing a bare `--model` with no value).
func TestLaunchLineOmitsModelWhenEmpty(t *testing.T) {
	line := LaunchLine(HarnessSpec{Name: "codex"}, Brief{StoryPath: "/e/stories/s.md"})
	if strings.Contains(line, "--model") {
		t.Fatalf("empty model must not type --model: %q", line)
	}
}

// A codex launch line marks the epic dir and the Go build cache as writable roots (--add-dir), since codex's
// workspace-write sandbox refuses writes outside the worktree; claude, which has no such sandbox, gets none.
func TestLaunchLineCodexWritableRoots(t *testing.T) {
	t.Setenv("GOCACHE", "/tmp/gocache-test")
	brief := Brief{StoryPath: "/epics/v2/stories/m10c.md"} // epic dir is /epics/v2
	line := LaunchLine(HarnessSpec{Name: "codex", Model: "gpt-5.6-sol"}, brief)
	for _, want := range []string{"--add-dir '/epics/v2'", "--add-dir '/tmp/gocache-test'"} {
		if !strings.Contains(line, want) {
			t.Fatalf("codex line missing writable root %q: %q", want, line)
		}
	}
	// The roots come before the prompt (they are launch flags, not part of the prompt arg).
	if rootAt, promptAt := strings.Index(line, "--add-dir"), strings.Index(line, "read it in full"); !(rootAt < promptAt) {
		t.Fatalf("expected --add-dir before prompt, got root=%d prompt=%d in %q", rootAt, promptAt, line)
	}
	// Claude has no sandbox, so no --add-dir.
	if c := LaunchLine(HarnessSpec{Name: "claude", Model: "claude-opus-4-8"}, brief); strings.Contains(c, "--add-dir") {
		t.Fatalf("claude must not type --add-dir: %q", c)
	}
}

// A codex launch types no --add-dir when the sandbox is read-only (codex refuses extra writable roots under -s
// read-only and exits) or when the launch is an arena role (it writes only its report in its own worktree, ADR 0013).
// Both must hold even though the epic dir is derivable from the story path.
func TestLaunchLineNoAddDirReadOnlyOrArena(t *testing.T) {
	t.Setenv("GOCACHE", "/tmp/gocache-test")
	brief := Brief{StoryPath: "/epics/v2/stories/arena-adversary.md"} // epic dir is /epics/v2

	// Read-only sandbox: no writable roots.
	ro := LaunchLine(HarnessSpec{Name: "codex", Model: "m", LaunchFlags: []string{"-a", "never", "-s", "read-only"}}, brief)
	if strings.Contains(ro, "--add-dir") {
		t.Errorf("read-only codex must type no --add-dir: %q", ro)
	}

	// Arena role under workspace-write: still no writable roots (report goes to the worktree, collected later).
	arena := LaunchLine(HarnessSpec{Name: "codex", Model: "m", LaunchFlags: []string{"-a", "never", "-s", "workspace-write"}}, Brief{StoryPath: brief.StoryPath, Arena: true})
	if strings.Contains(arena, "--add-dir") {
		t.Errorf("arena codex must type no --add-dir: %q", arena)
	}

	// A plain worker under workspace-write still gets the writable roots (the epic dir it writes into).
	worker := LaunchLine(HarnessSpec{Name: "codex", Model: "m", LaunchFlags: []string{"-a", "never", "-s", "workspace-write"}}, brief)
	if !strings.Contains(worker, "--add-dir '/epics/v2'") {
		t.Errorf("workspace-write worker should get the epic writable root: %q", worker)
	}
}

// A codex worker under workspace-write gets the loopback network config (-c sandbox_workspace_write.network_access=true)
// so its httptest suites can bind loopback; claude, read-only, and arena launches do not.
func TestLaunchLineCodexNetworkAccess(t *testing.T) {
	brief := Brief{StoryPath: "/epics/v2/stories/s.md"}
	ws := []string{"-a", "never", "-s", "workspace-write"}
	const want = "-c 'sandbox_workspace_write.network_access=true'"

	if l := LaunchLine(HarnessSpec{Name: "codex", Model: "m", LaunchFlags: ws}, brief); !strings.Contains(l, want) {
		t.Errorf("codex workspace-write must enable loopback: %q", l)
	}
	if l := LaunchLine(HarnessSpec{Name: "claude", Model: "m"}, brief); strings.Contains(l, "network_access") {
		t.Errorf("claude must not get the codex network config: %q", l)
	}
	ro := LaunchLine(HarnessSpec{Name: "codex", Model: "m", LaunchFlags: []string{"-a", "never", "-s", "read-only"}}, brief)
	if strings.Contains(ro, "network_access") {
		t.Errorf("read-only codex must not enable network: %q", ro)
	}
	arena := LaunchLine(HarnessSpec{Name: "codex", Model: "m", LaunchFlags: ws}, Brief{StoryPath: brief.StoryPath, Arena: true})
	if strings.Contains(arena, "network_access") {
		t.Errorf("arena codex must not enable network: %q", arena)
	}
}

// A codex worker in a linked git worktree gets the git common dir as a writable root, so it can commit (the shared
// objects and per-worktree index live under the main checkout's .git, outside the worktree sandbox).
func TestLaunchLineCodexGitCommonDir(t *testing.T) {
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
	commonDir := gitCommonDir(wt) // the resolved absolute .git (macOS adds a /private symlink prefix)
	if commonDir == "" || !strings.HasSuffix(commonDir, ".git") {
		t.Fatalf("gitCommonDir(%q) = %q, want the main checkout's absolute .git", wt, commonDir)
	}

	brief := Brief{StoryPath: "/epics/v2/stories/s.md", Worktree: wt}
	line := LaunchLine(HarnessSpec{Name: "codex", Model: "m", LaunchFlags: []string{"-a", "never", "-s", "workspace-write"}}, brief)
	if !strings.Contains(line, "--add-dir '"+commonDir+"'") {
		t.Fatalf("codex worker missing git common dir writable root %q: %q", commonDir, line)
	}
	// No worktree path (or a non-git path): no git root, and no crash.
	if g := gitCommonDir(""); g != "" {
		t.Errorf("empty worktree must resolve to no common dir, got %q", g)
	}
	if g := gitCommonDir(t.TempDir()); g != "" {
		t.Errorf("non-git dir must resolve to no common dir, got %q", g)
	}
}
