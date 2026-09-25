package wake

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/nphattai/coxswain/internal/protocol/decision"
	"github.com/nphattai/coxswain/internal/protocol/question"
	"github.com/nphattai/coxswain/internal/state"
)

// Presentation bounds (fm-wake-drain.sh:307,447 and fm-line-cap-lib.sh): each section item is cut to 219 characters
// with the shared truncation marker, and each section's items share a 4000-byte budget.
const (
	itemCap     = 220 - 1
	sectionCap  = 4000
	truncMarker = " [truncated]"
)

// PresentOptions steers one drain presentation. WatcherAlive, when set, reports whether a live watcher with a fresh
// beacon holds the epic; the presenter asserts liveness only when work is in flight.
type PresentOptions struct {
	Peek         bool
	Full         bool
	WatcherAlive func() bool
}

// Present is `cox wake drain` (fm-wake-drain.sh): it prints the unacked wakes, then the status sections every drain
// carries - including the empty-queue fast path - STATUS OUTCOME BACKSTOP (the newest captain-facing status event
// whose wake was never handled) and OPEN DECISIONS (every decision still open in a worker's folded status history).
// Retired rows and the watcher-down banner go to errOut. Every section is prepared before any byte is written, and the
// backstop receipts commit only after out accepted the whole presentation, so a failed consumer loses nothing and a
// failed receipt only repeats. A peek presentation retires nothing and commits no receipt.
func Present(epicDir string, out, errOut io.Writer, opts PresentOptions) error {
	res, err := DrainReport(epicDir, opts.Peek)
	if err != nil {
		return err
	}
	fmt.Fprint(errOut, res.RetiredNotice(epicDir))

	var buf bytes.Buffer
	for _, w := range res.Wakes {
		buf.WriteString(FormatWake(w, opts.Full) + "\n")
	}
	var receipts map[string]int
	hist, herr := Histories(epicDir)
	if herr != nil {
		fmt.Fprintf(&buf, "STATUS PRESENTATION INCOMPLETE: status history could not be read: %v\n", herr)
	} else {
		receipts, err = backstopSection(&buf, epicDir, hist)
		if err != nil {
			fmt.Fprintf(&buf, "STATUS OUTCOME BACKSTOP SKIPPED: %v; retry on the next drain.\n", err)
		}
		openDecisionsSection(&buf, hist)
	}
	if _, err := out.Write(buf.Bytes()); err != nil {
		return fmt.Errorf("write drain presentation: %w", err)
	}
	if !opts.Peek && len(receipts) > 0 {
		if err := commitReceipts(epicDir, receipts); err != nil {
			fmt.Fprintf(errOut, "wake drain: backstop receipt could not be committed (%v); the presented backstop repeats on the next drain\n", err)
		}
	}
	if opts.WatcherAlive != nil {
		fmt.Fprint(errOut, watcherDownBanner(epicDir, opts.WatcherAlive))
	}
	return nil
}

// FormatWake renders one wake as the drain prints it: "[gen N] <kind> <story>: <note>". With full it prints the
// untruncated body when the record kept one; an empty note falls back to the kind label.
func FormatWake(w Wake, full bool) string {
	note := w.Note
	if full && w.Full != "" {
		note = w.Full
	}
	if note == "" {
		note = string(w.Kind)
	}
	return fmt.Sprintf("[gen %d] %s %s: %s", w.Gen, w.Kind, w.Story, note)
}

// OpenDecisionLine renders one still-open decision the way the drain's OPEN DECISIONS section prints it
// (fm-wake-drain.sh:447 print_open_decisions_section): "<story> [key=<k>] <verb>: <note>", with the key segment
// omitted for the default key.
func OpenDecisionLine(story string, d decision.Decision) string {
	line := story
	if d.Key != decision.DefaultKey {
		line += " [key=" + d.Key + "]"
	}
	return line + " " + d.Verb + ": " + d.Note
}

