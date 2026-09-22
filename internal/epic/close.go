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
	"github.com/nphattai/coxswain/internal/state"
)

// CloseOptions configures Close.
type CloseOptions struct {
	EpicDir     string
	Runtime     backend.Backend // stops workers, removes worktrees (may be nil in dry-run)
	Alloc       *env.Allocator  // stops the backend, releases resources
	Yes         bool            // execute; default is a dry run that only prints the plan
	Force       bool            // remove an unlanded worktree anyway (default: KEEP it, F01/close safety)
	StoriesOnly bool            // skip the epic backend/services and epic worktrees
	Out         io.Writer       // plan/progress output; nil => os.Stdout
	// Captain bypasses the leader-terminal guard: close is refused from a terminal that is not the epic's recorded
	// leader (item 4d) unless the captain runs it. TerminalHandle is this terminal's handle (ORCA_TERMINAL_HANDLE),
	// injected by the command layer so the guard is testable; empty means "cannot tell", which never refuses.
	Captain        bool
	TerminalHandle string
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
	// (d) Only the epic's leader terminal (or the captain) may close it. The guard fires only when it can prove a
	// mismatch - both a recorded leader handle and this terminal's handle are known and differ - so a close from a
	// machine that records neither is never blocked (B-38 v1 dirs have no .cox/leader at all).
	if !o.Captain {
		if owner := o.leaderHandle(); owner != "" && o.TerminalHandle != "" && owner != o.TerminalHandle {
			return fmt.Errorf("cox epic close: this terminal (%s) is not the leader of %s (owned by %s); rerun from the leader terminal or pass --captain",
				o.TerminalHandle, filepath.Base(o.EpicDir), owner)
		}
	}
	// (a) A v1-migrated / never-attached epic has no .cox/ runtime: there is nothing to stop or remove, so steps 1-5
	// are vacuous and step 6 writes the .cox.closed marker (B-38: the old code failed at the archive rename because
	// .cox did not exist, and could not even record the failure).
	if !pathExists(filepath.Join(o.EpicDir, ".cox")) {
		return o.closeNoRuntime()
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

// closeNoRuntime handles an epic with no .cox/ runtime (B-38): steps 1-5 have nothing to act on, so it prints them as
// vacuous and writes the .cox.closed marker with closed.json noting no_runtime, so the epic is archived rather than
// left in a half-state the old code could not even record.
func (o *CloseOptions) closeNoRuntime() error {
	for _, step := range []string{"stop-workers", "stop-backend", "release-resources", "remove-worktrees", "stop-watcher"} {
		fmt.Fprintf(o.out(), "ok: %s (no runtime)\n", step)
	}
	closed := filepath.Join(o.EpicDir, ".cox.closed")
	if err := os.MkdirAll(closed, 0o755); err != nil {
		return err
	}
	rec := struct {
		NoRuntime bool   `json:"no_runtime"`
		At        string `json:"at"`
	}{NoRuntime: true, At: time.Now().UTC().Format(time.RFC3339)}
	b, _ := json.MarshalIndent(rec, "", "  ")
	if err := os.WriteFile(filepath.Join(closed, "closed.json"), append(b, '\n'), 0o644); err != nil {
		return err
	}
	fmt.Fprintf(o.out(), "ok: archived (no runtime) -> .cox.closed\n")
	return nil
}

// leaderHandle reads the epic's recorded leader terminal handle from .cox/leader, or "" when unset/unreadable. It routes
// through the single state reader, which accepts both the JSON leader record and the legacy plain handle (DESIGN wave-2
// item 7), so the close guard reads the record the same way every other consumer does.
func (o *CloseOptions) leaderHandle() string {
	return state.LeaderHandle(o.EpicDir)
}

func (o *CloseOptions) dryRun() error {
	w := o.out()
	fmt.Fprintf(w, "close plan for %s (dry run; pass --yes to execute):\n", filepath.Base(o.EpicDir))
	fmt.Fprintln(w, "  1. stop every worker and confirm settled")
	if !o.StoriesOnly {
		fmt.Fprintln(w, "  2. stop the epic backend (owned pid only)")
	}
	fmt.Fprintln(w, "  3. release each story's db/sim/env (ownership cleared only on confirmed delete)")
	fmt.Fprintf(w, "  4. detach + remove each worktree (branch kept; unlanded work KEPT unless --force=%v)\n", o.Force)
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

// removeWorktrees detaches and removes each worktree, keeping the branch. A worktree whose work has not landed (dirty
// tracked files, or a branch not contained in origin/production) is kept - a warning, not a failure - unless Force, so
// unlanded work is never destroyed. After a removal it re-reads the repo's worktree list and fails the step when the
// path is still registered, so a backend that reports success without actually removing (B-39) is caught rather than
// archived over.
func (o *CloseOptions) removeWorktrees() error {
	if o.Runtime == nil {
		return nil
	}
	for _, wt := range o.worktrees() {
		if !o.Force {
			if ok, reason := o.landed(wt.Path); !ok {
				fmt.Fprintf(o.out(), "  keep %s (%s): pass --force to remove\n", wt.Path, reason)
				continue
			}
		}
		branch, _ := worktreeBranch(wt.Path) // "" when detached; the adapter guard has nothing to protect then
		common := gitCommonDir(wt.Path)      // captured before removal; the checkout path is gone afterwards
		// Reaching here means removal is authorized - either landed() proved containment or the operator passed --force.
		// Pass Force: true so the adapter's B-16 origin guard, which cannot see close's richer landed() check, does not
		// refuse a branch that is landed in production but no longer on origin (the squash-merge-then-delete flow).
		if err := o.Runtime.WorktreeRemove(backend.Worktree{Path: wt.Path, Branch: branch, Force: true}); err != nil {
			return fmt.Errorf("remove worktree %s: %w", wt.Path, err)
		}
		// (b) prove the worktree is actually gone; a remove that returned ok but left the path registered is a failure.
		if common != "" && worktreeRegistered(common, wt.Path) {
			return fmt.Errorf("%s still registered", wt.Path)
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

// landed reports whether a worktree's work is safely landed, so its checkout can be removed without losing anything,
// and a short reason when it is not (printed as "keep <path> (<reason>)"). It replaces dirtyOrUnpushed (B-21, B-39):
//   - Uncommitted TRACKED changes, or an untracked file that is NOT under a backend-owned path, => not landed. A
//     backend-owned untracked artifact (Orca's .orca/ screenshot drops) is ignored, so it never makes a clean
//     worktree look dirty.
//   - A detached worktree has no branch to strand => landed.
//   - Otherwise the branch is landed when its tip is contained in origin/<branch> (fetched first) or in a production
//     branch. A branch with no upstream but contained in origin/<branch> IS landed (B-21). A fetch that cannot resolve
//     origin/<branch> leaves it not-landed with the reason, so uncertainty keeps the worktree rather than removing it.
func (o *CloseOptions) landed(path string) (bool, string) {
	if reason := dirtyReason(path, ownedPaths(o.Runtime)); reason != "" {
		return false, reason
	}
	b, _ := worktreeBranch(path)
	if b == "" {
		return true, "detached" // nothing to compare
	}
	// Refresh origin/<b> so containment is judged against the current remote. A failed fetch (no origin, or the branch
	// was deleted on origin after a squash/merge) does NOT by itself mean unlanded: it only makes the origin/<b> path
	// inconclusive, so we still check the production branches below. It is recorded and surfaced only when nothing
	// proves containment (the "unknown fetch => not landed with the reason" case).
	fetchErr := ""
	if hasOrigin(path) {
		if out, err := exec.Command("git", "-C", path, "fetch", "origin", b).CombinedOutput(); err != nil {
			fetchErr = strings.TrimSpace(string(out))
		}
	}
	// Landed when the branch tip is contained in origin/<b> (when we have it) or in a production branch: a branch merged
	// into main and deleted on origin is landed even though it is no longer on origin (B-21 and the squash-merge flow).
	for _, ref := range append([]string{"origin/" + b}, productionRefs(path)...) {
		if isAncestor(path, b, ref) {
			return true, ""
		}
	}
	if fetchErr != "" {
		return false, fmt.Sprintf("not landed; fetch origin/%s failed: %s", b, fetchErr)
	}
	return false, "not landed (no containment in origin or production)"
}

// dirtyReason returns "dirty" for uncommitted tracked changes or an untracked path outside owned, else "". Owned
// prefixes (e.g. ".orca/") are backend-scratch and ignored, so a screenshot drop never blocks close (B-39).
func dirtyReason(path string, owned []string) string {
	out, err := exec.Command("git", "-C", path, "status", "--porcelain").Output()
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
		if line == "" {
			continue
		}
		// Porcelain v1: XY<space>path. Untracked entries are "?? path"; everything else is a tracked change.
		if strings.HasPrefix(line, "?? ") {
			p := strings.TrimPrefix(line, "?? ")
			if isOwnedPath(p, owned) {
				continue
			}
		}
		return "dirty"
	}
	return ""
}

// isOwnedPath reports whether an untracked path (as git prints it, worktree-relative) sits under a backend-owned prefix.
func isOwnedPath(p string, owned []string) bool {
	p = strings.TrimPrefix(strings.TrimSpace(p), "./")
	for _, prefix := range owned {
		if prefix != "" && strings.HasPrefix(p, prefix) {
			return true
		}
	}
	return false
}

// ownedPather is the optional capability a backend implements to declare the worktree-relative path prefixes it owns
// (its scratch/artifact dirs). close consults it so the ignore list comes from the backend's capability card rather
// than a constant; a backend that does not implement it falls back to the Orca default (.orca/).
type ownedPather interface{ OwnedPaths() []string }

func ownedPaths(rt backend.Backend) []string {
	if op, ok := rt.(ownedPather); ok {
		if p := op.OwnedPaths(); len(p) > 0 {
			return p
		}
	}
	return []string{".orca/"}
}

// hasOrigin reports whether the checkout has an `origin` remote configured.
func hasOrigin(path string) bool {
	return exec.Command("git", "-C", path, "remote", "get-url", "origin").Run() == nil
}

// isAncestor reports whether branch's tip is contained in ref (ref exists and branch is an ancestor of it).
func isAncestor(path, branch, ref string) bool {
	return exec.Command("git", "-C", path, "merge-base", "--is-ancestor", branch, ref).Run() == nil
}

// productionRefs lists the refs a landed branch may have merged into: origin/HEAD's target when set, then the common
// main/master names locally and on origin. Nonexistent refs are harmless - isAncestor just returns false for them.
func productionRefs(path string) []string {
	var refs []string
	if out, err := exec.Command("git", "-C", path, "symbolic-ref", "--quiet", "refs/remotes/origin/HEAD").Output(); err == nil {
		if r := strings.TrimPrefix(strings.TrimSpace(string(out)), "refs/remotes/"); r != "" {
			refs = append(refs, r)
		}
	}
	return append(refs, "origin/main", "origin/master", "main", "master")
}

// gitCommonDir returns the absolute shared git dir for the worktree at path (the main repo's .git), or "" on error.
// It is read before a worktree is removed so worktreeRegistered can query the repo once the checkout path is gone.
func gitCommonDir(path string) string {
	out, err := exec.Command("git", "-C", path, "rev-parse", "--path-format=absolute", "--git-common-dir").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// worktreeRegistered reports whether path is still listed as a worktree of the repo whose git dir is common. It is the
// post-remove proof: a backend that returned ok without actually detaching the worktree leaves it listed here (B-39).
func worktreeRegistered(commonDir, path string) bool {
	out, err := exec.Command("git", "--git-dir="+commonDir, "worktree", "list", "--porcelain").Output()
	if err != nil {
		return false
	}
	want := resolvePath(path)
	for _, line := range strings.Split(string(out), "\n") {
		if p, ok := strings.CutPrefix(line, "worktree "); ok {
			if resolvePath(strings.TrimSpace(p)) == want {
				return true
			}
		}
	}
	return false
}

// resolvePath canonicalizes a path for comparison, resolving symlinks when it still exists (git prints resolved paths;
// /tmp is a symlink to /private/tmp on macOS) and falling back to a clean of the original when it does not.
func resolvePath(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return filepath.Clean(p)
}
