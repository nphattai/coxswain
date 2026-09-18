package verdict

import (
	"sort"
	"strings"

	"github.com/nphattai/coxswain/internal/adapter/forge"
)

// AuditReport is the proof-of-work bundle for one story PR (plan 5.3). Verdict is bound to Head; a later push (a
// different head) makes a stored verdict stale. CredMatches lists only the names of matched rules, never the secrets.
type AuditReport struct {
	Story       string   `json:"story"`
	PR          int      `json:"pr"`
	Head        string   `json:"head"`
	Verdict     Verdict  `json:"verdict"`
	Reasons     []string `json:"reasons"`
	Shape       Shape    `json:"shape"`
	CI          Verdict  `json:"ci"`
	Reviewer    string   `json:"reviewer,omitempty"`
	CredMatches []string `json:"cred_matches,omitempty"`
	Frames      []string `json:"frames,omitempty"`
}

// Shape is the size of the change: files touched, additions/deletions, and files-per-top-level-dir.
type Shape struct {
	Files     int            `json:"files"`
	Additions int            `json:"additions"`
	Deletions int            `json:"deletions"`
	ByDir     map[string]int `json:"by_dir,omitempty"`
}

// Audit runs the story PR through the forge and returns a three-state report. Any retrieval failure yields Unknown with
// a reason (never a guessed pass, F12). Decision order: a retrieval error is Unknown; then a credential match, a
// reviewer "changes requested", or a failed CI is Fail; then pending CI is Unknown; otherwise Pass.
func Audit(f forge.Forge, story, headRef string) AuditReport {
	r := AuditReport{Story: story, Verdict: Unknown}

	pr, err := f.PR(headRef)
	if err != nil {
		r.Reasons = append(r.Reasons, reason("pr", "retrieval failed: "+err.Error()))
		return r
	}
	r.PR, r.Head = pr.Number, pr.Head

	diff, err := f.Diff(pr)
	if err != nil {
		r.Reasons = append(r.Reasons, reason("diff", "retrieval failed: "+err.Error()))
		return r
	}
	checks, err := f.Checks(pr)
	if err != nil {
		r.Reasons = append(r.Reasons, reason("ci", "retrieval failed: "+err.Error()))
		return r
	}
	comments, err := f.Comments(pr)
	if err != nil {
		r.Reasons = append(r.Reasons, reason("comments", "retrieval failed: "+err.Error()))
		return r
	}

	r.Shape = shapeOf(diff)
	r.CI = ciStatus(checks)
	r.Reviewer = reviewerVerdict(comments)
	r.CredMatches = scanCredentials(diff)

	// Fail conditions (definite) take precedence over pending.
	failed := false
	if len(r.CredMatches) > 0 {
		r.Reasons = append(r.Reasons, reason("credentials", "matched rules "+strings.Join(r.CredMatches, ",")))
		failed = true
	}
	if r.Reviewer == "changes-requested" {
		r.Reasons = append(r.Reasons, reason("reviewer", "changes requested"))
		failed = true
	}
	if r.CI == Fail {
		r.Reasons = append(r.Reasons, reason("ci", "a check failed"))
		failed = true
	}
	if failed {
		r.Verdict = Fail
		return r
	}
	if r.CI == Unknown {
		r.Reasons = append(r.Reasons, reason("ci", "pending or no checks; cannot confirm green"))
		r.Verdict = Unknown
		return r
	}
	if r.Reviewer == "review-required" {
		r.Reasons = append(r.Reasons, reason("reviewer", "review required (not blocking)"))
	}
	r.Verdict = Pass
	return r
}

// shapeOf parses a unified diff for the file count, additions/deletions, and files per top-level directory.
func shapeOf(diff string) Shape {
	s := Shape{ByDir: map[string]int{}}
	for _, line := range strings.Split(diff, "\n") {
		switch {
		case strings.HasPrefix(line, "+++ b/"):
			path := strings.TrimPrefix(line, "+++ b/")
			s.Files++
			s.ByDir[topDir(path)]++
		case strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++"):
			s.Additions++
		case strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "---"):
			s.Deletions++
		}
	}
	if len(s.ByDir) == 0 {
		s.ByDir = nil
	}
	return s
}

func topDir(path string) string {
	if i := strings.IndexByte(path, '/'); i >= 0 {
		return path[:i]
	}
	return "."
}

// SortedDirs returns the by-dir keys sorted, for deterministic printing.
func (s Shape) SortedDirs() []string {
	out := make([]string, 0, len(s.ByDir))
	for k := range s.ByDir {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
