package bearings

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"

	"github.com/nphattai/coxswain/internal/adapter/harness/pi"
	"github.com/nphattai/coxswain/internal/doctor"
	"github.com/nphattai/coxswain/internal/wake"
	"github.com/nphattai/coxswain/internal/workspace"
)

const (
	rule    = "================================================================================"
	subrule = "--------------------------------------------------------------------------------"
	bar     = "●━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"

	// PiLoadedMarker is the Pi leader extension's loaded proof, written by the running extension: line 1 the extension
	// version (pi.ExtensionHash), line 2 the loading pi pid, then optional key=value lines.
	PiLoadedMarker = "pi-leader-extension-loaded"
)

// printer accumulates the digest. It is sealed when the runtime bound expires, so a stage still running afterwards
// can never append to what was already delivered.
type printer struct {
	mu     sync.Mutex
	b      strings.Builder
	sealed bool
}

func (p *printer) raw(s string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.sealed {
		p.b.WriteString(s)
	}
}

func (p *printer) line(s string)      { p.raw(s + "\n") }
func (p *printer) section(t string)   { p.raw("\n" + rule + "\n" + t + "\n" + rule + "\n") }
func (p *printer) sub(label string)   { p.raw("\n" + label + "\n" + subrule + "\n") }
func (p *printer) lines(ls ...string) { p.raw(strings.Join(ls, "\n") + "\n") }

// seal freezes the digest and returns it; first is false when it was already sealed (by completion or by the bound).
func (p *printer) seal() (text string, first bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	first = !p.sealed
	p.sealed = true
	return p.b.String(), first
}

func (p *printer) text() string { p.mu.Lock(); defer p.mu.Unlock(); return p.b.String() }

// DoctorSummary is the detect-only doctor diagnostics for one workspace (internal/doctor.InspectWorkspace): the
// workspace and policy validity, repo checkouts, stray policies, missing leader hooks, and active epics whose watcher
// is not running. It never mutates anything.
func DoctorSummary(ws string) (string, error) {
	rep := doctor.InspectWorkspace(ws)
	var issues []string
	if rep.Error != "" {
		issues = append(issues, "workspace: "+rep.Error)
	}
	if rep.PolicyError != "" {
		issues = append(issues, "policy: "+rep.PolicyError)
	}
	issues = append(issues, rep.RepoIssues...)
	for _, a := range rep.PolicyInRepo {
		issues = append(issues, "repo "+a+" carries a stray cox/policy.json (the workspace copy is the one read)")
	}
	for h, ok := range rep.Hooks {
		if !ok {
			issues = append(issues, fmt.Sprintf("leader hooks for %s are not installed (cox workspace hooks --harness %s)", h, h))
		}
	}
	for _, ep := range rep.Epics {
		if !ep.Closed && !epicClosed(ep.Path) && !ep.WatcherAlive {
			issues = append(issues, fmt.Sprintf("epic %s: watcher not running (%s)", ep.Slug, ep.Path))
		}
	}
	if len(issues) == 0 {
		return "(silent - all good)", nil
	}
	return strings.Join(issues, "\n"), nil
}

// WakeDrain is cox wake drain's source of truth: the unacked wakes of an epic, in gen order. It never acks.
func WakeDrain(epicDir string) ([]wake.Wake, error) { return wake.Drain(epicDir, true) }

// PidAlive reports whether a process exists (a signal-0 probe; EPERM still means it exists).
func PidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// piExtensionLoaded proves the Pi leader extension is loaded in the running pi process, not merely installed: the
// marker's version is the current extension build, its pid is a live process, it is not a handoff-generation marker,
// and the turn-end guard is present.
func piExtensionLoaded(ws string) bool {
	b, err := os.ReadFile(filepath.Join(ws, RuntimeDir, PiLoadedMarker))
	if err != nil {
		return false
	}
	ls := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	if len(ls) < 2 || strings.TrimSpace(ls[0]) != pi.ExtensionHash() {
		return false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(ls[1]))
	if err != nil || !PidAlive(pid) {
		return false
	}
	for _, l := range ls[2:] {
		for _, f := range strings.Fields(l) {
			if f == "phase=handoff" || f == "turnend=absent" {
				return false
			}
		}
	}
	return true
}

// printFileOrAbsent prints a file under a labelled subsection, distinguishing an absent file (ABSENT) from an empty one
// ((present, empty)) - absence is meaningful and must never read as empty.
func printFileOrAbsent(p *printer, path, label string) {
	p.sub(label)
	b, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		p.line("ABSENT")
	case err != nil:
		p.line(fmt.Sprintf("unreadable: %v", err))
	case len(b) == 0:
		p.line("(present, empty)")
	default:
		p.raw(strings.TrimRight(string(b), "\n") + "\n")
	}
}

// Notes renders the NOTES section: the three memory files in digest order and the budget line.
func Notes(ws string, _ int) (string, error) {
	p := &printer{}
	printNotes(p, ws)
	return p.text(), nil
}

