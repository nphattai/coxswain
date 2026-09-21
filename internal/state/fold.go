package state

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"sort"
)

// StorySnap is the folded current state of one story.
type StorySnap struct {
	ID              string `json:"id"`
	State           State  `json:"state"`
	Attempt         int    `json:"attempt"`
	LastEvent       Event  `json:"last_event"`
	PendingExternal bool   `json:"pending_external"` // last transition awaits external confirmation; ownership is retained
}

// Snapshot is the derived state of an epic: the fold of its event log. It is a cache (snapshot.json) that must
// rebuild identically from events.jsonl.
type Snapshot struct {
	Epic    string                `json:"epic"`
	Stories map[string]*StorySnap `json:"stories"`
}

// Fold reduces an ordered event slice to a Snapshot. It is pure: no I/O, no clock, no globals. Events are applied in
// slice order (the log's order); for each story the last applicable event wins. A story's state is its last event's
// `to`; PendingExternal is true when that last event landed on pending_external without external_confirmed, meaning
// ownership is not cleared (event.v1). Records with an unknown schema are ignored (versioning: readers skip them).
func Fold(events []Event) Snapshot {
	snap := Snapshot{Stories: map[string]*StorySnap{}}
	for _, ev := range events {
		if ev.Schema != Schema {
			continue
		}
		if snap.Epic == "" {
			snap.Epic = ev.Epic
		}
		// A typed event (design_signed, design_amended) is an epic-scoped fact, not a story transition; it never folds
		// into a story's state.
		if ev.Type != "" {
			continue
		}
		s := snap.Stories[ev.Story]
		if s == nil {
			s = &StorySnap{ID: ev.Story}
			snap.Stories[ev.Story] = s
		}
		s.State = ev.To
		s.Attempt = ev.Attempt
		s.LastEvent = ev
		s.PendingExternal = ev.To == PendingExternal && !ev.ExternalConfirmed
	}
	return snap
}

// SortedStories returns the snapshot's stories ordered by id, for deterministic output.
func (s Snapshot) SortedStories() []*StorySnap {
	out := make([]*StorySnap, 0, len(s.Stories))
	for _, st := range s.Stories {
		out = append(out, st)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Load reads the epic's two logs and merges them: the runtime log <epic>/.cox/events.jsonl (story lifecycle) and the
// durable ledger <epic>/ledger.jsonl (design_signed / design_amended, committed with git). Events are returned sorted by
// timestamp, so isSigned, cox board and cox doctor read one truth whether the signature was written to the ledger (the
// new path) or to an older events.jsonl (both are read, so a pre-ledger design_signed still counts - no migration). A
// line whose JSON is corrupt is a hard error naming the file and 1-based line; an unknown schema is skipped with a
// warning; a missing file is not an error.
func Load(epicDir string) (events []Event, warnings []string, err error) {
	runtime, w1, err := loadLog(EventsPath(epicDir))
	if err != nil {
		return nil, nil, err
	}
	ledger, w2, err := loadLog(LedgerPath(epicDir))
	if err != nil {
		return nil, w1, err
	}
	events = append(runtime, ledger...)
	// Stable sort by timestamp so cross-file order is deterministic; equal timestamps keep runtime-before-ledger order.
	sort.SliceStable(events, func(i, j int) bool { return events[i].TS < events[j].TS })
	warnings = append(w1, w2...)
	return events, warnings, nil
}

// loadLog parses one .jsonl event log in file order. A missing file returns no events (not an error).
func loadLog(path string) (events []Event, warnings []string, err error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil, nil
		}
		return nil, nil, fmt.Errorf("open event log: %w", err)
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024) // one event per line; allow long evidence maps
	line := 0
	for sc.Scan() {
		line++
		raw := bytes.TrimSpace(sc.Bytes())
		if len(raw) == 0 {
			continue
		}
		// Peek the schema first so an unknown-schema record is a warning, not a decode into the v1 struct.
		var probe struct {
			Schema string `json:"schema"`
		}
		if err := json.Unmarshal(raw, &probe); err != nil {
			return nil, warnings, fmt.Errorf("corrupt JSON at %s line %d: %w", path, line, err)
		}
		if probe.Schema != Schema {
			warnings = append(warnings, fmt.Sprintf("%s line %d: unknown schema %q, skipped", path, line, probe.Schema))
			continue
		}
		var ev Event
		if err := json.Unmarshal(raw, &ev); err != nil {
			return nil, warnings, fmt.Errorf("corrupt JSON at %s line %d: %w", path, line, err)
		}
		events = append(events, ev)
	}
	if err := sc.Err(); err != nil {
		return nil, warnings, fmt.Errorf("read event log: %w", err)
	}
	return events, warnings, nil
}
