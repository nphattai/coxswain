// Package claude is the Claude Code harness adapter: wake push via hooks, automatic checkpoints via PreCompact and
// SessionStart shims, and telemetry read from the session log. It satisfies harness.Harness. See docs/adapters/claude.md.
package claude

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/nphattai/coxswain/internal/adapter/harness"
)

// Harness is the Claude Code adapter. Home overrides the base for the session-log dir in tests; empty uses $HOME.
type Harness struct {
	Home string
}

// New returns a Claude Code adapter.
func New() *Harness { return &Harness{} }

func (h *Harness) Card() harness.Capability {
	return harness.Capability{
		Name:         "claude",
		Roles:        []harness.Role{harness.RoleLeader, harness.RoleWorker},
		Wake:         harness.WakePush,
		Checkpoint:   harness.CheckpointAuto,
		Doorbell:     true,
		Interrupt:    true,
		Telemetry:    true,
		Sandbox:      false,
		Instructions: "plugin skills + AGENTS.md",
	}
}

// Package renders instructions for Claude Code: AGENTS.md is the harness-neutral job description and Claude reads it
// via CLAUDE.md (a symlink is the plugin convention; a copy is written when symlinking is not possible). Skills are
// delivered as plugin skills out of band, so Package's job here is to make AGENTS.md visible as CLAUDE.md in dst.
func (h *Harness) Package(role harness.Role, dst string) error {
	return linkAgentsAsClaude(dst)
}

// LaunchArgs returns the argv to start Claude Code in wt. The worker's brief is the story file (read in full);
// a relaunch asks it to inject the checkpoint first. Wake is push, so no idle-wait arg is needed.
func (h *Harness) LaunchArgs(role harness.Role, wt string, b harness.Brief) []string {
	args := []string{"claude"}
	if role == harness.RoleWorker && b.StoryPath != "" {
		prompt := "Your task is the story file " + b.StoryPath + " - read it in full and follow its Working rules exactly."
		if b.InjectCheckpoint {
			prompt = "Read your checkpoint with `cox checkpoint inject` first, then continue from Next action. " + prompt
		}
		if b.Note != "" {
			prompt += " Progress note from your previous attempt: " + b.Note
		}
		args = append(args, prompt)
	}
	return args
}

// Telemetry ports v1 inbox-lib.sh session_ctx: find the newest session log for the worktree path and sum the last
// assistant usage. No log => Known=false (Unknown), never 0 tokens (F11).
func (h *Harness) Telemetry(session string) (harness.Context, error) {
	log, err := newestLog(h.home(), session)
	if err != nil || log == "" {
		return harness.Context{Known: false}, nil // no log is not an error: it is simply unknown
	}
	f, err := os.Open(log)
	if err != nil {
		return harness.Context{Known: false}, nil
	}
	defer f.Close()

	var lastUsage map[string]any
	turns := 0
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		var o struct {
			Message struct {
				Usage map[string]any `json:"usage"`
			} `json:"message"`
		}
		if err := json.Unmarshal(sc.Bytes(), &o); err != nil {
			continue
		}
		if o.Message.Usage != nil {
			lastUsage = o.Message.Usage
			turns++
		}
	}
	if lastUsage == nil {
		return harness.Context{Known: false}, nil
	}
	tokens := 0
	for _, k := range []string{"input_tokens", "cache_read_input_tokens", "cache_creation_input_tokens"} {
		if v, ok := lastUsage[k].(float64); ok {
			tokens += int(v)
		}
	}
	return harness.Context{Tokens: tokens, Turns: turns, Known: true}, nil
}

func (h *Harness) home() string {
	if h.Home != "" {
		return h.Home
	}
	return os.Getenv("HOME")
}

// logDir maps a worktree path to Claude's project log dir: ~/.claude/projects/<path-with-slashes-as-dashes>.
func logDir(home, worktree string) string {
	return filepath.Join(home, ".claude", "projects", strings.ReplaceAll(worktree, "/", "-"))
}

// newestLog returns the most recently modified *.jsonl in the worktree's log dir, or "" when there is none.
func newestLog(home, worktree string) (string, error) {
	dir := logDir(home, worktree)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	type fileInfo struct {
		path    string
		modTime int64
	}
	var logs []fileInfo
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		logs = append(logs, fileInfo{filepath.Join(dir, e.Name()), info.ModTime().UnixNano()})
	}
	if len(logs) == 0 {
		return "", nil
	}
	sort.Slice(logs, func(i, j int) bool { return logs[i].modTime > logs[j].modTime })
	return logs[0].path, nil
}

// linkAgentsAsClaude makes dst/CLAUDE.md point at dst/AGENTS.md (symlink, or a copy when symlinking fails). It is a
// no-op when AGENTS.md is absent, so Package never fails on a worktree that carries no job description.
func linkAgentsAsClaude(dst string) error {
	agents := filepath.Join(dst, "AGENTS.md")
	if _, err := os.Stat(agents); err != nil {
		return nil
	}
	claude := filepath.Join(dst, "CLAUDE.md")
	if _, err := os.Lstat(claude); err == nil {
		return nil // already present
	}
	if err := os.Symlink("AGENTS.md", claude); err == nil {
		return nil
	}
	data, err := os.ReadFile(agents)
	if err != nil {
		return err
	}
	return os.WriteFile(claude, data, 0o644)
}
