// Package migrate converts a v1 epic control file (<epic>/.run, append-only "key=value", last value wins) into the v2
// control tree: an events.jsonl the fold can replay, .cox/sessions/<story>.json for each live dispatch, and .cox/run.
// It is read-only by default (a dry run returns a plan the caller prints); Apply writes the tree and renames .run to
// .run.migrated so the epic no longer carries a live v1 state next to a v2 one (what cox doctor flags). The v1
// last-value-wins log has no attempt history, so every synthesized event is attempt 1 (accepted, plan risk).
package migrate

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/nphattai/coxswain/internal/adapter/backend"
	"github.com/nphattai/coxswain/internal/env"
	"github.com/nphattai/coxswain/internal/state"
)

// StoryPlan is the synthesized migration for one v1 story.
type StoryPlan struct {
	ID       string
	Final    state.State      // completed | parked | working | pending_external
	Events   []state.Event    // in apply order (dispatch, then parked, then done)
	Session  *backend.Session // non-nil when the story has a live dispatch to persist
	Dispatch string           // the live dispatch id (dispatch.<id>) v1 still carried, or ""
	Term     string
}

// Plan is the whole migration derived from a .run: the run id, one entry per started story, the machine-layer
// allocations to carry forward, and the v1 handoffs to wrap into checkpoints.
type Plan struct {
	EpicDir  string
	Run      string
	Stories  []StoryPlan
	Allocs   []env.MigratedAlloc
	Handoffs []HandoffWrap
}

// storyPrefixes are the .run key prefixes that name a story (as "<prefix>.<id>"). Allocation keys (port/db/redis/sim/env)
// also carry a story id but are v1 machine-layer state read separately into Allocs.
var storyPrefixes = []string{"dispatch", "term", "task", "wt", "started", "parked", "done"}

// allocPrefixes are the .run key prefixes for a story's machine-layer allocation.
var allocPrefixes = []string{"port", "db", "redis", "sim", "env"}

// Read parses <epic>/.run into a Plan without writing anything.
func Read(epicDir string) (Plan, error) {
	raw, err := os.ReadFile(filepath.Join(epicDir, ".run"))
	if err != nil {
		return Plan{}, fmt.Errorf("read .run: %w", err)
	}
	kv := lastValueWins(string(raw))
	slug := filepath.Base(epicDir)
	p := Plan{EpicDir: epicDir, Run: kv["run"]}

	for _, id := range storyIDs(kv) {
		dispatch := kv["dispatch."+id]
		term := kv["term."+id]
		task := kv["task."+id]
		parked := kv["parked."+id] != ""
		done := kv["done."+id] != ""
		started := dispatch != "" || task != "" || kv["started."+id] != "" || parked || done
		if !started {
			continue
		}

		sp := StoryPlan{ID: id, Dispatch: dispatch, Term: term}
		// dispatch id: the live dispatch when present, else the task id (a parked/done story cleared dispatch.<id>).
		dispID := dispatch
		if dispID == "" {
			dispID = task
		}
		sp.Events = append(sp.Events, event(slug, id, state.Submitted, state.Working, map[string]any{
			"dispatch": dispID, "term": term, "migrated": true,
		}))
		from := state.Working
		if parked {
			sp.Events = append(sp.Events, event(slug, id, from, state.Parked, map[string]any{"migrated": true}))
			from = state.Parked
		}
		if done {
			sp.Events = append(sp.Events, event(slug, id, from, state.Completed, map[string]any{"migrated": true}))
			sp.Final = state.Completed
		} else if parked {
			sp.Final = state.Parked
		} else {
			sp.Final = state.Working
		}

		// A live session exists only while dispatch.<id> is set (park and done clear it in v1). ApplyLiveness drops it
		// again if Orca reports the dispatch has settled.
		if dispatch != "" {
			sp.Session = &backend.Session{Kind: "orca", ID: dispatch, Handle: term}
		}
		p.Stories = append(p.Stories, sp)
	}

	p.Allocs = readAllocs(kv)
	wraps, err := handoffWraps(epicDir, wtPaths(kv))
	if err != nil {
		return Plan{}, err
	}
	p.Handoffs = wraps
	return p, nil
}