// CapLine cuts a line to max characters, ending it with the shared " [truncated]" marker (fm_cap_line_var).
func CapLine(line string, max int) string {
	r := []rune(line)
	if len(r) <= max {
		return line
	}
	keep := max - len(truncMarker)
	if keep < 0 {
		keep = 0
	}
	return string(r[:keep]) + truncMarker
}

// History is one story's worker status history as firstmate status lines (plan decision 1, leader ruling q001):
// Lines[i] is the line, Notes[i] the note its report carried (the text its wake row repeats), Kind the story kind.
type History struct {
	Story string
	Kind  decision.Kind
	Lines []string
	Notes []string
	first []bool // Lines[i] is the first line of its status event
}

// Histories reads every story's worker status events from the epic event log, in log order, and renders each as a
// status line: a status report's note is the line; a done report reads "done: <note>" and a stuck report
// "blocked: <note>" unless the note already declares that verb; a question reads "needs-decision [key=qNNN]: <body>"
// and, once answered, is followed by "resolved [key=qNNN]: answered". Stories come back sorted by id.
func Histories(epicDir string) ([]History, error) {
	events, _, err := state.Load(epicDir)
	if err != nil {
		return nil, err
	}
	byStory := map[string]*History{}
	var order []string
	for _, ev := range events {
		if ev.Type != "" || ev.Actor != state.Worker || ev.From != state.Working || ev.To != state.Working {
			continue
		}
		phase, _ := ev.Evidence["phase"].(string)
		note, _ := ev.Evidence["note"].(string)
		if phase == "" {
			continue
		}
		h := byStory[ev.Story]
		if h == nil {
			h = &History{Story: ev.Story, Kind: StoryKind(epicDir, ev.Story)}
			byStory[ev.Story] = h
			order = append(order, ev.Story)
		}
		for i, line := range statusLines(epicDir, ev.Story, phase, note) {
			h.Lines = append(h.Lines, line)
			h.Notes = append(h.Notes, note)
			h.first = append(h.first, i == 0)
		}
	}
	sort.Strings(order)
	out := make([]History, 0, len(order))
	for _, s := range order {
		out = append(out, *byStory[s])
	}
	return out, nil
}

// statusLines renders one status event (see Histories).
func statusLines(epicDir, story, phase, note string) []string {
	declares := func(verbs ...string) bool {
		v := decision.Verb(note)
		for _, want := range verbs {
			if v == want && strings.Contains(decision.Unstamped(note), ":") {
				return true
			}
		}
		return false
	}
	switch phase {
	case "done":
		if !declares("done") {
			return []string{"done: " + note}
		}
	case "stuck":
		if !declares("blocked", "needs-decision", "failed") {
			return []string{"blocked: " + note}
		}
	case "question":
		id := strings.TrimPrefix(note, "asked ")
		lines := []string{"needs-decision [key=" + id + "]: " + questionBody(epicDir, story, id)}
		if questionAnswered(epicDir, story, id) {
			lines = append(lines, "resolved [key="+id+"]: answered")
		}
		return lines
	}
	return []string{note}
}

// questionBody reads a question's text (live or handled), one line, header stripped (internal/protocol/question
// layout: <dir>/qNNN.md, <dir>/handled/qNNN.md).
func questionBody(epicDir, story, id string) string {
	for _, p := range []string{filepath.Join(question.Dir(epicDir, story), id+".md"), filepath.Join(question.Dir(epicDir, story), "handled", id+".md")} {
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		s := string(b)
		if strings.HasPrefix(s, "---\n") {
			if i := strings.Index(s[4:], "\n---\n"); i >= 0 {
				s = s[4+i+len("\n---\n"):]
			}
		}
		return strings.Join(strings.Fields(s), " ")
	}
	return "question " + id
}

// questionAnswered: the leader's answer exists (qNNN.answer.md) or the worker consumed it (the pair moved to handled/).
func questionAnswered(epicDir, story, id string) bool {
	d := question.Dir(epicDir, story)
	for _, p := range []string{filepath.Join(d, id+".answer.md"), filepath.Join(d, "handled", id+".md")} {
		if _, err := os.Stat(p); err == nil {
			return true
		}
	}
	return false
}

