package bearings

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/nphattai/coxswain/internal/protocol/question"
	"github.com/nphattai/coxswain/internal/state"
	"github.com/nphattai/coxswain/internal/wake"
)

// Bounds ported verbatim from fm-session-start.sh and bin/fm-line-cap-lib.sh.
const (
	DefaultStatusTail  = 5                // FM_SESSION_START_STATUS_TAIL
	LineCap            = 220              // FM_LINE_CAP_DEFAULT
	DefaultQueuedLimit = 20               // FM_SESSION_START_QUEUED_LIMIT
	notDispatched      = "not_dispatched" // a story file with no lifecycle event yet
)

// ActiveEpics lists the workspace's open epic dirs: <ws>/epics/*, <ws>/*/epics/* and <ws>/*/*/epics/*, minus an
// archived epic (.cox.closed without .cox) and one whose DESIGN.md Status: says closed or complete.
func ActiveEpics(ws string) []string {
	seen := map[string]bool{}
	var out []string
	for _, pat := range []string{"epics/*", "*/epics/*", "*/*/epics/*"} {
		m, _ := filepath.Glob(filepath.Join(ws, pat))
		for _, ep := range m {
			if seen[ep] || !isDir(ep) || epicClosed(ep) {
				continue
			}
			seen[ep] = true
			out = append(out, ep)
		}
	}
	sort.Strings(out)
	return out
}

func isDir(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}

func epicClosed(ep string) bool {
	if _, err := os.Stat(filepath.Join(ep, ".cox.closed")); err == nil && !isDir(filepath.Join(ep, ".cox")) {
		return true
	}
	b, err := os.ReadFile(filepath.Join(ep, "DESIGN.md"))
	if err != nil {
		return false
	}
	for _, l := range strings.Split(string(b), "\n") {
		if st, ok := strings.CutPrefix(l, "Status:"); ok {
			st = strings.ToLower(strings.TrimSpace(st))
			return strings.HasPrefix(st, "closed") || strings.HasPrefix(st, "complete")
		}
	}
	return false
}

// storyFiles are the ids with a stories/<id>.md file.
func storyFiles(epicDir string) map[string]bool {
	m, _ := filepath.Glob(filepath.Join(epicDir, "stories", "*.md"))
	out := map[string]bool{}
	for _, p := range m {
		out[strings.TrimSuffix(filepath.Base(p), ".md")] = true
	}
	return out
}

// StoryStates reads cox state's source of truth for an epic: the folded event log (state.Load + state.Fold) over every
// story file, with each story's still-open questions (questions/<story>/qNNN.md with no answer yet).
func StoryStates(epicDir string) ([]StoryState, error) {
	events, _, err := state.Load(epicDir)
	if err != nil {
		return nil, err
	}
	snap := state.Fold(events)
	ids := storyFiles(epicDir)
	for id := range snap.Stories {
		if id != "" && !strings.HasPrefix(id, "_") {
			ids[id] = true
		}
	}
	var out []StoryState
	for id := range ids {
		st := StoryState{Story: id, State: notDispatched}
		if s := snap.Stories[id]; s != nil {
			st.State = string(s.State)
		}
		qs, err := openQuestions(epicDir, id)
		if err != nil {
			return nil, err
		}
		st.OpenQuestions = qs
		out = append(out, st)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Story < out[j].Story })
	return out, nil
}

// openQuestions lists a story's live questions that carry no answer file yet, each as "qNNN: <first body line>".
func openQuestions(epicDir, story string) ([]string, error) {
	ids, err := question.List(epicDir, story)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, id := range ids {
		dir := question.Dir(epicDir, story)
		if _, err := os.Stat(filepath.Join(dir, id+".answer.md")); err == nil {
			continue
		}
		b, _ := os.ReadFile(filepath.Join(dir, id+".md"))
		out = append(out, id+": "+firstBodyLine(string(b)))
	}
	return out, nil
}

// firstBodyLine is the first non-empty line after a --- header block.
func firstBodyLine(s string) string {
	if rest, ok := strings.CutPrefix(s, "---\n"); ok {
		if i := strings.Index(rest, "\n---\n"); i >= 0 {
			s = rest[i+len("\n---\n"):]
		}
	}
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			return l
		}
	}
	return ""
}

// capLine bounds one status line the way fm_cap_line does (wake.CapLine): at most LineCap characters, the cut marked.
func capLine(s string) string { return wake.CapLine(strings.ReplaceAll(s, "\n", " "), LineCap) }

// statusHistory returns each story's acked status lines in queue order. Unacked status wakes are still this turn's
// work queue and print in WAKE QUEUE, so the tail never prints a line twice.
func statusHistory(epicDir string) (map[string][]string, error) {
	all, err := wake.Load(epicDir)
	if err != nil {
		return nil, err
	}
	acked, err := wake.Acked(epicDir)
	if err != nil {
		return nil, err
	}
	out := map[string][]string{}
	for _, w := range all {
		if w.Kind != wake.KindStatus || w.Gen > acked {
			continue
		}
		note := w.Full
		if note == "" {
			note = w.Note
		}
		out[w.Story] = append(out[w.Story], note)
	}
	return out, nil
}

// printStatusTail prints one story's bounded tail. The header discloses both bounds and where the full log lives,
// workspace-relative so a digest with many stories does not repeat long absolute paths.
func printStatusTail(p *printer, relEpic string, lines []string, n int) {
	p.line(fmt.Sprintf("status tail (last %d line(s), each capped at %d characters, wake-EVENT history, not current state; full log: %s, or cox wake drain --full):",
		n, LineCap, filepath.Join(relEpic, wake.ControlDir, "wake.jsonl")))
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	for _, l := range lines {
		p.line(capLine(l))
	}
}

