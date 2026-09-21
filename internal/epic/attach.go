package epic

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/nphattai/coxswain/internal/adapter/backend"
	"github.com/nphattai/coxswain/internal/workspace"
	"github.com/nphattai/coxswain/internal/worktree"
)

// AttachOptions configures Attach.
type AttachOptions struct {
	Runtime   backend.Backend      // creates the worktrees
	Workspace *workspace.Workspace // repo registry (for the canonical ref per alias)
	WsRoot    string               // workspace root (for trust)
	EpicDir   string               // the existing epic dir (has DESIGN.md, repos, stories, but no .cox)
	Warn      io.Writer            // best-effort warnings; nil => os.Stderr
}

func (o *AttachOptions) warn() io.Writer {
	if o.Warn != nil {
		return o.Warn
	}
	return os.Stderr
}

// Attach re-attaches an epic dir that has DESIGN.md, its repos file and stories but no .cox/ (a fresh clone, a new
// machine, or after .cox was discarded): it recreates .cox/epic.json, the worktrees on the EXISTING epic/<slug> branch
// (fetched, never recreated - the branch set is unchanged), the alias symlinks and the trust entries. It refuses if a
// .cox/ already exists or if a worktree that is still present is dirty (uncommitted work that a re-checkout would risk).
func Attach(o AttachOptions) error {
	slug := filepath.Base(o.EpicDir)
	branch := "epic/" + slug
	if !pathExists(filepath.Join(o.EpicDir, "DESIGN.md")) || !pathExists(filepath.Join(o.EpicDir, "repos")) {
		return fmt.Errorf("%s is not an epic dir (no DESIGN.md/repos); use cox epic new to create one", o.EpicDir)
	}
	if pathExists(filepath.Join(o.EpicDir, ".cox")) {
		return fmt.Errorf("epic %s already has a .cox/ (already attached); remove it first to force a re-attach", o.EpicDir)
	}
	project := filepath.Base(filepath.Dir(filepath.Dir(o.EpicDir)))

	repos, err := readEpicRepos(o.EpicDir)
	if err != nil {
		return err
	}
	if len(repos) == 0 {
		return fmt.Errorf("epic %s has no repos to attach", o.EpicDir)
	}

	// Refuse before touching anything if a still-present worktree is dirty.
	for _, r := range repos {
		link := filepath.Join(o.EpicDir, r.alias)
		if target, err := filepath.EvalSymlinks(link); err == nil {
			if dirty, _ := worktreeDirty(target); dirty {
				return fmt.Errorf("worktree %s (%s) is dirty; commit or discard its changes before re-attaching", r.alias, target)
			}
		}
	}

	var aliases []string
	for _, r := range repos {
		aliases = append(aliases, r.alias)
		// Resolve the ref from the LOCAL workspace.json, not the ref recorded in the tracked repos file: a fresh clone on
		// another machine has a different checkout path, so the tracked absolute path is stale (this is exactly the
		// captain's reinstall test). Fall back to the file ref only for an alias the local workspace does not register.
		ref := r.ref
		if o.Workspace != nil {
			if repo, ok := o.Workspace.Repo(r.alias); ok {
				ref = repo.Ref()
			}
		}

		link := filepath.Join(o.EpicDir, r.alias)
		// (item 5) Adopt a worktree that is ALREADY on the epic branch instead of asking the backend for another one.
		// Look it up by branch in the alias checkout's worktree list, not only through the alias symlink, so a checkout
		// that exists without a symlink is still found (B-40): asking the backend for a second worktree on an already
		// checked-out branch is refused by git and, on Orca, produces a renamed <user>/epic-<slug> branch and a duplicate
		// worktree. A clean match is adopted (symlink written, trusted); a dirty match refuses rather than risk its work.
		if strings.HasPrefix(ref, "/") {
			if existing, found := worktreeOnBranch(ref, branch); found {
				if dirty, _ := worktreeDirty(existing); dirty {
					return fmt.Errorf("worktree %s on %s is dirty; commit or discard its changes before re-attaching", existing, branch)
				}
				fmt.Fprintf(o.warn(), "attach: adopting existing worktree %s\n", existing)
				_ = os.Remove(link)
				if err := os.Symlink(existing, link); err != nil {
					return fmt.Errorf("symlink %s -> %s: %w", r.alias, existing, err)
				}
				trustWorktree(o.warn(), existing)
				continue
			}
		}

		// On a true fresh clone epic/<slug> exists only on origin: git fetch brings origin/<slug> but not the local
		// refs/heads/<slug>, so basing the worktree on the (missing) local branch fails. Fetch the epic branch, then base
		// the worktree on the local branch when it exists (never reset it) or on the fetched origin/<branch> when it does
		// not (git creates the local tracking branch from it; nothing is deleted). Only a path checkout can be inspected;
		// a name ref is left to the backend.
		base := branch
		if strings.HasPrefix(ref, "/") {
			if out, err := exec.Command("git", "-C", ref, "fetch", "origin", branch).CombinedOutput(); err != nil {
				fmt.Fprintf(o.warn(), "warn: fetch %s for %s: %v: %s\n", branch, r.alias, err, strings.TrimSpace(string(out)))
			}
			if !localBranchExists(ref, branch) {
				base = "origin/" + branch
			}
		}
		// worktree.Ensure owns the isolation verification (F02): it creates the worktree and proves it is on the requested
		// branch. (item 5) Orca prefixes the git username onto an already checked-out branch (epic/<slug> ->
		// <user>/epic-<slug>), which Ensure reports as a BranchMismatchError; that renamed branch is not the epic branch,
		// so remove the worktree again and refuse, deleting the renamed LOCAL branch only when it is safe (no unique
		// commits, not on origin - never a branch that carries work, F01/B-40).
		wt, err := worktree.Ensure(o.Runtime, ref, branch, base)
		if err != nil {
			var bm *worktree.BranchMismatchError
			if errors.As(err, &bm) {
				_ = o.Runtime.WorktreeRemove(backend.Worktree{Path: bm.Path, Branch: bm.Got, Force: true})
				removeRenamedBranch(o.warn(), ref, bm.Got, branch)
				return fmt.Errorf("attach: backend created %s on renamed branch %q, not %q; removed it (the epic branch is likely checked out elsewhere, or this repo is not yours - B-40)", r.alias, bm.Got, branch)
			}
			return fmt.Errorf("attach worktree for %s: %w", r.alias, err)
		}
		_ = os.Remove(link)
		if err := os.Symlink(wt.Path, link); err != nil {
			return fmt.Errorf("symlink %s -> %s: %w", r.alias, wt.Path, err)
		}
		fmt.Fprintf(o.warn(), "attach: marking %s trusted in ~/.claude.json\n", wt.Path)
		trustWorktree(o.warn(), wt.Path)
	}

	if err := os.MkdirAll(filepath.Join(o.EpicDir, ".cox"), 0o755); err != nil {
		return err
	}
	meta := EpicMeta{Slug: slug, Project: project, Repos: aliases, CreatedAt: time.Now().UTC().Format(time.RFC3339)}
	mb, _ := json.MarshalIndent(meta, "", "  ")
	if err := os.WriteFile(filepath.Join(o.EpicDir, ".cox", "epic.json"), append(mb, '\n'), 0o644); err != nil {
		return err
	}
	return nil
}

