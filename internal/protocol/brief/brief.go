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

	mode, yolo := deliveryContract(epicDir, story)
	b.WriteString("\n## Delivery contract\n")
	fmt.Fprintf(&b, "%s\n\n", ContractLine(mode, yolo))
	fmt.Fprintf(&b, "%s\n", ModeParagraph(mode))

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

// deliveryContract resolves the story's delivery mode (from its frontmatter, defaulting to direct-PR) and the epic's
// merge yolo posture (from policy, default off), the two values the brief's Delivery contract line prints (item 8b).
func deliveryContract(epicDir, story string) (mode string, yolo bool) {
	mode = frontmatterValue(epicDir, story, "mode")
	if mode == "" {
		mode = workspace.DefaultDeliveryMode
	}
	if wsRoot := workspaceRoot(epicDir); wsRoot != "" {
		if pol, err := workspace.LoadPolicy(wsRoot); err == nil {
			yolo = pol.MergeYolo()
		}
	}
	return mode, yolo
}

// ContractLine renders the one-line delivery contract the brief and a promotion both print (item 8b/9).
func ContractLine(mode string, yolo bool) string {
	return fmt.Sprintf("Delivery contract: mode=%s yolo=%s", mode, onOff(yolo))
}

// ModeParagraph is the one-paragraph statement of what each delivery mode expects of the worker (item 8b). An unknown
// mode falls back to the direct-PR text so the brief always carries a paragraph.
func ModeParagraph(mode string) string {
	switch mode {
	case workspace.ModeNoMistakes:
		return "no-mistakes: run every gate, open the PR, then WAIT for merge authority - the captain merges with `cox ship merge`. Never push a default branch, merge, or delete a branch."
	case workspace.ModeLocalOnly:
		return "local-only: leave a clean, ready branch; do NOT push and do NOT open a PR. Report done and wait; the captain takes it from here."
	default:
		return "direct-PR: push your branch and open the PR; no extra pipeline. Wait for the captain to merge with `cox ship merge`."
	}
}

// onOff renders a bool as on|off for the delivery contract line.
func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

// frontmatterValue reads one top-level `key: value` from a story file's leading --- frontmatter block, or "" when the
// file or key is absent. It mirrors cmd/cox's readStoryMeta without importing the command package.
func frontmatterValue(epicDir, story, key string) string {
	b, err := os.ReadFile(filepath.Join(epicDir, "stories", story+".md"))
	if err != nil {
		return ""
	}
	inFM := false
	for _, line := range strings.Split(string(b), "\n") {
		t := strings.TrimSpace(line)
		if t == "---" {
			if inFM {
				break
			}
			inFM = true
			continue
		}
		if !inFM {
			continue
		}
		k, v, ok := strings.Cut(t, ":")
		if ok && strings.TrimSpace(k) == key {
			return strings.TrimSpace(strings.SplitN(v, "#", 2)[0])
		}
	}
	return ""
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
