package bearings

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/nphattai/coxswain/internal/workspace"
)

// noteFile is one memory file held in memory for a pass.
type noteFile struct {
	name, path string
	present    bool
	lines      []string
	removed    map[int]bool
	actions    []string
}

func (f *noteFile) act(a string) {
	for _, x := range f.actions {
		if x == a {
			return
		}
	}
	f.actions = append(f.actions, a)
}

func (f *noteFile) content() string {
	var out []string
	for i, l := range f.lines {
		if !f.removed[i] {
			out = append(out, l)
		}
	}
	return strings.Join(out, "\n")
}

func isEntry(line string) bool { return strings.HasPrefix(line, "- ") || strings.HasPrefix(line, "* ") }

// evidenced reports whether this session named the entry as reinforced (the judgment half stays with the leader: it
// passes the entry, or its leading words, only when it can name independent session evidence - stow SKILL.md:99).
func evidenced(text string, reinforced []string, matched map[string]bool) bool {
	text = stripBullet(text)
	for _, r := range reinforced {
		r = stripBullet(r)
		if r != "" && (text == r || strings.HasPrefix(text, r+" ")) {
			matched[r] = true
			return true
		}
	}
	return false
}

// stripBullet drops a leading "- " or "* " so a reinforced entry matches with or without its bullet.
func stripBullet(s string) string {
	s = strings.TrimSpace(s)
	for _, b := range []string{"- ", "* "} {
		if t, ok := strings.CutPrefix(s, b); ok {
			return strings.TrimSpace(t)
		}
	}
	return s
}

func archiveLine(file, tier, date, text, reason string) string {
	if date == "" {
		date = "none"
	}
	text = strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(text, "- "), "* "))
	return fmt.Sprintf("- (from %s, tier: %s, reinforced: %s) %s [archived: %s]", file, tier, date, text, reason)
}

func notesTotal(files []*noteFile) int {
	n := 0
	for _, f := range files {
		if f.present {
			n += Estimate(len(f.content()))
		}
	}
	return n
}

// Curate runs the mechanical half of the stow pass (stow SKILL.md "Required startup-memory pass", the cold tier, the
// one-time migration and the completion receipt) over cox/notes/: report, header pointer, reinforcement, pass tick,
// decay archive, grace migration, budget eviction behind the convergence precondition, report again, receipt.
// reinforced names the entries this session evidenced. An absent memory file is never created; a pinned entry is
// never moved; nothing leaves memory except by a move into cox/notes/memory-archive.md with provenance.
func Curate(ws string, now time.Time, reinforced []string) (Receipt, error) {
	r := Receipt{Actions: map[string][]string{}}
	for _, f := range workspace.NotesFiles {
		r.Actions[f] = []string{"unchanged"}
	}
	// Step 1: report before any write. A rejected setting or memory file is a concrete exception, never a default.
	if err := MaterializeBudget(ws); err != nil {
		return rejected(r, err)
	}
	before, err := Budget(ws)
	if err != nil {
		return rejected(r, err)
	}
	r.Before = before
	today := now.Format("2006-01-02")
	_, herr := os.Lstat(filepath.Join(ws, workspace.NotesHorizonPath()))
	horizon := herr == nil

	// Step 2: read every memory file completely.
	var files []*noteFile
	for _, name := range workspace.NotesFiles {
		f := &noteFile{name: name, path: filepath.Join(ws, workspace.NotesPath(name)), removed: map[int]bool{}}
		b, err := os.ReadFile(f.path)
		switch {
		case errors.Is(err, os.ErrNotExist):
		case err != nil:
			return rejected(r, err)
		default:
			f.present, f.lines = true, strings.Split(string(b), "\n")
		}
		files = append(files, f)
	}

	var archived []string
	matched := map[string]bool{}
	type candidate struct {
		f    *noteFile
		i    int
		date string
		text string
	}
	var pool []candidate
	for _, f := range files {
		if !f.present {
			continue
		}
		def, _ := defaultTier(f.name)
		// Header pointer: at most one line, added once, corrected in place wherever it sits.
		at := -1
		for i, l := range f.lines {
			if strings.Contains(l, "memory tiers:") {
				at = i
				break
			}
		}
		switch {
		case at < 0:
			f.lines = append([]string{HeaderPointer}, f.lines...)
			f.act("rewritten")
		case f.lines[at] != HeaderPointer:
			f.lines[at] = HeaderPointer
			f.act("rewritten")
		}
		for i, line := range f.lines {
			if !isEntry(line) {
				continue
			}
			k := parseMarker(line)
			ev := evidenced(k.text, reinforced, matched)
			switch {
			case k.letter == "P" || (k.letter == "" && def == "pinned"):
				continue // no clock is ever read for a pinned entry
			case k.letter == "g":
				// Only an entry already carrying <!--g--> at pass start resolves: evidence stamps it, else it archives.
				if ev {
					k.letter, k.date, k.counter = "a", today, ""
					f.lines[i] = k.render()
					f.act("rewritten")
				} else {
					archived = append(archived, archiveLine(f.name, "grace", "", k.text, "legacy-unvalidated"))
					f.removed[i] = true
					f.act("archived")
				}
				continue
			case k.letter == "":
				// An unmarked entry in a clock-carrying file: evidence stamps today, else one grace cycle.
				if ev {
					k.letter, k.date = "a", today
				} else {
					k.letter = "g"
				}
				f.lines[i] = k.render()
				f.act("rewritten")
				continue
			}
			// A dated aging or perishable entry.
			if ev {
				k.date = today
				if horizon {
					k.counter, k.passes = "", 0
				}
			} else if horizon {
				k.passes++ // the pass tick
				k.counter = fmt.Sprintf("/%d", k.passes)
			}
			if nl := k.render(); nl != line {
				f.lines[i] = nl
				f.act("rewritten")
			}
			tier := tierOfLetter[k.letter]
			days, err := daysSince(k.date, now)
			if err != nil {
				return rejected(r, fmt.Errorf("bad marker date in %s: %q", f.name, line))
			}
			if stale, byPasses := staleness(tier, days, k.passes, horizon); stale && !ev {
				reason := fmt.Sprintf("unreinforced %dd", days)
				if byPasses {
					reason = fmt.Sprintf("unreinforced %dp", k.passes)
				}
				archived = append(archived, archiveLine(f.name, tier, k.date, k.text, reason))
				f.removed[i] = true
				f.act("archived")
				continue
			}
			if k.letter == "a" {
				pool = append(pool, candidate{f, i, k.date, k.text})
			}
		}
	}

	// Step 7: budget eviction of dated aging entries, oldest-reinforced first, only when the whole eligible pool can
	// close the gap (the convergence precondition). A grace entry is never eligible.
	if notesTotal(files) > before.Budget {
		sort.SliceStable(pool, func(a, b int) bool { return pool[a].date < pool[b].date })
		for _, c := range pool {
			c.f.removed[c.i] = true
		}
		converges := notesTotal(files) <= before.Budget
		for _, c := range pool {
			c.f.removed[c.i] = false
		}
		if converges {
			for _, c := range pool {
				if notesTotal(files) <= before.Budget {
					break
				}
				tier := tierOfLetter["a"]
				archived = append(archived, archiveLine(c.f.name, tier, c.date, c.text, "budget oldest-first"))
				c.f.removed[c.i] = true
				c.f.act("archived")
			}
		}
	}

	// The move: archive first, then rewrite memory, so a fact is never in neither place.
	if len(archived) > 0 {
		if err := appendArchive(ws, today, archived); err != nil {
			return rejected(r, err)
		}
	}
	for _, f := range files {
		if !f.present {
			continue
		}
		if len(f.actions) > 0 {
			if err := writeAtomic(f.path, f.content()); err != nil {
				return rejected(r, err)
			}
			r.Actions[f.name] = f.actions
		}
	}
	r.Archived = archived
	// A reinforcement that named no entry is an exception: the session evidenced something memory does not hold.
	for _, want := range reinforced {
		if w := stripBullet(want); w != "" && !matched[w] {
			r.Exceptions = append(r.Exceptions, "reinforced entry not found in any memory file: "+w)
		}
	}

	// Step 8: report again; finish within budget or open a concrete captain decision.
	after, err := Budget(ws)
	if err != nil {
		return rejected(r, err)
	}
	r.After = after
	if after.Status == "over-budget" {
		r.Decision = overBudgetDecision(files, after)
	}
	r.ResetSafe = after.Status == "within-budget" && len(r.Exceptions) == 0 && r.Decision == ""
	return r, nil
}

