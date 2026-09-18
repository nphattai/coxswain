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

// Load reads and parses <epic>/.cox/events.jsonl. It returns the events in file order. A line whose JSON is corrupt
// is a hard error naming the 1-based line number (a torn or bad record must not be silently dropped). A line that
// parses but carries an unknown schema is skipped and reported in warnings, never fatal (forward compatibility).
// A missing log is not an error: it returns no events.
func Load(epicDir string) (events []Event, warnings []string, err error) {
	f, err := os.Open(EventsPath(epicDir))
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
			return nil, warnings, fmt.Errorf("corrupt JSON at %s line %d: %w", EventsPath(epicDir), line, err)
		}
		if probe.Schema != Schema {
			warnings = append(warnings, fmt.Sprintf("line %d: unknown schema %q, skipped", line, probe.Schema))
			continue
		}
		var ev Event
		if err := json.Unmarshal(raw, &ev); err != nil {
			return nil, warnings, fmt.Errorf("corrupt JSON at %s line %d: %w", EventsPath(epicDir), line, err)
		}
		events = append(events, ev)
	}
	if err := sc.Err(); err != nil {
		return nil, warnings, fmt.Errorf("read event log: %w", err)
	}
	return events, warnings, nil
}
