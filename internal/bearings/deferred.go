package bearings

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/nphattai/coxswain/internal/state"
	"github.com/nphattai/coxswain/internal/wake"
)

// LeaderStory is the wake story id for workspace-level leader findings (not a story of the epic).
const LeaderStory = "_leader"

// DeferredFailedFile (under RuntimeDir) is the failed record a deferred worker leaves when it could not publish its
// results inside its budget (fm-startup-network.sh's failed stage record). Every digest prints it; the next worker run
// that publishes everything removes it.
const DeferredFailedFile = "bearings-deferred.failed"

// lockPoll is how often a bounded publish retries a held wake-queue lock.
const lockPoll = 50 * time.Millisecond

// deferredRun is the deferred forge stage one locked, non-re-emit digest started (fm-startup-network.sh): the forge
// probe runs concurrently with the digest and never blocks it; the slow inactive-story state reads run strictly after
// the digest, in the deferred worker. A failed result the digest could not print arrives once as a startup-forge wake.
type deferredRun struct {
	o     Opts
	epics []string

	mu        sync.Mutex
	forgeDone bool
	forgeErr  error
	harvest   string // "" until the digest reached its forge-checks stage; then printed | missed
	published bool

	forge     chan struct{}
	readsOnce sync.Once
	reads     chan struct{}
	readLines []string

	// The worker's publish budget (zero in the in-process digest, which publishes unbounded as before). Every wake
	// append shares it; what misses it is kept in unpublished for the failed record, and after the worker sealed its
	// run nothing is appended any more.
	deadline    time.Time
	pubs        sync.WaitGroup
	sealed      bool
	unpublished []string
	holders     []string // each distinct held lock, named once
	raced       bool
}

var (
	deferredMu   sync.Mutex
	deferredRuns = map[string]*deferredRun{}
)

func (d *deferredRun) labels() string {
	var l []string
	if d.o.Forge != nil {
		l = append(l, "GitHub authentication")
	}
	if d.o.StateRead != nil {
		l = append(l, "the inactive-story state reads")
	}
	return strings.Join(l, " and ")
}

// startDeferred launches the forge probe for this workspace and registers the run for Deferred to harvest.
func startDeferred(o Opts, epics []string, deadline time.Time) *deferredRun {
	d := &deferredRun{o: o, epics: epics, forge: make(chan struct{}), reads: make(chan struct{}), deadline: deadline}
	deferredMu.Lock()
	deferredRuns[o.Workspace] = d
	deferredMu.Unlock()
	go func() {
		var err error
		if o.Forge != nil {
			err = o.Forge()
		}
		d.mu.Lock()
		d.forgeDone, d.forgeErr = true, err
		publish := d.harvest == "missed"
		d.mu.Unlock()
		if publish {
			d.publish()
		}
		close(d.forge)
	}()
	return d
}

func (d *deferredRun) forgeLine() string {
	if d.forgeErr != nil {
		return "NEEDS_GH_AUTH - " + d.forgeErr.Error()
	}
	return "GitHub authentication: ok"
}

// harvestInto prints whatever the forge probe has published by now, without waiting.
func (d *deferredRun) harvestInto(p *printer) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.forgeDone {
		d.harvest = "printed"
		p.line("completed off the startup path: " + d.labels() + ".")
		if d.forgeErr != nil {
			p.line(d.forgeLine())
		} else {
			p.line("(silent - no problems found)")
		}
		return
	}
	d.harvest = "missed"
	p.lines("IN PROGRESS - the deferred forge checks have not finished yet.",
		"NOT yet confirmed: "+d.labels()+".",
		"Only a FAILED or otherwise actionable result arrives as a startup-forge wake; a clean success stays silent.")
}

// missed marks a digest that ended (truncated) before its forge-checks stage; a finished failure is published now.
func (d *deferredRun) missed() {
	d.mu.Lock()
	publish := false
	if d.harvest == "" {
		d.harvest = "missed"
		publish = d.forgeDone
	}
	d.mu.Unlock()
	if publish {
		d.publish()
	}
}

