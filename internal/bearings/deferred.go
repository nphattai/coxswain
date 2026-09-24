package bearings

import (
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/nphattai/coxswain/internal/state"
	"github.com/nphattai/coxswain/internal/wake"
)

// LeaderStory is the wake story id for workspace-level leader findings (not a story of the epic).
const LeaderStory = "_leader"

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
func startDeferred(o Opts, epics []string) *deferredRun {
	d := &deferredRun{o: o, epics: epics, forge: make(chan struct{}), reads: make(chan struct{})}
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

// publish appends the failed forge result once to every active epic's wake queue; a success stays silent.
func (d *deferredRun) publish() {
	d.mu.Lock()
	if d.published || d.forgeErr == nil {
		d.mu.Unlock()
		return
	}
	d.published = true
	note := "startup-forge: " + d.forgeLine()
	d.mu.Unlock()
	for _, ep := range d.epics {
		_, _ = wake.Append(ep, wake.Wake{Epic: filepath.Base(ep), Story: LeaderStory, Kind: wake.KindStatus, Note: note})
	}
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
			_, _ = wake.Append(ep, wake.Wake{Epic: filepath.Base(ep), Story: st.Story, Kind: wake.KindStatus, Note: "inactive-outcome: " + line})
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
