// Package baseline replays a story from the repository state that existed BEFORE its solution, so a harness's
// unassisted performance can be measured without the answer leaking through git history. The core guard is LeakCheck:
// it refuses a replay whose --before sha already contains the story's solution branch. Recording a run appends one line
// to docs/baselines/<harness>-<date>.md. It never fetches PR refs and never exposes the solution branch to the worker.
package baseline

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Condition is how the story is replayed: bare (minimal prompt, no cox AGENTS.md or hooks) or v2 (the full cox
// dispatch). These are the two the brief measures against each other.
const (
	ConditionBare = "bare"
	ConditionV2   = "v2"
)

// LeakCheck reports whether replaying story from the before sha would leak its solution. It is a leak when the story's
// solution branch story/<story> exists in repo and its tip is already reachable from before (an ancestor of it): the
// worktree checked out at before would then contain the answer. A missing solution branch is not a leak - that is the
// normal case, where before is the pre-solution HEAD and nothing has been written yet. detail carries the resolved
// solution tip (or a note) for the caller's message.
func LeakCheck(repo, story, before string) (leak bool, detail string, err error) {
	if _, err := gitOut(repo, "rev-parse", "--verify", before+"^{commit}"); err != nil {
		return false, "", fmt.Errorf("before sha %q does not resolve in %s: %w", before, repo, err)
	}
	tip, err := gitOut(repo, "rev-parse", "--verify", "refs/heads/story/"+story)
	if err != nil {
		return false, "no solution branch story/" + story + " (nothing to leak)", nil
	}
	// tip an ancestor of before => before already contains the solution.
	if err := gitRun(repo, "merge-base", "--is-ancestor", tip, before); err == nil {
		return true, "solution branch story/" + story + " (" + short(tip) + ") is already reachable from before", nil
	}
	return false, "solution branch story/" + story + " (" + short(tip) + ") is not in before", nil
}

// Row is one recorded baseline run.
type Row struct {
	Story     string
	SHA       string
	Harness   string
	Condition string
	Result    string // pass | fail | unknown | dry-run
	LeaderFix string // count of leader fixes, or "-" when not measured
}

// Record appends row to docs/baselines/<harness>-<date>.md under baselinesDir, creating the file with a table header
// (and the n=1 smoke caveat) when it does not exist yet. date is caller-supplied (YYYY-MM-DD) so it is testable.
func Record(baselinesDir, date string, row Row) (string, error) {
	if err := os.MkdirAll(baselinesDir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(baselinesDir, row.Harness+"-"+date+".md")
	if _, err := os.Stat(path); os.IsNotExist(err) {
		header := fmt.Sprintf("# Baseline replays - %s - %s\n\n"+
			"Small sample (often n=1): a smoke of the replay framework, not a measurement. Read each row as one run.\n\n"+
			"| story | before sha | harness | condition | test result | leader fixes |\n"+
			"|---|---|---|---|---|---|\n", row.Harness, date)
		if err := os.WriteFile(path, []byte(header), 0o644); err != nil {
			return "", err
		}
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return "", err
	}
	defer f.Close()
	line := fmt.Sprintf("| %s | %s | %s | %s | %s | %s |\n",
		row.Story, short(row.SHA), row.Harness, row.Condition, orDash(row.Result), orDash(row.LeaderFix))
	if _, err := f.WriteString(line); err != nil {
		return "", err
	}
	return path, nil
}

func short(sha string) string {
	sha = strings.TrimSpace(sha)
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

func orDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "-"
	}
	return s
}

func gitOut(repo string, args ...string) (string, error) {
	out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func gitRun(repo string, args ...string) error {
	return exec.Command("git", append([]string{"-C", repo}, args...)...).Run()
}