// epicRepo is one line of an epic's repos file: an alias and the ref (name or absolute path) it addresses.
type epicRepo struct {
	alias string
	ref   string
}

// readEpicRepos parses <epic>/repos ("<alias> <ref>" per line). The ref is the canonical value cox epic new wrote
// (Repo.Ref(): a name or an absolute path), which is exactly what worktree creation addresses.
func readEpicRepos(epicDir string) ([]epicRepo, error) {
	b, err := os.ReadFile(filepath.Join(epicDir, "repos"))
	if err != nil {
		return nil, fmt.Errorf("read epic repos file: %w", err)
	}
	var out []epicRepo
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) == 0 {
			continue
		}
		r := epicRepo{alias: f[0]}
		if len(f) > 1 {
			r.ref = f[1]
		}
		out = append(out, r)
	}
	return out, nil
}

// worktreeOnBranch returns the path of a worktree in repoPath's worktree list that is checked out on branch, and
// whether one was found. It reads `git -C <repoPath> worktree list --porcelain`, whose blocks carry a `worktree
// <path>` line and a `branch refs/heads/<name>` line (absent when detached). This finds a checkout on the branch even
// when no alias symlink points at it (B-40).
func worktreeOnBranch(repoPath, branch string) (string, bool) {
	out, err := exec.Command("git", "-C", repoPath, "worktree", "list", "--porcelain").Output()
	if err != nil {
		return "", false
	}
	want := "refs/heads/" + branch
	path := ""
	for _, line := range strings.Split(string(out), "\n") {
		switch {
		case strings.HasPrefix(line, "worktree "):
			path = strings.TrimSpace(strings.TrimPrefix(line, "worktree "))
		case line == "branch "+want && path != "":
			return path, true
		}
	}
	return "", false
}

// removeRenamedBranch deletes a renamed local branch left behind by a backend rename (B-40), but only when it is safe:
// the branch must NOT be on origin and must have NO commits beyond origin/<canonical> (no unique work). This never
// deletes a branch that carries work or exists on origin (F01). A name ref (no path to run git in) is a no-op.
func removeRenamedBranch(warn io.Writer, ref, renamed, canonical string) {
	if !strings.HasPrefix(ref, "/") || renamed == "" || renamed == canonical {
		return
	}
	if out, err := exec.Command("git", "-C", ref, "ls-remote", "--heads", "origin", renamed).Output(); err != nil || strings.TrimSpace(string(out)) != "" {
		return // on origin (or cannot tell) -> keep it
	}
	if out, err := exec.Command("git", "-C", ref, "rev-list", "origin/"+canonical+".."+renamed).Output(); err != nil || strings.TrimSpace(string(out)) != "" {
		return // has commits beyond origin/<canonical> (or cannot tell) -> keep it
	}
	if out, err := exec.Command("git", "-C", ref, "branch", "-D", renamed).CombinedOutput(); err != nil {
		fmt.Fprintf(warn, "attach: left renamed branch %s in place (could not delete: %s)\n", renamed, strings.TrimSpace(string(out)))
		return
	}
	fmt.Fprintf(warn, "attach: deleted renamed local branch %s (no unique commits, not on origin)\n", renamed)
}

// localBranchExists reports whether refs/heads/<branch> exists in the checkout at repoPath.
func localBranchExists(repoPath, branch string) bool {
	return exec.Command("git", "-C", repoPath, "show-ref", "--verify", "--quiet", "refs/heads/"+branch).Run() == nil
}

// worktreeBranch returns the checked-out branch at path, or an error when path is not a readable git worktree.
func worktreeBranch(path string) (string, error) {
	out, err := exec.Command("git", "-C", path, "branch", "--show-current").Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// worktreeDirty reports whether a git worktree has uncommitted changes (staged, unstaged, or untracked).
func worktreeDirty(path string) (bool, error) {
	out, err := exec.Command("git", "-C", path, "status", "--porcelain").Output()
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(string(out)) != "", nil
}

func pathExists(p string) bool { _, err := os.Stat(p); return err == nil }
