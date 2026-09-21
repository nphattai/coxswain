// Package codex is the Codex harness adapter: no hooks, so wake is pull (the leader drains each turn and blocks on
// `cox wake wait` when idle), checkpoints are manual (the worker writes them at phase boundaries), and telemetry is
// unknown. It satisfies harness.Harness. See docs/adapters/codex.md.
package codex

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/nphattai/coxswain/internal/adapter/harness"
)

// Harness is the Codex adapter. Home overrides the base for ~/.codex in tests; empty uses $HOME.
type Harness struct {
	Home string
}

// New returns a Codex adapter.
func New() *Harness { return &Harness{} }

func (h *Harness) home() string {
	if h.Home != "" {
		return h.Home
	}
	return os.Getenv("HOME")
}

func (h *Harness) Card() harness.Capability {
	return harness.Capability{
		Name:             "codex",
		Roles:            []harness.Role{harness.RoleLeader, harness.RoleWorker},
		Wake:             harness.WakePull,
		Checkpoint:       harness.CheckpointManual,
		Doorbell:         true,
		Interrupt:        true,
		BackendInterrupt: true, // codex aborts on the backend ESC keystroke
		Telemetry:        false,
		Sandbox:          true,
		Instructions:     "AGENTS.md + markdown skills",
	}
}

// Package renders instructions for Codex: it reads AGENTS.md natively, so Package only ensures AGENTS.md exists in dst
// (it is a no-op when absent). Skills are delivered as Markdown out of band.
func (h *Harness) Package(role harness.Role, dst string) error {
	if _, err := os.Stat(filepath.Join(dst, "AGENTS.md")); err != nil {
		return nil // no job description to package; not an error
	}
	return nil
}

// LaunchArgs returns the full argv to start Codex: `codex --model <id> <policy flags> [-c network] [--add-dir root...]
// <prompt>`. Codex owns its sandbox resource flags (DESIGN launch seam): under the workspace-write sandbox a dispatched
// worker also writes into the epic dir, the Go build cache, and the linked worktree's git common dir (all outside the
// worktree), and needs loopback network for its httptest suites, so the writable roots (--add-dir) and network config
// (-c) are typed here, before the prompt. A read-only sandbox or an arena role gets none. Wake is pull, so the idle-wait
// discipline lives in AGENTS.md, not a launch flag.
func (h *Harness) LaunchArgs(l harness.Launch) []string {
	args := []string{"codex"}
	if l.Model != "" {
		args = append(args, "--model", l.Model)
	}
	for _, f := range l.Flags {
		if f != "" {
			args = append(args, f)
		}
	}
	if codexWorkspaceWrite(l) {
		// Codex's workspace-write sandbox blocks loopback binds, so a worker cannot run the repo's httptest suites; codex
		// enables it via a -c override (captain-owned risk, like the autonomy flags). Read-only and arena roles get none.
		args = append(args, "-c", "sandbox_workspace_write.network_access=true")
		for _, root := range writableRoots(l) {
			args = append(args, "--add-dir", root)
		}
	}
	if prompt := harness.WorkerPrompt(l.Role, l.Brief); prompt != "" {
		args = append(args, prompt)
	}
	return args
}

// Telemetry is always Unknown for Codex: it exposes no session log to read (F11: unknown, never 0).
func (h *Harness) Telemetry(session string) (harness.Context, error) {
	return harness.Context{Known: false}, nil
}

// PrepareWorktree marks wt trusted for Codex by ensuring a `[projects."<abspath>"]` table with `trust_level = "trusted"`
// in ~/.codex/config.toml, so a dispatched worker never stalls on the interactive repository-trust prompt (DESIGN obs
// #2). The repo depends only on the standard library (no TOML writer), so the table is appended when absent rather than
// re-serializing the whole file: TOML tables are order-independent, and an existing table for this exact path is left
// untouched (appending a duplicate would be a TOML error). cox never changes any other codex setting.
func (h *Harness) PrepareWorktree(wt string) error {
	abs, err := filepath.Abs(wt)
	if err != nil {
		return fmt.Errorf("codex PrepareWorktree: resolve %q: %w", wt, err)
	}
	path := filepath.Join(h.home(), ".codex", "config.toml")
	header := fmt.Sprintf("[projects.%s]", tomlQuoteKey(abs))
	// Hold an exclusive lock across the read-append-write so concurrent dispatches never read the same snapshot and
	// drop each other's trust table, and write via a unique temp (not a shared fixed name).
	return harness.WithFileLock(path+".cox.lock", func() error {
		existing, err := os.ReadFile(path)
		if err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("codex PrepareWorktree: read %s: %w", path, err)
		}
		updated, changed := ensureCodexTrusted(string(existing), header)
		if !changed {
			return nil // the project table already reads trust_level = "trusted"
		}
		if err := harness.AtomicWriteFile(path, []byte(updated), 0o600); err != nil {
			return fmt.Errorf("codex PrepareWorktree: write %s: %w", path, err)
		}
		return nil
	})
}

