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
	"github.com/nphattai/coxswain/internal/env"
)

// CloseOptions configures Close.
type CloseOptions struct {
	EpicDir     string
	Runtime     backend.Backend // stops workers, removes worktrees (may be nil in dry-run)
	Alloc       *env.Allocator  // stops the backend, releases resources
	Yes         bool            // execute; default is a dry run that only prints the plan
	Force       bool            // remove a dirty/unpushed worktree (default: KEEP it, F01/close safety)
	StoriesOnly bool            // skip the epic backend/services and epic worktrees
	Out         io.Writer       // plan/progress output; nil => os.Stdout
}

func (o *CloseOptions) out() io.Writer {
	if o.Out != nil {
		return o.Out
	}
	return os.Stdout
}

// closeIncomplete is written to .cox/close.incomplete.json when a required step fails; the epic is NOT archived.
type closeIncomplete struct {
	Step   string `json:"step"`
	Reason string `json:"reason"`
	At     string `json:"at"`
}

// Close tears an epic down in a fixed, confirmed order: (1) stop every worker and confirm it settled, (2) stop the epic
// backend, (3) release each story's resources (only clearing ownership on a confirmed delete), (4) detach and remove
// each worktree (keeping the branch; a dirty or unpushed worktree is KEPT unless Force), and only when all four
// succeed (5) archive .cox -> .cox.closed. A failure at any step writes .cox/close.incomplete.json and returns an
// error without archiving (F03: never archive over an incomplete teardown). Without Yes it prints the plan and stops.
func Close(o CloseOptions) error {
	if !o.Yes {
		return o.dryRun()
	}
	steps := []struct {
		name string
		fn   func() error
	}{
		{"stop-workers", o.stopWorkers},
		{"stop-backend", o.stopBackend},
		{"release-resources", o.releaseResources},
		{"remove-worktrees", o.removeWorktrees},
	}
	for _, s := range steps {
		if err := s.fn(); err != nil {
			o.recordIncomplete(s.name, err.Error())
			return fmt.Errorf("close aborted at %s (not archived): %w", s.name, err)
		}
		fmt.Fprintf(o.out(), "ok: %s\n", s.name)
	}
	// Stop the epic's watcher before archiving: a live watcher recreates .cox/ after it is moved (finding 13). A live
	// pid that cannot be proven to be this epic's watcher aborts close (never archived) rather than being signalled blind.
	if err := o.stopWatcher(); err != nil {
		o.recordIncomplete("stop-watcher", err.Error())
		return fmt.Errorf("close aborted at stop-watcher (not archived): %w", err)
	}
	// (5) archive only after every step confirmed.
	closed := filepath.Join(o.EpicDir, ".cox.closed")
	if err := os.RemoveAll(closed); err != nil {
		o.recordIncomplete("archive", err.Error())
		return err
	}
	if err := os.Rename(filepath.Join(o.EpicDir, ".cox"), closed); err != nil {
		o.recordIncomplete("archive", err.Error())
		return err
	}
	fmt.Fprintf(o.out(), "ok: archived .cox -> .cox.closed\n")
	return nil
}

func (o *CloseOptions) dryRun() error {
	w := o.out()
	fmt.Fprintf(w, "close plan for %s (dry run; pass --yes to execute):\n", filepath.Base(o.EpicDir))
	fmt.Fprintln(w, "  1. stop every worker and confirm settled")
	if !o.StoriesOnly {
		fmt.Fprintln(w, "  2. stop the epic backend (owned pid only)")
	}
	fmt.Fprintln(w, "  3. release each story's db/sim/env (ownership cleared only on confirmed delete)")
	fmt.Fprintf(w, "  4. detach + remove each worktree (branch kept; dirty/unpushed KEPT unless --force=%v)\n", o.Force)
	fmt.Fprintln(w, "  5. stop this epic's watcher (refuses to archive while a provable watcher is alive)")
	fmt.Fprintln(w, "  6. archive .cox -> .cox.closed (only if 1-5 all succeed)")
	return nil
}

// stopWorkers stops every recorded worker session and confirms it settled. An unconfirmed stop or a still-alive probe
// is a failure: the worker must be down before its worktree is removed (F03).
func (o *CloseOptions) stopWorkers() error {
	sessions := o.sessions()
	if o.Runtime == nil {
		if len(sessions) == 0 {
			return nil
		}
		return fmt.Errorf("no backend to stop %d worker(s)", len(sessions))
	}
	for story, sess := range sessions {
		confirmed, err := o.Runtime.Stop(sess)
		if err != nil {
			// Stop can error even though the worker has actually settled (e.g. Orca closed the terminal
			// itself, so a follow-up stop reports "Terminal closed by operator request"). Trust a Settled
			// probe over the Stop error rather than aborting close over a worker that is already down.
			live, perr := o.Runtime.Probe(sess)
			if perr != nil || live != backend.Settled {
				return fmt.Errorf("stop worker %s: %w", story, err)
			}
			fmt.Fprintf(o.out(), "  note: stop worker %s errored (%v) but probe settled; treating as stopped\n", story, err)
			continue
		}
		if confirmed {
			continue
		}
		live, perr := o.Runtime.Probe(sess)
		if perr != nil || live != backend.Settled {
			return fmt.Errorf("worker %s not confirmed settled (probe=%v, err=%v)", story, live, perr)
		}
	}
	return nil
}

