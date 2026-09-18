package backend

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// LaunchLine builds the shell line a terminal-plane backend types into a fresh worker terminal: the cox env prefix
// (COX_EPIC/COX_STORY derived from the story path, COX_PLANE=terminal) followed by the harness command and its single
// prompt argument. claude and codex both launch as `<name> <prompt>` (harness.LaunchArgs), so both terminal-plane
// backends (orca, herdr) build the command here without importing the harness layer (decision 0002). Shared so the env
// contract and quoting cannot drift between backends.
func LaunchLine(h HarnessSpec, brief Brief) string {
	var b strings.Builder
	if epic := EpicFromPath(brief.StoryPath); epic != "" {
		fmt.Fprintf(&b, "COX_EPIC=%s ", shellQuote(epic))
	}
	if story := StoryFromPath(brief.StoryPath); story != "" {
		fmt.Fprintf(&b, "COX_STORY=%s ", shellQuote(story))
	}
	b.WriteString("COX_PLANE=terminal ")
	b.WriteString(h.Name)
	// Model is always typed on the terminal plane so a worker never silently inherits the harness's ambient default
	// (captain ruling: workers run claude-opus-4-8). The caller resolves it to a non-empty id from policy before Spawn.
	// claude and codex both spell it `--model <id>` (verified via `--help`, 2026-09-15). Effort is not emitted: neither
	// harness exposes a launch-time effort flag today, so HarnessSpec.Effort is threaded but unused until one does.
	if h.Model != "" {
		fmt.Fprintf(&b, " --model %s", shellQuote(h.Model))
	}
	// Approval/autonomy flags come from policy (harness.launch.<name>), typed after the model and before the prompt: a
	// dispatched worker cannot answer a local approval prompt, so it runs autonomously (claude bypassPermissions, codex
	// -a never). The captain owns this risk; docs/adapters/{claude,codex}.md record how to tighten it.
	for _, f := range h.LaunchFlags {
		if f == "" {
			continue
		}
		fmt.Fprintf(&b, " %s", shellQuote(f))
	}
	// Codex's workspace-write sandbox blocks loopback binds, so a worker cannot run the repo's httptest suites (M13b:
	// `httptest.NewServer` cannot bind loopback), and the leader had to rerun `go test -race ./...` outside the sandbox.
	// Codex's own help states network access in workspace-write depends on `[sandbox_workspace_write] network_access`
	// (verified against codex-cli 0.154, docs/adapters/codex.md), so cox enables it as a `-c` override for a workspace-write
	// codex worker (captain-owned risk, like the autonomy flags). Read-only sandboxes and arena roles get none.
	if codexWorkspaceWrite(h, brief) {
		fmt.Fprintf(&b, " -c %s", shellQuote("sandbox_workspace_write.network_access=true"))
	}
	// Codex's -s workspace-write sandboxes writes to the worktree only, but a dispatched worker also writes into the
	// epic dir (its reports, questions, and checkpoint under <epic>/) and needs its Go build cache writable to run
	// `go test`, so a bare launch failed with "mkdir <epic>/questions: operation not permitted" and go test could not
	// write GOCACHE (M10c, live codex smoke). It also commits in a linked worktree, whose git objects and per-worktree
	// index live under the git common dir OUTSIDE the worktree (M13b q001: `.git/worktrees/<name>/index.lock`, operation
	// not permitted). Codex's --add-dir marks extra writable roots alongside the workspace (the CLI form of
	// sandbox_workspace_write.writable_roots); the policy flags stay the base and these per-spawn roots are appended
	// here. Terminal plane only; claude has no such sandbox.
	for _, root := range writableRoots(h, brief) {
		fmt.Fprintf(&b, " --add-dir %s", shellQuote(root))
	}
	if prompt := PromptFromBrief(brief); prompt != "" {
		b.WriteByte(' ')
		b.WriteString(shellQuote(prompt))
	}
	return b.String()
}