// StoryKind is the story's frontmatter kind (ADR 0018: unset reads as ship); a story with no story file is unknown,
// which firstmate's terminal-supersession rule leaves alone.
func StoryKind(epicDir, story string) decision.Kind {
	b, err := os.ReadFile(filepath.Join(epicDir, "stories", story+".md"))
	if err != nil {
		return decision.KindUnknown
	}
	s := string(b)
	if !strings.HasPrefix(s, "---\n") {
		return decision.KindShip
	}
	end := strings.Index(s[4:], "\n---")
	if end < 0 {
		return decision.KindShip
	}
	for _, l := range strings.Split(s[4:4+end], "\n") {
		if k, ok := strings.CutPrefix(l, "kind:"); ok && strings.TrimSpace(k) != "" {
			return decision.ParseKind(k)
		}
	}
	return decision.KindShip
}

// openDecisionsSection prints OPEN DECISIONS (fm-wake-drain.sh:447): every still-open decision, epic-wide, folded
// from the durable histories rather than from this drain's rows, bounded, and silent when nothing is open.
func openDecisionsSection(buf *bytes.Buffer, hist []History) {
	var items []string
	for _, h := range hist {
		for _, d := range decision.Fold(h.Lines, h.Kind, decision.Verbs{}) {
			items = append(items, OpenDecisionLine(h.Story, d))
		}
	}
	shown, omitted := bounded(items)
	if len(shown) == 0 && omitted == 0 {
		return
	}
	buf.WriteString("OPEN DECISIONS (still open, folded from the durable status logs - not just the latest line):\n")
	for _, l := range shown {
		buf.WriteString(l + "\n")
	}
	if omitted > 0 {
		fmt.Fprintf(buf, "OPEN DECISIONS: %d more omitted (byte cap)\n", omitted)
	}
	buf.WriteString("OPEN DECISIONS: close one by answering it: cox reply <story> <qNNN> '<answer>' for a [key=qNNN] question; otherwise steer the worker to report resolved [key=<key>]: <answer>\n")
}

// bounded caps each item and keeps items while the section budget allows, counting the rest.
func bounded(items []string) (shown []string, omitted int) {
	used := 0
	for _, it := range items {
		it = CapLine(it, itemCap)
		if used+len(it)+1 > sectionCap {
			omitted++
			continue
		}
		shown = append(shown, it)
		used += len(it) + 1
	}
	return shown, omitted
}

// backstopSection prints STATUS OUTCOME BACKSTOP (fm-wake-drain.sh:307): per story, the newest recognised status
// event, when it is captain-facing, not a parseable decision (those belong to the fold), not yet receipted, and not
// covered by a wake row the leader handled or is being shown now. It returns the receipts to commit once presented.
func backstopSection(buf *bytes.Buffer, epicDir string, hist []History) (map[string]int, error) {
	receipts, err := readReceipts(epicDir)
	if err != nil {
		return nil, err
	}
	rows, err := Load(epicDir)
	if err != nil {
		return nil, err
	}
	acked, err := Acked(epicDir)
	if err != nil {
		return nil, err
	}
	handled, err := Handled(epicDir)
	if err != nil {
		return nil, err
	}
	var items []string
	commit := map[string]int{}
	for _, h := range hist {
		idx := latestEvent(h.Lines)
		if idx < 0 || receipts[h.Story] > idx {
			continue
		}
		event := h.Lines[idx]
		if !decision.CaptainRelevant(event, "") {
			continue
		}
		if v := decision.Verb(event); v == "needs-decision" || v == "blocked" {
			if _, ok := decision.Key(event); ok {
				continue
			}
		}
		if covered(h, idx, rows, acked, handled) {
			continue
		}
		items = append(items, h.Story+" "+event)
		commit[h.Story] = idx + 1
	}
	shown, omitted := bounded(items)
	if len(shown) == 0 && omitted == 0 {
		return nil, nil
	}
	buf.WriteString("STATUS OUTCOME BACKSTOP (newest captain-facing task event has no handled wake):\n")
	for _, l := range shown {
		buf.WriteString(l + "\n")
	}
	if omitted > 0 {
		fmt.Fprintf(buf, "STATUS OUTCOME BACKSTOP: %d more omitted (byte cap)\n", omitted)
	}
	return commit, nil
}