// ensureCodexTrusted returns content with the project table `header` set to trust_level = "trusted", and whether it
// changed. It appends the table when absent, sets trust_level when the table exists without it, and REWRITES an existing
// trust_level whose value is not "trusted" (e.g. "untrusted") - it never treats a matching header as trusted on sight.
func ensureCodexTrusted(content, header string) (string, bool) {
	lines := strings.Split(content, "\n")
	hi := -1
	for i, l := range lines {
		if strings.TrimSpace(l) == header {
			hi = i
			break
		}
	}
	if hi == -1 {
		var b strings.Builder
		b.WriteString(content)
		if len(content) > 0 && !strings.HasSuffix(content, "\n") {
			b.WriteByte('\n')
		}
		fmt.Fprintf(&b, "\n%s\ntrust_level = \"trusted\"\n", header)
		return b.String(), true
	}
	// Scan the table body (until the next table header or EOF) for a trust_level key.
	end := len(lines)
	for i := hi + 1; i < len(lines); i++ {
		if strings.HasPrefix(strings.TrimSpace(lines[i]), "[") {
			end = i
			break
		}
	}
	for i := hi + 1; i < end; i++ {
		if strings.HasPrefix(strings.TrimSpace(lines[i]), "trust_level") {
			if tomlStringValue(lines[i]) == "trusted" {
				return content, false // already trusted
			}
			lines[i] = "trust_level = \"trusted\"" // rewrite an untrusted/other value
			return strings.Join(lines, "\n"), true
		}
	}
	// Table exists but has no trust_level: insert it right after the header.
	out := append([]string{}, lines[:hi+1]...)
	out = append(out, "trust_level = \"trusted\"")
	out = append(out, lines[hi+1:]...)
	return strings.Join(out, "\n"), true
}

// tomlStringValue extracts the unquoted value of a `key = "value"` line, or "" when it cannot.
func tomlStringValue(line string) string {
	_, rhs, ok := strings.Cut(line, "=")
	if !ok {
		return ""
	}
	return strings.Trim(strings.TrimSpace(rhs), `"`)
}

// tomlQuoteKey renders an absolute path as a TOML basic-string quoted key, escaping backslashes and quotes so a path
// with either stays a single valid key. Paths rarely contain them, but the escape keeps the emitted TOML valid.
func tomlQuoteKey(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return `"` + s + `"`
}

// codexWorkspaceWrite reports whether this launch is a codex worker in the workspace-write sandbox (not read-only, not
// an arena role). It gates the per-spawn writable roots and the loopback network config: both apply only to a sandboxed
// codex worker that writes outside its worktree and runs tests.
func codexWorkspaceWrite(l harness.Launch) bool {
	return !l.Arena && !hasReadOnlySandbox(l.Flags)
}

// writableRoots returns the directories a dispatched codex worker needs writable beyond its worktree: the epic dir
// (where its reports/questions/checkpoint live under <epic>/), the Go build cache (which `go test` writes), and the
// linked worktree's git common dir (the shared objects + per-worktree index it commits into, outside the worktree). An
// empty root is skipped.
func writableRoots(l harness.Launch) []string {
	var roots []string
	if epic := epicFromStoryPath(l.Brief.StoryPath); epic != "" {
		roots = append(roots, epic)
	}
	if c := goCacheDir(); c != "" {
		roots = append(roots, c)
	}
	if g := gitCommonDir(l.Worktree); g != "" {
		roots = append(roots, g)
	}
	return roots
}

// hasReadOnlySandbox reports whether the codex launch flags request the read-only sandbox (`-s read-only`). A read-only
// sandbox refuses additional writable roots, so codex types no --add-dir when it is set.
func hasReadOnlySandbox(flags []string) bool {
	for i, f := range flags {
		if f == "-s" && i+1 < len(flags) && flags[i+1] == "read-only" {
			return true
		}
	}
	return false
}

// gitCommonDir returns the absolute git common directory of the worktree. For a linked worktree this is the main
// checkout's .git, which holds the shared objects and the per-worktree index under worktrees/<name>/ - both written
// during a commit. Empty on any error (empty path, not a worktree, git missing), so a non-git launch gets no such root.
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

// goCacheDir resolves the Go build cache directory the way the go tool does: $GOCACHE when set, else <user cache
// dir>/go-build. Empty only when the home/cache dir cannot be resolved.
func goCacheDir() string {
	if c := strings.TrimSpace(os.Getenv("GOCACHE")); c != "" {
		return c
	}
	if d, err := os.UserCacheDir(); err == nil {
		return filepath.Join(d, "go-build")
	}
	return ""
}

// epicFromStoryPath returns the epic dir from a story path <epic>/stories/<id>.md (the parent of the stories dir), or "".
func epicFromStoryPath(storyPath string) string {
	if storyPath == "" {
		return ""
	}
	return filepath.Dir(filepath.Dir(storyPath))
}
