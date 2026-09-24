package watch

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/nphattai/coxswain/internal/adapter/backend"
	"github.com/nphattai/coxswain/internal/protocol/busy"
	"github.com/nphattai/coxswain/internal/state"
)

// Crew state: the cox reading of firstmate's crew_absorb_class / crew_is_provably_working (bin/fm-classify-lib.sh over
// bin/fm-crew-state.sh, pinned 1e0e773). Firstmate absorbs a benign wake only on POSITIVE evidence the crew is still
// executing: an actively running no-mistakes step, or a busy pane. The cox observables are, in order:
//
//   - run-step: the story PR has CI checks running at its live head (captain ruling 2026-09-24), read through the forge
//     adapter with a three-state result - a forge error is unknown, and unknown never absorbs;
//   - pane: the harness-owned busy record says busy (ADR 0016), unless a probe proves the session ended (B-51) or the
//     record is still the dispatch seed while the backend reports the agent idle at an empty prompt (no harness hook has
//     ever run, so the seed is not evidence - the kill-test path).
//
// Anything else is none: the crew stopped, finished, is parked, or cannot be read, so the wake surfaces.

const (
	crewWorking = "working"
	crewNone    = "none"
)

// ciRunning reports whether the story's PR has a CI check queued or in progress at its head: yes, no, or unknown (the
// forge could not be read, or no forge is configured). The PR is resolved from the story branch story/<id>; the read is
// cached for the tick (two forge calls).
func (w *Watcher) ciRunning(story string) (running, known bool) {
	if r, ok := w.ci[story]; ok {
		return r.running, r.known
	}
	running, known = w.readCI(story)
	if w.ci == nil {
		w.ci = map[string]ciResult{}
	}
	w.ci[story] = ciResult{running, known}
	return running, known
}

type ciResult struct{ running, known bool }

func (w *Watcher) readCI(story string) (running, known bool) {
	if w.Forge == nil {
		return false, false
	}
	pr, err := w.Forge.PR("story/" + story)
	if err != nil {
		return false, false
	}
	checks, err := w.Forge.Checks(pr)
	if err != nil {
		return false, false
	}
	for _, c := range checks {
		if c.Status != "completed" {
			return true, true
		}
	}
	return false, true
}

// probe reads the story session's liveness once per tick (the backend read is a CLI call on Orca).
func (w *Watcher) probe(story string) (backend.Liveness, error) {
	if p, ok := w.probes[story]; ok {
		return p.live, p.err
	}
	sess, ok := w.Sessions[story]
	if !ok {
		return backend.Unknown, errors.New("no session")
	}
	live, err := w.Backend.Probe(sess)
	if w.probes == nil {
		w.probes = map[string]probeResult{}
	}
	w.probes[story] = probeResult{live, err}
	return live, err
}

type probeResult struct {
	live backend.Liveness
	err  error
}

// busyNow is firstmate's busy-pane verdict: a trusted busy record that says busy and that nothing contradicts. With no
// record at all (a harness without one) the backend's own composer is the only busy signal.
func (w *Watcher) busyNow(story string) bool {
	sess, hasSess := w.Sessions[story]
	switch busy.Read(w.EpicDir, story) {
	case busy.Busy:
	case busy.Unknown:
		if busyRecordPresent(w.EpicDir, story) || !hasSess {
			return false
		}
		return w.nativeComposer(sess) == backend.ComposerBusy
	default:
		return false
	}
	if live, err := w.probe(story); err == nil && live == backend.Settled {
		return false // B-51: the session ended under a record that still says busy
	}
	if rec, ok := busy.ReadRecord(w.EpicDir, story); ok && rec.Source == "dispatch" && hasSess {
		// The dispatch seed with no harness event since is evidence only while the backend does not see an idle agent.
		if w.nativeComposer(sess) == backend.ComposerEmpty {
			return false
		}
	}
	return true
}

// nativeComposer is the backend's own reading of the worker's composer (Orca agents[] state and screen classifier),
// never the busy record: the session is passed without its story, which is what makes a backend consult the record.
func (w *Watcher) nativeComposer(sess backend.Session) string {
	sess.Story = ""
	cs, err := w.Backend.Composer(sess)
	if err != nil {
		return backend.ComposerUnknown
	}
	return cs
}

func busyRecordPresent(epic, story string) bool {
	_, err := os.Stat(busy.Path(epic, story))
	return err == nil
}

// crewClass is crew_absorb_class without the paused branch (a declared wait is read from the status line by the
// caller): working on an active run-step or a busy pane, else none.
func (w *Watcher) crewClass(story string) string {
	if running, known := w.ciRunning(story); known && running {
		return crewWorking
	}
	if w.busyNow(story) {
		return crewWorking
	}
	return crewNone
}

// Worktree write probe (crew_worktree_written_since): a regular file under the story worktree newer than the anchor is
// positive evidence the crew is still producing work behind a quiet pane. One pruned, depth-bounded, wall-clock-bounded
// walk, reached only when an escalation is about to fire.
var (
	writePrune         = strings.Fields(".git node_modules .venv venv __pycache__ .mypy_cache .pytest_cache .ruff_cache .tox target dist build .next .cache vendor")
	writeMaxDepth      = 6
	writeProbeDeadline = 10 * time.Second // FM_WORKTREE_WRITE_TIMEOUT
)

// walkNewerFn is the walk worktreeWrittenSince bounds; a var so a test can stand in a walk that outlives the bound (a
// hung mount).
var walkNewerFn = walkNewer

// errWriteFound stops the walk at the first hit (-quit).
var errWriteFound = errors.New("found")

// worktreeWrittenSince reports whether any regular file under the story worktree is newer than anchor. Every other
// outcome - no recorded worktree, a missing one, a walk that fails, finds nothing or outlives the bound - is no
// evidence, so the escalation schedule is untouched.
//
// ponytail: a walk stuck on a hung mount is abandoned at the deadline, not killed (Go cannot cancel a blocked stat); the
// leaked goroutine is one per escalation window at most. Move the walk into a `find` child if hung mounts prove real.
func (w *Watcher) worktreeWrittenSince(story string, anchor time.Time) bool {
	wt := state.ReadWorktree(w.EpicDir, story)
	if wt == "" {
		return false
	}
	root, err := os.Stat(wt)
	if err != nil || !root.IsDir() {
		return false
	}
	walk, bound := walkNewerFn, writeProbeDeadline
	done := make(chan bool, 1)
	go func() { done <- walk(wt, root, anchor) }()
	select {
	case hit := <-done:
		return hit
	case <-time.After(bound):
		return false
	}
}

// walkNewer is the pruned, depth-bounded, single-filesystem walk behind worktreeWrittenSince (find -xdev -maxdepth 6
// \( <prune> \) -prune -o -type f -newer <anchor> -print -quit).
func walkNewer(wt string, root os.FileInfo, anchor time.Time) bool {
	prune := map[string]bool{}
	for _, p := range writePrune {
		prune[p] = true
	}
	base := strings.Count(filepath.Clean(wt), string(os.PathSeparator))
	err := filepath.WalkDir(wt, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if p == wt {
			return nil
		}
		if prune[d.Name()] {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		fi, err := d.Info()
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if strings.Count(filepath.Clean(p), string(os.PathSeparator))-base >= writeMaxDepth || !sameDevice(root, fi) {
				return filepath.SkipDir
			}
			return nil
		}
		if fi.Mode().IsRegular() && fi.ModTime().After(anchor) {
			return errWriteFound
		}
		return nil
	})
	return errors.Is(err, errWriteFound)
}
