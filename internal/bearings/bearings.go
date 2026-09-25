// Package bearings is the leader's session start: one ordered digest composed from cox's sources of truth (the leader
// lease, internal/doctor, the wake queue, the story event log, questions/, BACKLOG.md) plus the three budgeted memory
// files under cox/notes/ and their curation pass. It ports firstmate's bin/fm-session-start.sh, the stow skill and the
// startup-memory budget verbatim (firstmate@1e0e773, epic cox-supervision-port); the port cases in port_test.go are
// its specification. Firstmate names map to cox names: home -> workspace, fleet lock -> leader lease, bootstrap ->
// internal/doctor, state/*.status -> story status wakes, data/backlog.md -> BACKLOG.md, data/*.md -> cox/notes/*.md.
package bearings

import "time"

// Opts is one digest invocation.
type Opts struct {
	Workspace   string            // workspace root: AGENTS.md, cox/, BACKLOG.md, the epics
	LeaderID    string            // this session's leader identity (ORCA_TERMINAL_HANDLE in cox)
	Live        func(string) bool // is a leader identity live (the backend probe); nil = every identity is live
	Harness     string            // claude | codex | pi
	Source      string            // "" or startup (true start) | resume | compact | clear
	Reemit      bool              // re-print the digest without startup's sweeps (compact, clear)
	StatusTail  int               // 0 = DefaultStatusTail
	QueuedLimit int               // 0 = DefaultQueuedLimit
	Timeout     time.Duration     // runtime bound; 0 = DefaultTimeout
	Forge       func() error      // the deferred forge/auth probe; nil = no forge check configured
	Endpoint    func(epic, story string) (alive bool, handle string)
	StateRead   func(epic, story string) (string, error) // the slow current-state read for a working story (deferred)
	StageCmd    map[string][]string                      // test seam: an extra subprocess a named stage runs
	// Detach launches the deferred stage in a detached worker process (the CLI) instead of in-process Forge/StateRead;
	// its result arrives as wakes. It is called only on a locked, non-re-emit start.
	Detach func() error
}

// Digest is the printed session-start digest.
type Digest struct {
	Text      string
	ReadOnly  bool
	Truncated bool
}

// StoryState is one story's state as the fleet section prints it.
type StoryState struct {
	Story         string
	State         string
	OpenQuestions []string
}

// BudgetReport is the startup-memory accounting for one workspace.
type BudgetReport struct {
	Budget int            // effective allowance, estimated tokens
	Files  map[string]int // workspace-relative path -> ceil(bytes/3); an absent file has no key
	Total  int
	Status string // within-budget | over-budget
}

// Entry is one classified memory entry.
type Entry struct {
	Tier       string // pinned | aging | perishable | grace
	Reinforced string // YYYY-MM-DD, "" for pinned and grace
	Passes     int    // unreinforced-pass counter (pass horizon only)
	Stale      bool
}

// Receipt is a curation pass's completion receipt (stow SKILL.md "Completion receipt").
type Receipt struct {
	Before, After BudgetReport
	Actions       map[string][]string // memory file -> unchanged | added | rewritten | pruned | routed | archived | proposed-offload
	Archived      []string            // archive lines written this pass
	Exceptions    []string
	Decision      string // the captain decision opened for an unresolved over-budget result, "" when none
	ResetSafe     bool
}

// Estimate is the stable local startup-memory estimate, ceil(UTF-8 bytes / 3) (firstmate docs/configuration.md:287).
func Estimate(bytes int) int { return (bytes + 2) / 3 }
