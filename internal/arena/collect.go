package arena

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/nphattai/coxswain/internal/arena/roles"
	"github.com/nphattai/coxswain/internal/state"
)

// Collect brings each arena role's round-N report home from its worktree into the epic reports dir. A role runs on an
// isolated worktree (its path is recorded in <epic>/.cox/wt/arena-<role>); the template tells it to write the report on
// the absolute epic path, but a role that wrote a relative path leaves the file in its worktree instead. Collect copies
// such a report to <epic>/reports/arena/round-N-<role>.md only when the epic does not already have it, so a report the
// role wrote straight to the epic dir is never clobbered. It returns the reports it copied. A role with no worktree or no
// report is skipped (not every role runs every round), not an error.
func Collect(epicDir string, round int) ([]string, error) {
	var copied []string
	for _, role := range []roles.Role{roles.Adversary, roles.Reviewer, roles.Domain} {
		name := fmt.Sprintf("round-%d-%s.md", round, role)
		dst := filepath.Join(epicDir, "reports", "arena", name)
		if _, err := os.Stat(dst); err == nil {
			continue // the role wrote it straight to the epic dir
		}
		wt := state.ReadWorktree(epicDir, "arena-"+string(role)) // JSON or legacy record; "" when none
		if wt == "" {
			continue
		}
		src := filepath.Join(wt, "reports", "arena", name)
		b, err := os.ReadFile(src)
		if err != nil {
			continue // this role did not write a report in its worktree
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return copied, err
		}
		if err := os.WriteFile(dst, b, 0o644); err != nil {
			return copied, err
		}
		copied = append(copied, dst)
	}
	return copied, nil
}