// ApplyLiveness adjusts the plan against what Orca reports: a working story whose dispatch is not ready|running keeps
// no session and lands in pending_external (intended_to working) for cox reconcile to finish, with the last observed
// dispatch state recorded. A live dispatch keeps its session unchanged. Call it only with real backend info; with no
// backend the caller keeps Read's behavior and warns (a v1 .run on another machine has no Orca to ask).
func (p *Plan) ApplyLiveness(workers []backend.Worker) {
	byDispatch := map[string]backend.Worker{}
	for _, w := range workers {
		byDispatch[w.Dispatch] = w
	}
	slug := filepath.Base(p.EpicDir)
	for i := range p.Stories {
		sp := &p.Stories[i]
		if sp.Dispatch == "" {
			continue // parked/done story: v1 already cleared the dispatch, no live session either way
		}
		w, ok := byDispatch[sp.Dispatch]
		if ok && w.Alive() {
			continue // ready|running: keep the session Read built
		}
		// Dispatch settled or gone: drop the session, record the last state, and (for a working story) hand it to
		// reconcile via pending_external rather than asserting it is still working.
		last := "gone"
		if ok {
			last = strings.ToLower(strings.TrimSpace(w.State))
		}
		sp.Session = nil
		if len(sp.Events) > 0 {
			sp.Events[0].Evidence["last_dispatch"] = last
		}
		if sp.Final == state.Working {
			sp.Events = append(sp.Events, pendingEvent(slug, sp.ID, state.Working, map[string]any{
				"intended_to": string(state.Working), "dispatch": sp.Dispatch, "last_dispatch": last, "migrated": true,
			}))
			sp.Final = state.PendingExternal
		}
	}
}

// readAllocs collects each story's machine-layer allocation (port/db/redis/sim/env, last value wins, empties dropped)
// into a migrated-unverified record for cox env reconcile. A story with no non-empty allocation key is skipped.
func readAllocs(kv map[string]string) []env.MigratedAlloc {
	ids := map[string]bool{}
	for k := range kv {
		prefix, id, ok := strings.Cut(k, ".")
		if !ok || id == "" {
			continue
		}
		for _, p := range allocPrefixes {
			if prefix == p && kv[k] != "" {
				ids[id] = true
			}
		}
	}
	sorted := make([]string, 0, len(ids))
	for id := range ids {
		sorted = append(sorted, id)
	}
	sort.Strings(sorted)
	var out []env.MigratedAlloc
	for _, id := range sorted {
		port, _ := strconv.Atoi(kv["port."+id])
		out = append(out, env.MigratedAlloc{
			Story:   id,
			Port:    port,
			DB:      kv["db."+id],
			Redis:   kv["redis."+id],
			Sim:     kv["sim."+id],
			EnvFile: kv["env."+id],
			State:   env.StateMigratedUnverified,
		})
	}
	return out
}

// wtPaths returns the wt.<story> map from a parsed .run.
func wtPaths(kv map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range kv {
		if id, ok := strings.CutPrefix(k, "wt."); ok && v != "" {
			out[id] = v
		}
	}
	return out
}

