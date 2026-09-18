// Package brief is the brief channel: the context pack a leader sends a worker alongside the story. Build writes
// <epic>/briefs/<id>/context.md with the paths, the epic HEAD sha, the applicable rulings, and the current attempt,
// plus a relaunch opening line pointing at `cox checkpoint inject`. The story file stays the source of truth; the
// context pack only points at what surrounds it.
package brief

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/nphattai/coxswain/internal/protocol/checkpoint"
	"github.com/nphattai/coxswain/internal/state"
	"github.com/nphattai/coxswain/internal/workspace"
)

// Build writes briefs/<story>/context.md and returns its path. The attempt is read from the event log (default 1 when
// the story has no events yet). The epic HEAD sha is read from git; a non-repo epic dir yields "unknown". Rulings come
// from cox/policy.yaml when present (M3 introduces the file; M2 leaves the placeholder line).
func Build(epicDir, story string) (string, error) {
	slug := filepath.Base(epicDir)
	dir := filepath.Join(epicDir, "briefs", story)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create brief dir: %w", err)
	}

	attempt := currentAttempt(epicDir, story)
	head := epicHead(epicDir)

	var b strings.Builder
	fmt.Fprintf(&b, "# Brief context for %s (attempt %d)\n\n", story, attempt)
	b.WriteString("Đọc checkpoint bằng `cox checkpoint inject` trước, rồi tiếp từ Next action.\n\n")
	b.WriteString("## Paths\n")
	fmt.Fprintf(&b, "- story: %s\n", filepath.Join(epicDir, "stories", story+".md"))
	fmt.Fprintf(&b, "- DESIGN.md: %s\n", filepath.Join(epicDir, "DESIGN.md"))
	fmt.Fprintf(&b, "- env: %s\n", filepath.Join(epicDir, ".env."+story))
	fmt.Fprintf(&b, "- inbox: %s\n", filepath.Join(epicDir, "inbox", story))
	fmt.Fprintf(&b, "- checkpoint: %s\n", checkpoint.Path(epicDir, story))
	b.WriteString("\n## Epic state\n")
	fmt.Fprintf(&b, "- epic branch HEAD: %s\n", head)
	fmt.Fprintf(&b, "- base branch: origin/epic/%s\n", slug)
	fmt.Fprintf(&b, "- attempt: %d\n", attempt)
	b.WriteString("\n## Rulings áp dụng\n")
	for _, r := range rulings(epicDir) {
		fmt.Fprintf(&b, "- %s\n", r)
	}

	path := filepath.Join(dir, "context.md")
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		return "", fmt.Errorf("write brief context: %w", err)
	}
	return path, nil
}

func currentAttempt(epicDir, story string) int {
	events, _, err := state.Load(epicDir)
	if err != nil {
		return 1
	}
	snap := state.Fold(events)
	if s := snap.Stories[story]; s != nil && s.Attempt >= 1 {
		return s.Attempt
	}
	return 1
}

func epicHead(epicDir string) string {
	out, err := exec.Command("git", "-C", epicDir, "rev-parse", "--short", "HEAD").Output()
	if err != nil {
		return "unknown"
	}
	return strings.TrimSpace(string(out))
}

// rulings returns the applicable ruling lines from the workspace policy (M3): the `why` of each policy section, so the
// worker sees the reason behind each rule, not just the file path. If no workspace policy is found above the epic, it
// falls back to a placeholder line.
func rulings(epicDir string) []string {
	wsRoot := workspaceRoot(epicDir)
	if wsRoot == "" {
		return []string{"(no cox/policy.json found above the epic)"}
	}
	pol, err := workspace.LoadPolicy(wsRoot)
	if err != nil {
		return []string{"(cox/policy.json present but unreadable: " + err.Error() + ")"}
	}
	return []string{
		"workers_per_repo: " + pol.WorkersPerRepo.Why,
		"waves: " + pol.Waves.Why,
		"context: " + pol.Context.Why,
		"delivery: " + pol.Delivery.Why,
		"harness: " + pol.Harness.Why,
	}
}

// workspaceRoot walks up from the epic dir to the first ancestor holding cox/policy.json.
func workspaceRoot(epicDir string) string {
	dir, err := filepath.Abs(epicDir)
	if err != nil {
		return ""
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "cox", "policy.json")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}