// writableRoots returns the directories a dispatched worker of harness h needs writable beyond its worktree. Only
// codex needs them (its sandbox confines writes to the workspace): the epic dir, where its reports/questions/checkpoint
// live under <epic>/, and the Go build cache, which `go test` writes. An empty root is skipped. None are typed when the
// sandbox is read-only (codex refuses extra writable roots under -s read-only and exits, M12b) or for an arena role (it
// writes only its report in its own worktree, collected later, so it needs no root beyond the workspace, ADR 0013).
func writableRoots(h HarnessSpec, brief Brief) []string {
	if !codexWorkspaceWrite(h, brief) {
		return nil
	}
	var roots []string
	if epic := EpicFromPath(brief.StoryPath); epic != "" {
		roots = append(roots, epic)
	}
	if c := goCacheDir(); c != "" {
		roots = append(roots, c)
	}
	// A linked worktree commits into the git common dir (shared objects + the per-worktree index) outside the worktree,
	// so grant it writable or the worker cannot `git commit` (M13b q001).
	if g := gitCommonDir(brief.Worktree); g != "" {
		roots = append(roots, g)
	}
	return roots
}

// codexWorkspaceWrite reports whether this launch is a codex worker in the workspace-write sandbox (not read-only, not
// an arena role). It gates the per-spawn writable roots and the loopback network config: both apply only to a sandboxed
// codex worker that writes outside its worktree and runs tests.
func codexWorkspaceWrite(h HarnessSpec, brief Brief) bool {
	return h.Name == "codex" && !brief.Arena && !hasReadOnlySandbox(h.LaunchFlags)
}

// gitCommonDir returns the absolute git common directory of the worktree (git rev-parse --path-format=absolute
// --git-common-dir). For a linked worktree this is the main checkout's .git, which holds the shared objects and the
// per-worktree index under worktrees/<name>/ - both written during a commit. Empty on any error (empty path, not a
// worktree, git missing), so a non-git launch simply gets no such root.
func gitCommonDir(wtPath string) string {
	if wtPath == "" {
		return ""
	}
	out, err := exec.Command("git", "-C", wtPath, "rev-parse", "--path-format=absolute", "--git-common-dir").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// hasReadOnlySandbox reports whether the codex launch flags request the read-only sandbox (`-s read-only`). A read-only
// sandbox refuses additional writable roots, so cox types no --add-dir when it is set.
func hasReadOnlySandbox(flags []string) bool {
	for i, f := range flags {
		if f == "-s" && i+1 < len(flags) && flags[i+1] == "read-only" {
			return true
		}
	}
	return false
}

// goCacheDir resolves the Go build cache directory the way the go tool does: $GOCACHE when set, else <user cache
// dir>/go-build (os.UserCacheDir honours XDG on Linux and Library/Caches on macOS). Empty only when the home/cache
// dir cannot be resolved.
func goCacheDir() string {
	if c := strings.TrimSpace(os.Getenv("GOCACHE")); c != "" {
		return c
	}
	if d, err := os.UserCacheDir(); err == nil {
		return filepath.Join(d, "go-build")
	}
	return ""
}

// PromptFromBrief renders the single prompt argument the worker harness receives, matching harness.LaunchArgs for the
// worker role: the story-file instruction when a story path is set (with the inline Text appended as a progress note),
// else the inline Text alone.
func PromptFromBrief(brief Brief) string {
	if brief.StoryPath != "" {
		p := "Your task is the story file " + brief.StoryPath + " - read it in full and follow its Working rules exactly."
		if brief.Text != "" {
			p += " Progress note from your previous attempt: " + brief.Text
		}
		return p
	}
	return brief.Text
}

// StoryFromPath returns the story id from a story path <epic>/stories/<id>.md, or "".
func StoryFromPath(storyPath string) string {
	if storyPath == "" {
		return ""
	}
	return strings.TrimSuffix(filepath.Base(storyPath), ".md")
}

// EpicFromPath returns the epic dir from a story path <epic>/stories/<id>.md (the parent of the stories dir), or "".
func EpicFromPath(storyPath string) string {
	if storyPath == "" {
		return ""
	}
	return filepath.Dir(filepath.Dir(storyPath))
}

// shellQuote wraps s in single quotes for safe interpolation into a typed shell line, escaping embedded single quotes
// as the standard POSIX '\” idiom. An empty string becomes ”.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
