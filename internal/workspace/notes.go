package workspace

import "path/filepath"

// The leader's startup memory is three files under cox/notes/, ported from firstmate's data/captain.md,
// data/captain-shared.md and data/learnings.md (captain ruling 2026-09-24, epic cox-supervision-port). Their curation
// contract (tiers, markers, decay, archive) is `cox bearings curate`; the startup digest prints them in this order.
const (
	NotesDir           = "notes"             // under ControlDir
	NotesCaptain       = "captain.md"        // default tier pinned: preferences, authority, standing rulings
	NotesCaptainShared = "captain-shared.md" // default tier pinned: preferences shared across workspaces
	NotesLearnings     = "learnings.md"      // default tier aging: operational facts that must re-prove themselves
	NotesArchive       = "memory-archive.md" // the cold tier: append-only, never printed, never budget-counted
	NotesBudgetFile    = "notes-budget"      // under ControlDir: one positive integer and one newline
	NotesHorizonFile   = "notes-pass-horizon"

	// DefaultNotesBudget is the allowance, in estimated tokens, for the three memory files together
	// (firstmate docs/configuration.md "Startup memory budget", default 7500).
	DefaultNotesBudget = 7500
)

// NotesFiles lists the three budgeted memory files in digest order.
var NotesFiles = []string{NotesCaptain, NotesCaptainShared, NotesLearnings}

// NotesPath is a memory file's workspace-relative path, e.g. cox/notes/captain.md.
func NotesPath(name string) string { return filepath.Join(ControlDir, NotesDir, name) }

// NotesBudgetPath is the budget setting's workspace-relative path, cox/notes-budget.
func NotesBudgetPath() string { return filepath.Join(ControlDir, NotesBudgetFile) }

// NotesHorizonPath is the optional pass-horizon presence flag, cox/notes-pass-horizon.
func NotesHorizonPath() string { return filepath.Join(ControlDir, NotesHorizonFile) }
