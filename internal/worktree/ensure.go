// Package worktree creates a verified isolated checkout for a story and never falls back. Ensure asks the backend to
// create the worktree, then independently proves the directory exists and is on the requested branch; any mismatch is
// an error with no path returned and no fallback to a shared checkout (F02). Nothing in this package deletes a branch
// (F01) - a source test asserts that.
package worktree

import (
	"fmt"
	"os"
	"strings"

	"github.com/nphattai/coxswain/internal/adapter/backend"
	"github.com/nphattai/coxswain/internal/boundexec"
)

// Ensure creates and verifies a story worktree. It returns the backend's Worktree only when the directory exists and
// `git -C <path> branch --show-current` reports exactly the requested branch. On any failure it returns a zero
// Worktree and an error; it never prints or returns an unverified path, and never falls back to the epic checkout.
func Ensure(b backend.Backend, repo, branch, base string) (backend.Worktree, error) {
	wt, err := b.WorktreeCreate(repo, branch, base)
	if err != nil {
		return backend.Worktree{}, fmt.Errorf("worktree create for %s: %w", repo, err)
	}
	if wt.Path == "" {
		return backend.Worktree{}, fmt.Errorf("worktree create for %s returned no path", repo)
	}
	info, err := os.Stat(wt.Path)
	if err != nil || !info.IsDir() {
		return backend.Worktree{}, fmt.Errorf("worktree path for %s does not exist as a directory", repo)
	}
	got, err := currentBranch(wt.Path)
	if err != nil {
		return backend.Worktree{}, fmt.Errorf("verify branch for %s: %w", repo, err)
	}
	if got != branch {
		// A typed error so a caller can tell a branch rename (Orca prefixes the git username onto an already
		// checked-out branch) from a create failure, and clean up the worktree it names, while this package stays the
		// single owner of the isolation verification (F02). Still a zero Worktree; nothing here deletes a branch (F01).
		return backend.Worktree{}, &BranchMismatchError{Path: wt.Path, Got: got, Want: branch}
	}
	return backend.Worktree{Path: wt.Path, Branch: branch}, nil
}

// BranchMismatchError is returned by Ensure when the created worktree is on a branch other than the one requested. Path
// is the created checkout (so a caller can remove it), Got the branch it landed on, Want the branch that was requested.
type BranchMismatchError struct {
	Path string
	Got  string
	Want string
}

func (e *BranchMismatchError) Error() string {
	return fmt.Sprintf("worktree %s is on branch %q, expected %q", e.Path, e.Got, e.Want)
}

// currentBranch returns the checked-out branch of the git worktree at path.
func currentBranch(path string) (string, error) {
	out, err := boundexec.Git("-C", path, "branch", "--show-current")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}
