package bearings

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/nphattai/coxswain/internal/workspace"
)

// Tier clocks, verbatim from firstmate's stow skill (.agents/skills/stow/SKILL.md:38-39, :70-71).
const (
	AgingDays        = 30
	PerishableDays   = 7
	AgingPasses      = 10 // only with cox/notes-pass-horizon
	PerishablePasses = 3  // only with cox/notes-pass-horizon
)

// HeaderPointer is the one-line pointer each memory file's header carries to the owner of the tier scheme (stow
// SKILL.md:48). docs/handoff.md owns tier semantics, marker spellings and clocks; no memory file restates them.
const HeaderPointer = "<!-- memory tiers: see docs/handoff.md -->"

// markerRe matches a trailing tier marker: <!--a:YYYY-MM-DD[/N]-->, <!--p:YYYY-MM-DD[/N]-->, <!--P-->, <!--g-->.
var markerRe = regexp.MustCompile(`\s*<!--(?:([ap]):(\d{4}-\d{2}-\d{2})(?:/(\d+))?|(P)|(g))-->\s*$`)

// marker is one parsed entry line.
type marker struct {
	text    string // the entry without its marker
	letter  string // a | p | P | g | "" (unmarked)
	date    string
	passes  int
	counter string // the raw "/N" suffix, kept verbatim when the horizon is off
}

func parseMarker(line string) marker {
	m := markerRe.FindStringSubmatchIndex(line)
	if m == nil {
		return marker{text: strings.TrimRight(line, " \t")}
	}
	sub := func(i int) string {
		if m[2*i] < 0 {
			return ""
		}
		return line[m[2*i]:m[2*i+1]]
	}
	k := marker{text: line[:m[0]]}
	switch {
	case sub(4) != "":
		k.letter = "P"
	case sub(5) != "":
		k.letter = "g"
	default:
		k.letter, k.date = sub(1), sub(2)
		if c := sub(3); c != "" {
			k.passes, _ = strconv.Atoi(c)
			k.counter = "/" + c
		}
	}
	return k
}

// render writes the entry back with its marker; an unmarked pinned-default entry carries none.
func (k marker) render() string {
	switch k.letter {
	case "":
		return k.text
	case "P", "g":
		return k.text + " <!--" + k.letter + "-->"
	}
	return k.text + " <!--" + k.letter + ":" + k.date + k.counter + "-->"
}

// defaultTier is a memory file's default tier (stow SKILL.md:45): the captain files are pinned, learnings age.
func defaultTier(file string) (string, error) {
	switch file {
	case workspace.NotesCaptain, workspace.NotesCaptainShared:
		return "pinned", nil
	case workspace.NotesLearnings:
		return "aging", nil
	}
	return "", fmt.Errorf("bearings: %q is not a memory file", file)
}

var tierOfLetter = map[string]string{"a": "aging", "p": "perishable", "P": "pinned", "g": "grace"}

// daysSince counts whole calendar days from a YYYY-MM-DD date to now.
func daysSince(date string, now time.Time) (int, error) {
	d, err := time.Parse("2006-01-02", date)
	if err != nil {
		return 0, err
	}
	y, mo, da := now.Date()
	today := time.Date(y, mo, da, 0, 0, 0, 0, time.UTC)
	return int(today.Sub(d).Hours() / 24), nil
}

// staleness judges a dated entry against its tier clocks; byPasses reports that the pass horizon alone made it stale.
func staleness(tier string, days, passes int, horizon bool) (stale, byPasses bool) {
	dayLimit, passLimit := AgingDays, AgingPasses
	if tier == "perishable" {
		dayLimit, passLimit = PerishableDays, PerishablePasses
	}
	byDays := days >= dayLimit
	byPasses = horizon && passes >= passLimit && !byDays
	return byDays || byPasses, byPasses
}

// Classify reads one entry line of a memory file (captain.md, captain-shared.md, learnings.md) under that file's
// default tier. The unreinforced-pass counter is read only when the pass horizon is on; pinned reads no clock.
func Classify(file, line string, now time.Time, horizon bool) (Entry, error) {
	def, err := defaultTier(file)
	if err != nil {
		return Entry{}, err
	}
	k := parseMarker(line)
	if k.letter == "" {
		return Entry{Tier: def}, nil
	}
	e := Entry{Tier: tierOfLetter[k.letter], Reinforced: k.date}
	if k.date == "" {
		return e, nil
	}
	if horizon {
		e.Passes = k.passes
	}
	days, err := daysSince(k.date, now)
	if err != nil {
		return Entry{}, fmt.Errorf("bearings: bad marker date in %q: %w", line, err)
	}
	e.Stale, _ = staleness(e.Tier, days, e.Passes, horizon)
	return e, nil
}