// owner reports whether the leader that started this stage still holds the lease: a worker that outlived a takeover
// publishes nothing, the new holder's own start runs the checks again.
func (d *deferredRun) owner() bool {
	return d.o.LeaderID == "" || LeaseHolder(d.o.Workspace) == d.o.LeaderID
}

// queued reports whether an identical wake for the story is already waiting unacked, so a rerun never duplicates it.
func queued(epicDir, story, note string) bool {
	ws, _ := WakeDrain(epicDir)
	for _, w := range ws {
		if w.Story == story && w.Note == note {
			return true
		}
	}
	return false
}

// publish appends the failed forge result once to every active epic's wake queue; a success stays silent.
func (d *deferredRun) publish() {
	d.mu.Lock()
	if d.published || d.forgeErr == nil || !d.owner() {
		d.mu.Unlock()
		return
	}
	d.published = true
	note := "startup-forge: " + d.forgeLine()
	d.mu.Unlock()
	for _, ep := range d.epics {
		if !queued(ep, LeaderStory, note) {
			d.append(ep, wake.Wake{Epic: filepath.Base(ep), Story: LeaderStory, Kind: wake.KindStatus, Note: note})
		}
	}
}

// append publishes one wake. In the worker it shares the publish budget: it waits for the epic's wake-queue lock
// without holding it until the deadline, and a lock still held then is a miss, recorded with its holder, and nothing
// is appended - so a result the failed record reports can never land behind it (5842d42). Only when the lock was seen
// free and another process took it in the instant before the append can a late append still land; the record then
// says so.
func (d *deferredRun) append(ep string, w wake.Wake) {
	if d.deadline.IsZero() {
		_, _ = wake.Append(ep, w)
		return
	}
	d.mu.Lock()
	if d.sealed {
		d.mu.Unlock()
		return
	}
	d.pubs.Add(1)
	d.mu.Unlock()
	defer d.pubs.Done()
	miss := func(holder string, raced bool) {
		d.mu.Lock()
		defer d.mu.Unlock()
		d.unpublished = append(d.unpublished, fmt.Sprintf("%s/%s: %s", w.Epic, w.Story, w.Note))
		if !slices.Contains(d.holders, holder) {
			d.holders = append(d.holders, holder)
		}
		d.raced = d.raced || raced
	}
	lock := filepath.Join(ep, wake.ControlDir, "wake.lock") // internal/wake's queue lock (queue.go lockPath)
	if held, err := waitFree(lock, d.deadline); err == nil && held {
		miss(lockHolder(lock), false)
		return
	}
	done := make(chan struct{})
	go func() { _, _ = wake.Append(ep, w); close(done) }()
	select {
	case <-done:
	case <-time.After(max(time.Until(d.deadline), time.Second)):
		miss(lockHolder(lock), true)
	}
}

// waitFree polls the lock without blocking until it is free (released at once) or the deadline passes (held).
func waitFree(path string, deadline time.Time) (held bool, err error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return false, err
	}
	defer f.Close()
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
			return false, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) {
			return false, err
		}
		if !time.Now().Before(deadline) {
			return true, nil
		}
		time.Sleep(lockPoll)
	}
}

// lockHolder names a held wake-queue lock: its path, and the holder's pid when the lock file records one on its first
// line ("pid=N" or a bare "N").
func lockHolder(path string) string {
	name := "the wake queue lock " + path
	b, _ := os.ReadFile(path)
	first, _, _ := strings.Cut(string(b), "\n")
	if pid, err := strconv.Atoi(strings.TrimPrefix(strings.TrimSpace(first), "pid=")); err == nil && pid > 0 {
		return name + " is still held by pid " + strconv.Itoa(pid)
	}
	return name + " is still held (its holder records no pid)"
}