func (o *CloseOptions) stopBackend() error {
	if o.StoriesOnly || o.Alloc == nil {
		return nil
	}
	if _, err := o.Alloc.StopBackend(); err != nil {
		return err
	}
	return nil
}

func (o *CloseOptions) releaseResources() error {
	if o.Alloc == nil {
		return nil
	}
	for _, story := range o.storiesWithResources() {
		if err := o.Alloc.Release(story); err != nil {
			return err // ownership kept by Release; close stops here
		}
	}
	return nil
}

// removeWorktrees detaches and removes each worktree, keeping the branch. A dirty or unpushed worktree is kept (a
// warning, not a failure) unless Force, so unlanded work is never destroyed.
func (o *CloseOptions) removeWorktrees() error {
	if o.Runtime == nil {
		return nil
	}
	for _, wt := range o.worktrees() {
		if !o.Force {
			if reason := dirtyOrUnpushed(wt.Path); reason != "" {
				fmt.Fprintf(o.out(), "  keep %s (%s): pass --force to remove\n", wt.Path, reason)
				continue
			}
		}
		if err := o.Runtime.WorktreeRemove(backend.Worktree{Path: wt.Path}); err != nil {
			return fmt.Errorf("remove worktree %s: %w", wt.Path, err)
		}
		if wt.Link != "" {
			_ = os.Remove(wt.Link)
		}
		fmt.Fprintf(o.out(), "  removed %s (branch kept)\n", wt.Path)
	}
	return nil
}

func (o *CloseOptions) recordIncomplete(step, reason string) {
	rec := closeIncomplete{Step: step, Reason: reason, At: time.Now().UTC().Format(time.RFC3339)}
	b, _ := json.MarshalIndent(rec, "", "  ")
	_ = os.WriteFile(filepath.Join(o.EpicDir, ".cox", "close.incomplete.json"), append(b, '\n'), 0o644)
	fmt.Fprintf(o.out(), "FAILED at %s: %s (wrote .cox/close.incomplete.json, NOT archived)\n", step, reason)
}

// sessions loads .cox/sessions/*.json.
func (o *CloseOptions) sessions() map[string]backend.Session {
	out := map[string]backend.Session{}
	dir := filepath.Join(o.EpicDir, ".cox", "sessions")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return out
	}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		var s backend.Session
		if json.Unmarshal(b, &s) == nil {
			out[strings.TrimSuffix(e.Name(), ".json")] = s
		}
	}
	return out
}

// storiesWithResources reads .cox/resources.json for the story ids that own resources.
func (o *CloseOptions) storiesWithResources() []string {
	b, err := os.ReadFile(filepath.Join(o.EpicDir, ".cox", "resources.json"))
	if err != nil {
		return nil
	}
	var r struct {
		Stories map[string]any `json:"stories"`
	}
	if json.Unmarshal(b, &r) != nil {
		return nil
	}
	var out []string
	for id := range r.Stories {
		out = append(out, id)
	}
	return out
}

type worktreeRef struct {
	Path string
	Link string // the alias symlink to remove, if any
}

// worktrees gathers every worktree to remove: the per-story worktrees under .cox/wt/* and the epic alias symlinks named
// in the repos file. Paths are de-duplicated.
func (o *CloseOptions) worktrees() []worktreeRef {
	seen := map[string]bool{}
	var out []worktreeRef
	// Story worktrees.
	wtDir := filepath.Join(o.EpicDir, ".cox", "wt")
	if entries, err := os.ReadDir(wtDir); err == nil {
		for _, e := range entries {
			b, err := os.ReadFile(filepath.Join(wtDir, e.Name()))
			if err != nil {
				continue
			}
			p := strings.TrimSpace(string(b))
			if p != "" && !seen[p] {
				seen[p] = true
				out = append(out, worktreeRef{Path: p})
			}
		}
	}
	// Epic worktrees via alias symlinks.
	if !o.StoriesOnly {
		if b, err := os.ReadFile(filepath.Join(o.EpicDir, "repos")); err == nil {
			for _, line := range strings.Split(string(b), "\n") {
				f := strings.Fields(line)
				if len(f) < 1 {
					continue
				}
				link := filepath.Join(o.EpicDir, f[0])
				target, err := filepath.EvalSymlinks(link)
				if err != nil || seen[target] {
					continue
				}
				seen[target] = true
				out = append(out, worktreeRef{Path: target, Link: link})
			}
		}
	}
	return out
}

// dirtyOrUnpushed returns a non-empty reason when the worktree has uncommitted changes or commits not on its upstream.
func dirtyOrUnpushed(path string) string {
	if out, err := exec.Command("git", "-C", path, "status", "--porcelain").Output(); err == nil && strings.TrimSpace(string(out)) != "" {
		return "dirty"
	}
	branch, err := exec.Command("git", "-C", path, "branch", "--show-current").Output()
	if err != nil {
		return ""
	}
	b := strings.TrimSpace(string(branch))
	if b == "" {
		return "" // detached; nothing to compare
	}
	// No upstream => unpublished work; treat as unpushed.
	if err := exec.Command("git", "-C", path, "rev-parse", "--abbrev-ref", b+"@{u}").Run(); err != nil {
		return "unpushed (no upstream)"
	}
	if out, err := exec.Command("git", "-C", path, "rev-list", b+"@{u}.."+b).Output(); err == nil && strings.TrimSpace(string(out)) != "" {
		return "unpushed"
	}
	return ""
}