// Apply writes the plan: every event (actor=migration, external_confirmed=true), each live session, .cox/run, then
// renames .run to .run.migrated so no live v1 state remains beside the v2 tree.
func Apply(p Plan) error {
	// Idempotency: appending onto an existing log would double every event. Refuse and say what to do.
	if _, err := os.Stat(state.EventsPath(p.EpicDir)); err == nil {
		return fmt.Errorf("%s already exists: this epic is already on v2 (or migrate ran before). Remove .cox/events.jsonl to re-migrate, or migrate a fresh epic", state.EventsPath(p.EpicDir))
	}
	for _, sp := range p.Stories {
		for _, ev := range sp.Events {
			if err := state.Append(p.EpicDir, ev); err != nil {
				return fmt.Errorf("append event for %s: %w", sp.ID, err)
			}
		}
		if sp.Session != nil {
			if err := writeSession(p.EpicDir, sp.ID, *sp.Session); err != nil {
				return err
			}
		}
	}
	if p.Run != "" {
		if err := writeControl(p.EpicDir, "run", p.Run); err != nil {
			return err
		}
	}
	for _, a := range p.Allocs {
		if err := env.WriteMigratedAlloc(p.EpicDir, a); err != nil {
			return fmt.Errorf("write alloc for %s: %w", a.Story, err)
		}
	}
	if err := wrapHandoffs(p.Handoffs); err != nil {
		return err
	}
	old := filepath.Join(p.EpicDir, ".run")
	if err := os.Rename(old, filepath.Join(p.EpicDir, ".run.migrated")); err != nil {
		return fmt.Errorf("rename .run: %w", err)
	}
	return nil
}

// Table renders the dry-run plan for the CLI.
func (p Plan) Table() string {
	var b strings.Builder
	fmt.Fprintf(&b, "run: %s\n", nonEmpty(p.Run, "(none)"))
	fmt.Fprintf(&b, "%-24s %-11s %-26s %s\n", "story", "state", "events", "session")
	for _, sp := range p.Stories {
		var kinds []string
		for _, ev := range sp.Events {
			kinds = append(kinds, string(ev.To))
		}
		sess := "-"
		if sp.Session != nil {
			sess = "orca:" + sp.Session.ID
		}
		fmt.Fprintf(&b, "%-24s %-11s %-26s %s\n", sp.ID, sp.Final, strings.Join(kinds, ","), sess)
	}
	if len(p.Stories) == 0 {
		b.WriteString("(no started stories in .run)\n")
	}
	return b.String()
}

func event(slug, story string, from, to state.State, ev map[string]any) state.Event {
	return state.Event{
		Epic: slug, Story: story, Attempt: 1, Actor: state.Migration,
		From: from, To: to, Evidence: ev, ExternalConfirmed: true,
	}
}

// pendingEvent is a migration event landing on pending_external: it is NOT externally confirmed, so the fold keeps the
// story pending and cox reconcile finishes it toward evidence.intended_to.
func pendingEvent(slug, story string, from state.State, ev map[string]any) state.Event {
	return state.Event{
		Epic: slug, Story: story, Attempt: 1, Actor: state.Migration,
		From: from, To: state.PendingExternal, Evidence: ev, ExternalConfirmed: false,
	}
}

// lastValueWins collapses the append-only "key=value" log into a map where the last line for a key wins.
func lastValueWins(s string) map[string]string {
	kv := map[string]string{}
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimRight(line, "\r")
		k, v, ok := strings.Cut(line, "=")
		if !ok || k == "" {
			continue
		}
		kv[k] = v
	}
	return kv
}

// storyIDs returns the sorted set of story ids named by any story-scoped key.
func storyIDs(kv map[string]string) []string {
	seen := map[string]bool{}
	for k := range kv {
		prefix, id, ok := strings.Cut(k, ".")
		if !ok || id == "" {
			continue
		}
		for _, p := range storyPrefixes {
			if prefix == p {
				seen[id] = true
			}
		}
	}
	ids := make([]string, 0, len(seen))
	for id := range seen {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func writeSession(epicDir, story string, s backend.Session) error {
	dir := filepath.Join(epicDir, ".cox", "sessions")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, story+".json"), b, 0o644)
}

func writeControl(epicDir, name, content string) error {
	dir := filepath.Join(epicDir, ".cox")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644)
}

func nonEmpty(s, def string) string {
	if s == "" {
		return def
	}
	return s
}