func printNotes(p *printer, ws string) {
	p.section("NOTES")
	for _, f := range workspace.NotesFiles {
		printFileOrAbsent(p, filepath.Join(ws, workspace.NotesPath(f)), workspace.NotesPath(f))
	}
	p.line("")
	budget, berr := ReadBudget(ws)
	rep, merr := measureAll(ws, budget)
	switch {
	case merr != nil:
		p.line("startup memory: " + merr.Error())
	case berr != nil:
		p.line(fmt.Sprintf("startup memory: %d estimated tokens; %s is not usable (%s) - cox bearings curate materializes the %d default or reports the exception",
			rep.Total, workspace.NotesBudgetPath(), strings.TrimPrefix(berr.Error(), "startup-memory-budget: "), workspace.DefaultNotesBudget))
	default:
		p.line(fmt.Sprintf("startup memory: %d of %d estimated tokens, %s (ceil(UTF-8 bytes / 3) per file; curate with cox bearings curate)",
			rep.Total, rep.Budget, rep.Status))
	}
}

// printSupervision prints exactly one operating block for the leader harness.
func printSupervision(p *printer, ws, harness string, readOnly bool) {
	if harness == "" {
		harness = "unknown"
	}
	p.section("SUPERVISION OPERATING INSTRUCTIONS - leader harness: " + harness)
	if harness == "pi" {
		if piExtensionLoaded(ws) {
			p.line("PI_LEADER_EXTENSION: loaded")
		} else {
			p.line("PI_LEADER_EXTENSION: not loaded - restart pi in this workspace so the cox leader extension auto-loads (install it with cox workspace hooks --harness pi); without it there is no turn-end guard and no background wake coverage")
		}
	}
	if readOnly {
		p.line("This session is read-only: read the wake queue and the fleet state, but do not drain, acknowledge,")
		p.line("arm a watcher, dispatch, steer, merge, or repair from here.")
		return
	}
	switch harness {
	case "codex":
		p.lines("Pull harness (docs/adapters/codex.md): every turn starts with cox wake drain --epic <dir>; handle each",
			"wake, then cox wake ack-through <gen> --epic <dir>. When nothing is left to do, make the last tool call",
			"cox wake wait --max 25m --epic <dir>, so the next turn sees the wake without polling.")
	case "pi":
		p.lines("Push harness (docs/adapters/pi.md): the cox leader extension attaches unread wakes before each turn and",
			"reopens a turn when an urgent wake is queued. Every turn starts with cox wake drain --epic <dir>; handle",
			"each wake, then cox wake ack-through <gen> --epic <dir>. Never run cox wake wait.")
	default:
		p.lines("Push harness (docs/adapters/claude.md): the UserPromptSubmit hook attaches unread wakes to each turn and",
			"the Stop hook reopens a turn when an urgent wake is queued. Every turn starts with cox wake drain --epic",
			"<dir>; handle each wake, then cox wake ack-through <gen> --epic <dir>. Never run cox wake wait.")
	}
}

const readOnceContract = `Everything below is printed in full for this session start: every active epic's story
inventory with its cox state row, a compact BACKLOG.md listing, a bounded tail of every story's status,
cox/notes/captain.md, cox/notes/captain-shared.md, and cox/notes/learnings.md.
Do NOT re-read any of them after reading this digest, and do NOT bulk-read
BACKLOG.md, the stories/ files, or the wake log: re-reading everything defeats the entire
point of this command.

Go to a source directly only when:
  - this digest flagged it ABSENT (then create it per docs/handoff.md),
  - its contents looked unparseable or corrupt,
  - an older status line is needed, or a status line was capped and its tail
    matters (each story's full wake log path is printed with its tail),
  - a full backlog row or its evidence is needed (BACKLOG.md),
  - the backlog listing disclosed omitted open rows and this turn needs them,
  - the FORGE CHECKS section reported its checks still IN PROGRESS and this
    turn needs their verdict (the result arrives as a startup-forge wake),
  - or a STARTUP TRUNCATED banner named the stage that would have printed it, in
    which case that stage's sources were never emitted and must be reconciled.`

// watcherDown lists the active epics whose watcher is not running.
func watcherDown(epics []string) []string {
	var out []string
	for _, ep := range epics {
		b, _ := os.ReadFile(filepath.Join(ep, wake.ControlDir, "watch.pid"))
		pid, _ := strconv.Atoi(strings.TrimSpace(string(b)))
		if !PidAlive(pid) {
			out = append(out, ep)
		}
	}
	return out
}

func printNextStep(p *printer, harness string, readOnly bool, epics []string) {
	p.section("NEXT STEP")
	if readOnly {
		p.lines("This session did not acquire the leader lease. Stay read-only: do not arm,",
			"drain, dispatch, steer, merge, or repair fleet state from here. A read-only session",
			"must leave repair work to the session holding the leader lease; only a session",
			"with verified leader-lease ownership may perform mutable follow-up.", "")
	} else {
		p.lines("Handle every record in the WAKE QUEUE section above, then acknowledge through the generation it",
			"names; on later turns drain them with cox wake drain --epic <dir> before anything else.")
		if down := watcherDown(epics); len(down) > 0 {
			p.line("After draining queued wakes, start the watcher of each epic whose watcher is not running:")
			for _, ep := range down {
				p.line("  cox watch --epic " + ep)
			}
		} else {
			p.line("After draining queued wakes, the watcher of every active epic is already running.")
		}
		p.lines(fmt.Sprintf("Follow the supervision operating instructions block above for harness '%s'.", harness),
			"This command never starts supervision itself.", "")
	}
	p.lines("The digest above is complete for this session start. The READ-ONCE CONTRACT",
		"section near the top of it governs what may still be read from disk.")
}
