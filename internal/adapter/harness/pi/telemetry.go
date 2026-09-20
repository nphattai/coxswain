package pi

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/nphattai/coxswain/internal/adapter/harness"
)

// telemetry reads Pi token/turn usage from the worktree's session JSONL. It binds to the session directory Pi derives
// from the worktree cwd and requires exactly one session file there: zero (missing) or more than one (ambiguous, e.g.
// after /resume forked a new session) returns Known=false rather than guessing "newest" (DESIGN section 5, F11). An
// unreadable file or a session with no assistant usage is also Known=false, never 0.
func (h *Harness) telemetry(worktree string) (harness.Context, error) {
	file, ok := sessionFile(h.home(), worktree)
	if !ok {
		return harness.Context{Known: false}, nil
	}
	return parseSession(file), nil
}

// sessionFile returns the single Pi session JSONL for a worktree, or ok=false when there is not exactly one. Pi stores
// sessions under ~/.pi/agent/sessions/--<cwd-with-separators-as-dashes>--/<timestamp>_<session-id>.jsonl.
func sessionFile(home, worktree string) (string, bool) {
	dir := sessionDir(home, worktree)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", false // missing dir (or unreadable) => unknown
	}
	var files []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".jsonl") {
			files = append(files, filepath.Join(dir, e.Name()))
		}
	}
	if len(files) != 1 {
		return "", false // zero => missing; more than one => ambiguous; both are unknown, not a newest-heuristic pick
	}
	return files[0], true
}

// sessionDir maps a worktree cwd to Pi's session directory name: the leading separator is dropped and '/', '\\', ':'
// are replaced with '-', wrapped in leading and trailing '--' (verified against pi 0.85.1 docs/session-format.md).
func sessionDir(home, worktree string) string {
	p := strings.TrimPrefix(worktree, string(os.PathSeparator))
	p = strings.NewReplacer("/", "-", "\\", "-", ":", "-").Replace(p)
	return filepath.Join(home, ".pi", "agent", "sessions", "--"+p+"--")
}

// piUsage is the subset of Pi's Usage we read: the input-side tokens that occupy the context window (input + cached).
// Output/reasoning are not context occupancy, matching the claude adapter's input+cache sum.
type piUsage struct {
	Input       int `json:"input"`
	CacheRead   int `json:"cacheRead"`
	CacheWrite  int `json:"cacheWrite"`
	CacheWrite1 int `json:"cacheWrite1h"`
}

func (u piUsage) contextTokens() int { return u.Input + u.CacheRead + u.CacheWrite + u.CacheWrite1 }

// parseSession sums the last assistant message's context tokens and counts assistant turns. Taking the LAST assistant
// usage naturally reflects compaction (the post-compaction turn carries the reduced context). Malformed lines are
// skipped; a session with no assistant usage is Known=false, never 0 (F11).
func parseSession(path string) harness.Context {
	f, err := os.Open(path)
	if err != nil {
		return harness.Context{Known: false}
	}
	defer f.Close()

	var last piUsage
	haveUsage := false
	turns := 0
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		var line struct {
			Type    string `json:"type"`
			Message struct {
				Role  string  `json:"role"`
				Usage piUsage `json:"usage"`
			} `json:"message"`
		}
		if err := json.Unmarshal(sc.Bytes(), &line); err != nil {
			continue // malformed line: skip, do not fail the whole read
		}
		if line.Type == "message" && line.Message.Role == "assistant" {
			last = line.Message.Usage
			haveUsage = true
			turns++
		}
	}
	if err := sc.Err(); err != nil || !haveUsage {
		return harness.Context{Known: false}
	}
	return harness.Context{Tokens: last.contextTokens(), Turns: turns, Known: true}
}