// runReads is the deferred inactive-story scan: each working story's slow current-state read, its finding queued as an
// inactive-outcome status wake for that story.
func (d *deferredRun) runReads() {
	defer close(d.reads)
	if d.o.StateRead == nil {
		return
	}
	for _, ep := range d.epics {
		sts, err := StoryStates(ep)
		if err != nil {
			continue
		}
		for _, st := range sts {
			if st.State != string(state.Working) {
				continue
			}
			out, err := d.o.StateRead(ep, st.Story)
			line := strings.TrimSpace(out)
			if err != nil {
				line = "state read failed: " + err.Error()
			}
			if line == "" {
				continue
			}
			d.readLines = append(d.readLines, fmt.Sprintf("%s/%s inactive-outcome: %s", filepath.Base(ep), st.Story, line))
			note := "inactive-outcome: " + line
			if d.owner() && !queued(ep, st.Story, note) {
				d.append(ep, wake.Wake{Epic: filepath.Base(ep), Story: st.Story, Kind: wake.KindStatus, Note: note})
			}
		}
	}
}

// Deferred is the deferred worker's harvest: it runs the post-digest state reads of the workspace's last locked
// startup, waits up to wait for the whole stage, and returns its report. A read-only or re-emitted digest starts no
// stage, so there is nothing to run.
func Deferred(ws string, wait time.Duration) (string, error) {
	if abs, err := filepath.Abs(ws); err == nil {
		ws = abs
	}
	deferredMu.Lock()
	d := deferredRuns[ws]
	deferredMu.Unlock()
	if d == nil {
		return "not started - no deferred forge checks have run for this workspace.", nil
	}
	d.readsOnce.Do(func() { go d.runReads() })
	deadline := time.After(wait)
	for _, ch := range []chan struct{}{d.forge, d.reads} {
		select {
		case <-ch:
		case <-deadline:
			return "IN PROGRESS - the deferred forge checks have not finished yet (NOT yet confirmed: " + d.labels() + ").", nil
		}
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	var out []string
	if d.o.Forge != nil {
		out = append(out, d.forgeLine())
	}
	out = append(out, d.readLines...)
	if len(out) == 0 {
		return "(silent - no problems found)", nil
	}
	return strings.Join(out, "\n"), nil
}

// RunDeferred is the detached deferred worker (cox bearings deferred): it runs the forge probe and the inactive-story
// reads of o's workspace, publishes a failed forge result as a startup-forge wake (there is no digest left to print
// it), and returns the report once the stage finished or wait elapsed. Every lock wait shares wait as its budget: what
// it could not publish inside it goes to the failed record the next digest prints, with the rerun command (5842d42).
func RunDeferred(o Opts, wait time.Duration) (string, error) {
	o = withDefaults(o)
	d := startDeferred(o, ActiveEpics(o.Workspace), time.Now().Add(wait))
	d.missed()
	rep, err := Deferred(o.Workspace, wait)
	d.mu.Lock()
	d.sealed = true
	d.mu.Unlock()
	d.pubs.Wait()
	if rerr := d.record(); err == nil {
		err = rerr
	}
	return rep, err
}

// record writes the failed record when anything missed the budget, and removes a previous one when everything was
// published.
func (d *deferredRun) record() error {
	path := filepath.Join(d.o.Workspace, RuntimeDir, DeferredFailedFile)
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.unpublished) == 0 {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	lines := []string{
		"state=failed",
		"reason: " + strings.Join(d.holders, "; ") + ".",
		"unpublished results (kept here):",
	}
	for _, u := range d.unpublished {
		lines = append(lines, "  "+u)
	}
	if d.raced {
		lines = append(lines, "note: the lock was taken in the instant before an append; that wake may still land late.")
	}
	lines = append(lines, "rerun: cox bearings deferred --root "+d.o.Workspace)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// DeferredFailed returns the lines of the workspace's failed deferred record, nil when there is none.
func DeferredFailed(ws string) []string {
	b, err := os.ReadFile(filepath.Join(ws, RuntimeDir, DeferredFailedFile))
	if err != nil {
		return nil
	}
	var out []string
	for _, l := range strings.Split(strings.TrimRight(string(b), "\n"), "\n") {
		if l != "state=failed" {
			out = append(out, l)
		}
	}
	return out
}