// fleetItem is one line in the four bearings sections.
type fleetItem struct{ section, text string }

// printFleet prints each active epic's story inventory (cox state rows, endpoint liveness, status tail), the orphan
// status, then the four bearings sections in their fixed order, each with its empty-state sentence.
func printFleet(p *printer, o Opts, epics []string) {
	var items []fleetItem
	if len(epics) == 0 {
		p.sub("Stories")
		p.line("(none) - no active epic in this workspace")
	}
	for _, ep := range epics {
		slug := filepath.Base(ep)
		rel, err := filepath.Rel(o.Workspace, ep)
		if err != nil {
			rel = ep
		}
		p.sub(fmt.Sprintf("Stories - epic %s (%s)", slug, rel))
		sts, err := StoryStates(ep)
		if err != nil {
			p.line(fmt.Sprintf("unreadable story state: %v", err))
			continue
		}
		hist, err := statusHistory(ep)
		if err != nil {
			p.line(fmt.Sprintf("unreadable wake log: %v", err))
		}
		if len(sts) == 0 {
			p.line("(none)")
		}
		known := map[string]bool{}
		for _, st := range sts {
			known[st.Story] = true
			p.line("")
			p.line("--- " + st.Story + " ---")
			p.line(st.Story + ": " + st.State)
			for _, q := range st.OpenQuestions {
				p.line("open question " + q)
			}
			switch {
			case o.Endpoint == nil:
				p.line("endpoint: unknown (no terminal probe)")
			default:
				if alive, handle := o.Endpoint(ep, st.Story); handle == "" {
					p.line("endpoint: unknown (no terminal recorded, or the backend probe could not tell)")
				} else if alive {
					p.line(fmt.Sprintf("endpoint: alive (backend=orca terminal=%s)", handle))
				} else {
					p.line(fmt.Sprintf("endpoint: dead (backend=orca terminal=%s)", handle))
				}
			}
			if lines := hist[st.Story]; len(lines) > 0 {
				printStatusTail(p, rel, lines, o.StatusTail)
			} else {
				p.line("status tail: (no status recorded yet)")
			}
			items = append(items, classifyStory(slug, st)...)
		}
		items = append(items, prReadyItems(ep, slug, sts)...)

		p.sub("Orphan status (wakes for a story with no story file)")
		var orphans []string
		for id := range hist {
			if !known[id] && id != "" && !strings.HasPrefix(id, "_") {
				orphans = append(orphans, id)
			}
		}
		sort.Strings(orphans)
		if len(orphans) == 0 {
			p.line("(none)")
		}
		for _, id := range orphans {
			p.line("")
			p.line("--- " + id + " ---")
			printStatusTail(p, rel, hist[id], o.StatusTail)
		}
	}
	empty := map[string]string{
		"Captain's Call":  "Nothing needs your action right now, captain.",
		"Recently Landed": "No recent completions are in the current baseline.",
		"Underway":        "Nothing is underway.",
		"Charted Next":    "Nothing is queued.",
	}
	for _, sec := range []string{"Captain's Call", "Recently Landed", "Underway", "Charted Next"} {
		p.sub(sec)
		n := 0
		for _, it := range items {
			if it.section == sec {
				p.line(it.text)
				n++
			}
		}
		if n == 0 {
			p.line(empty[sec])
		}
	}
}

// classifyStory places a story in the bearings sections (bearings SKILL.md "Chat-response contract"): an open question
// is the captain's call; completed, failed and canceled work has landed; working work is underway; submitted, parked,
// pending_external and not-yet-dispatched work is charted next. Action-free items never enter Captain's Call.
func classifyStory(slug string, st StoryState) []fleetItem {
	name := slug + "/" + st.Story
	var out []fleetItem
	for _, q := range st.OpenQuestions {
		out = append(out, fleetItem{"Captain's Call", name + " asks " + q})
	}
	switch state.State(st.State) {
	case state.Completed, state.Failed, state.Canceled:
		out = append(out, fleetItem{"Recently Landed", name + " - " + st.State})
	case state.Working, state.InputRequired:
		if len(st.OpenQuestions) == 0 {
			out = append(out, fleetItem{"Underway", name + " - " + st.State})
		}
	default:
		out = append(out, fleetItem{"Charted Next", name + " - " + st.State})
	}
	return out
}

// prReadyItems puts each story's latest pr_ready wake in Captain's Call with its full PR URL, unless the story has
// already landed.
func prReadyItems(epicDir, slug string, sts []StoryState) []fleetItem {
	all, err := wake.Load(epicDir)
	if err != nil {
		return nil
	}
	landed := map[string]bool{}
	for _, st := range sts {
		switch state.State(st.State) {
		case state.Completed, state.Failed, state.Canceled:
			landed[st.Story] = true
		}
	}
	latest := map[string]wake.Wake{}
	var order []string
	for _, w := range all {
		if w.Kind != wake.KindPRReady || landed[w.Story] {
			continue
		}
		if _, ok := latest[w.Story]; !ok {
			order = append(order, w.Story)
		}
		latest[w.Story] = w
	}
	var out []fleetItem
	for _, id := range order {
		w := latest[id]
		pr, _ := w.Evidence["pr"].(string)
		if pr == "" {
			pr = w.Note
		}
		out = append(out, fleetItem{"Captain's Call", fmt.Sprintf("%s/%s PR ready for review or merge: %s", slug, id, pr)})
	}
	return out
}