func rejected(r Receipt, err error) (Receipt, error) {
	r.Exceptions = append(r.Exceptions, err.Error())
	r.ResetSafe = false
	return r, nil
}

// overBudgetDecision names the shortfall and the exempt pinned floor, with exactly the two options stow SKILL.md:124
// allows.
func overBudgetDecision(files []*noteFile, after BudgetReport) string {
	var pinned []string
	for _, f := range files {
		def, _ := defaultTier(f.name)
		for i, l := range f.lines {
			if f.removed[i] || !isEntry(l) {
				continue
			}
			if k := parseMarker(l); k.letter == "P" || (k.letter == "" && def == "pinned") {
				t := k.text
				if len(t) > 80 {
					t = t[:80] + "..."
				}
				pinned = append(pinned, f.name+": "+t)
			}
		}
	}
	return fmt.Sprintf("startup memory is %d estimated tokens over its %d budget (total %d) after every safe archival and "+
		"eviction; the exempt pinned floor crowds it out: [%s]. Captain, choose: raise the effective budget in %s, or "+
		"explicitly approve offloading or trimming a named pinned entry.",
		after.Total-after.Budget, after.Budget, after.Total, strings.Join(pinned, "; "), workspace.NotesBudgetPath())
}

// appendArchive appends archived entries under one dated pass heading to the append-only cold tier.
func appendArchive(ws, today string, lines []string) error {
	path := filepath.Join(ws, workspace.NotesPath(workspace.NotesArchive))
	fi, err := os.Stat(path)
	head := "## " + today + " notes pass\n"
	if err == nil && fi.Size() > 0 {
		head = "\n" + head
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open memory archive: %w", err)
	}
	defer f.Close()
	if _, err := f.WriteString(head + strings.Join(lines, "\n") + "\n"); err != nil {
		return fmt.Errorf("write memory archive: %w", err)
	}
	return f.Sync()
}

// writeAtomic replaces a file by a synced temp file and a rename, keeping an existing file's mode.
func writeAtomic(path, content string) error {
	mode := os.FileMode(0o644)
	if fi, err := os.Stat(path); err == nil {
		mode = fi.Mode().Perm()
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.WriteString(content); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), mode); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
