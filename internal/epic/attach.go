package epic

import (
	"encoding/json"
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

		// Fetch the existing epic branch so a fresh clone has it locally; best-effort for a path checkout.
		if strings.HasPrefix(ref, "/") {
			if out, err := exec.Command("git", "-C", ref, "fetch", "origin", branch).CombinedOutput(); err != nil {
				fmt.Fprintf(o.warn(), "warn: fetch %s for %s: %v: %s\n", branch, r.alias, err, strings.TrimSpace(string(out)))
			}
		}
		// Check out the EXISTING branch at its own tip (base = the branch itself, so it is never reset to production and
		// no branch is created or deleted).
		wt, err := worktree.Ensure(o.Runtime, ref, branch, branch)
		if err != nil {
			return fmt.Errorf("attach worktree for %s: %w", r.alias, err)
		}
		link := filepath.Join(o.EpicDir, r.alias)
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

// worktreeDirty reports whether a git worktree has uncommitted changes (staged, unstaged, or untracked).
func worktreeDirty(path string) (bool, error) {
	out, err := exec.Command("git", "-C", path, "status", "--porcelain").Output()
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(string(out)) != "", nil
}

func pathExists(p string) bool { _, err := os.Stat(p); return err == nil }
