// Package forge is the adapter seam to a code forge (GitHub). The core (internal/verdict) depends only on the Forge
// interface, never on gh, so audit and ship logic is unit-tested against a fake that replays fixtures. Every method
// returns a real error on a retrieval failure; the verdict layer turns any such error into `unknown`, never a guessed
// pass (P5, F12).
package forge

// PR identifies a pull request and its head. Head is the head commit sha, used to bind a verdict to the revision it
// was computed against so a later push makes the verdict stale.
type PR struct {
	Number    int
	HeadRef   string
	Head      string // head commit sha
	Base      string
	State     string // open | merged | closed
	Draft     bool   // true while the PR is a draft (not ready for merge)
	Mergeable bool   // true only when the forge reports the PR cleanly mergeable (no conflicts, mergeable state known)
}

// Check is one CI check run. Status is the run status (queued | in_progress | completed); Conclusion is set only when
// Status is completed (success | failure | cancelled | ...). StartedAt/CompletedAt are RFC 3339 timestamps used to
// measure CI wall clock including queue wait; either is "" when the forge does not report it (still queued/running, or
// no timing available).
type Check struct {
	Name        string
	Status      string
	Conclusion  string
	StartedAt   string
	CompletedAt string
}

// Comment is one review comment. Author is the login (e.g. a bot "rs-pr-reviewer"); Path/Line locate an inline comment.
type Comment struct {
	Author string
	Body   string
	Path   string
	Line   int
}

// Forge is the surface the verdict layer needs. A method that cannot retrieve its data returns a non-nil error; it
// never returns an empty-but-nil "all clear".
type Forge interface {
	// PR resolves the pull request for a head branch. A missing PR is an error, not an empty PR.
	PR(headRef string) (PR, error)
	// Diff returns the unified diff text of the PR.
	Diff(pr PR) (string, error)
	// Checks returns the CI check runs for the PR head.
	Checks(pr PR) ([]Check, error)
	// Comments returns the PR's review comments and threads.
	Comments(pr PR) ([]Comment, error)
	// Merged reports whether the PR is merged, read live from the forge (never from the passed struct, which may predate a
	// merge).
	Merged(pr PR) (bool, error)
	// Merge merges the PR with its head sha pinned (the equivalent of --match-head-commit), so a push between the read and
	// the merge is rejected by the forge rather than silently merging a different head. method is "squash"|"merge"|"rebase".
	// A merge that the forge rejects (head moved, not mergeable, checks not satisfied) returns a non-nil error; the caller
	// treats that as a refusal, never a guessed success.
	Merge(pr PR, method string) error
}