// latestEvent is the index of the newest recognised event (decision.Latest), -1 for an empty history.
func latestEvent(lines []string) int {
	last, fallback := -1, -1
	for i, l := range lines {
		if strings.TrimSpace(l) == "" {
			continue
		}
		fallback = i
		if decision.IsEvent(l, "") {
			last = i
		}
	}
	if last >= 0 {
		return last
	}
	return fallback
}

// covered reports that the event at idx has a wake row the leader handled (gen <= the handled cursor) or is being
// shown now (unacked). The n-th occurrence of a note matches the n-th row carrying it, so an older handled copy never
// covers a newer event whose own row was lost.
func covered(h History, idx int, rows []Wake, acked, handled int) bool {
	note := h.Notes[idx]
	n := 0
	for i := 0; i <= idx; i++ {
		if h.first[i] && h.Notes[i] == note {
			n++
		}
	}
	seen := 0
	for _, w := range rows {
		if w.Story == h.Story && note != "" && strings.Contains(w.text(), note) {
			seen++
			if seen == n {
				return w.Gen <= handled || w.Gen > acked
			}
		}
	}
	return false
}

func receiptPath(epicDir string) string { return filepath.Join(epicDir, ControlDir, "wake.backstop") }

func readReceipts(epicDir string) (map[string]int, error) {
	b, err := os.ReadFile(receiptPath(epicDir))
	if os.IsNotExist(err) {
		return map[string]int{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read backstop receipts: %w", err)
	}
	m := map[string]int{}
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("parse backstop receipts: %w", err)
	}
	return m, nil
}

// commitReceipts records each presented event's history position (causal position, never a timestamp), under the
// queue lock, by atomic replace.
func commitReceipts(epicDir string, add map[string]int) error {
	unlock, err := lock(epicDir)
	if err != nil {
		return err
	}
	defer unlock()
	m, err := readReceipts(epicDir)
	if err != nil {
		return err
	}
	for s, n := range add {
		if n > m[s] {
			m[s] = n
		}
	}
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	return state.AtomicWrite(receiptPath(epicDir), append(b, '\n'), 0o644)
}

func handledPath(epicDir string) string { return filepath.Join(epicDir, ControlDir, "wake.handled") }

// Handled is the highest gen an acknowledgement named while that row existed: the leader handled every wake at or
// below it. An acknowledgement past every row consumes rows without handling them, so their events stay uncovered
// for the outcome backstop (fm-wake-drain-outcome-backstop).
func Handled(epicDir string) (int, error) {
	b, err := os.ReadFile(handledPath(epicDir))
	if os.IsNotExist(err) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("read wake handled: %w", err)
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		return 0, fmt.Errorf("parse wake handled: %w", err)
	}
	return n, nil
}

// watcherDownBanner is the drain's liveness assertion (fm-wake-drain.sh:226 assert_watcher_liveness, fm-guard.sh
// banner): stories in flight and no live watcher with a fresh beacon.
func watcherDownBanner(epicDir string, alive func() bool) string {
	events, _, err := state.Load(epicDir)
	if err != nil {
		return ""
	}
	inFlight := 0
	for _, s := range state.Fold(events).SortedStories() {
		if s.State == state.Working || s.State == state.InputRequired {
			inFlight++
		}
	}
	if inFlight == 0 || alive() {
		return ""
	}
	rule := "●" + strings.Repeat("━", 71) + "\n"
	return rule + "●  WATCHER DOWN - SUPERVISION IS OFF\n" +
		fmt.Sprintf("●  %d story(ies) in flight, but no live watcher with a fresh beacon holds this epic.\n", inFlight) +
		fmt.Sprintf("●  Repair: cox watch --epic %s --replace\n", epicDir) + rule
}
