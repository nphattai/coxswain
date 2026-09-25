package verdict

import (
	"fmt"
	"time"

	"github.com/nphattai/coxswain/internal/adapter/forge"
)

// MergeState is the three-state outcome of a `cox ship merge` decision, mapped to its exit codes: merged (0), refused
// (1, every failing condition listed), unknown (3, a forge read failed or CI is still pending).
type MergeState string

const (
	MergeDone         MergeState = "merged"
	MergeRefused      MergeState = "refused"
	MergeUndetermined MergeState = "unknown"
)

// MergeInput is everything the decision needs besides the forge. Selector resolves the PR (a number or the head branch);
// EpicBranch and Production are the only bases a merge may land on; Method is squash|merge|rebase; Yolo/Captain/Worker are
// the authorization posture (policy merge.yolo, --captain, and whether the terminal is a worker with COX_STORY set);
// AllowRed names checks the caller waived; Check is --check (read and decide, never merge, never write the ledger).
type MergeInput struct {
	Selector   string
	EpicBranch string
	Production string
	Method     string
	Yolo       bool
	Captain    bool
	Worker     bool
	AllowRed   []string
	Check      bool
}

// MergeReport is the decision plus what it acted on. State maps to the exit code; Reasons lists EVERY failing condition
// (not only the first), so a caller sees all of them at once; Merged is true only after a confirmed read-back.
type MergeReport struct {
	PR      int        `json:"pr"`
	Head    string     `json:"head,omitempty"`
	Method  string     `json:"method"`
	State   MergeState `json:"state"`
	Merged  bool       `json:"merged"`
	Reasons []string   `json:"reasons,omitempty"`
}

// Merge reads the PR live through the forge, gathers every reason it may not merge, and - unless Check - merges it with
// the head pinned (the forge rejects a moved head) and reads the result back, accepting only a confirmed merged. A forge
// read failure or pending CI is unknown, never a guessed pass (F12, P5). The order is: a forge read failure or pending CI
// dominates only when there is no definite refusal; a definite refusal (authority, state, draft, mergeable, base, a
// failed check) refuses; otherwise the merge runs (or, with Check, the verdict says it would).
func Merge(f forge.Forge, in MergeInput) MergeReport {
	r := MergeReport{Method: in.Method, State: MergeRefused}

	pr, err := f.PR(in.Selector)
	if err != nil {
		r.State = MergeUndetermined
		r.Reasons = append(r.Reasons, reason("pr", "retrieval failed: "+err.Error()))
		return r
	}
	r.PR, r.Head = pr.Number, pr.Head

	// Authorization gates. --check is read-only (a dry run against a real PR is allowed), so it skips them.
	if !in.Check {
		if in.Worker {
			r.Reasons = append(r.Reasons, reason("authority", "refused from a worker terminal (COX_STORY set); the captain merges"))
		}
		if !in.Yolo && !in.Captain {
			r.Reasons = append(r.Reasons, reason("authority", "merge.yolo=false and not --captain; the captain runs cox ship merge"))
		}
	}

	// PR-shape gates.
	if pr.State != "open" {
		r.Reasons = append(r.Reasons, reason("state", "PR is "+orUnknownState(pr.State)+", not open"))
	}
	if pr.Draft {
		r.Reasons = append(r.Reasons, reason("draft", "PR is a draft; mark it ready before merging"))
	}
	if !pr.Mergeable {
		r.Reasons = append(r.Reasons, reason("mergeable", "the forge does not report the PR cleanly mergeable"))
	}
	if !baseAllowed(pr.Base, in.EpicBranch, in.Production) {
		r.Reasons = append(r.Reasons, reason("base", fmt.Sprintf("PR base %q is neither the epic branch %q nor production %q", pr.Base, in.EpicBranch, in.Production)))
	}

	// CI at the live head, honouring waivers. A failed check refuses; pending or no checks is unknown.
	ciPending := false
	checks, err := f.Checks(pr)
	if err != nil {
		r.State = MergeUndetermined
		r.Reasons = append(r.Reasons, reason("checks", "retrieval failed: "+err.Error()))
		return r
	}
	switch CI(waive(checks, in.AllowRed)) {
	case Fail:
		r.Reasons = append(r.Reasons, reason("ci", "a check failed at the live head"))
	case Unknown:
		ciPending = true
	}

	// A definite refusal wins over pending; pending is unknown; otherwise the merge is clear.
	if len(r.Reasons) > 0 {
		r.State = MergeRefused
		return r
	}
	if ciPending {
		r.State = MergeUndetermined
		r.Reasons = append(r.Reasons, reason("ci", "pending or no checks at the live head; cannot confirm green"))
		return r
	}
	if in.Check {
		r.State = MergeDone // the dry-run verdict: it would merge; nothing was merged
		return r
	}

	if err := f.Merge(pr, in.Method); err != nil {
		r.State = MergeRefused
		r.Reasons = append(r.Reasons, reason("merge", "the forge rejected the merge (head moved or not mergeable): "+err.Error()))
		return r
	}
	// The forge accepted the merge, so an outcome it does not report merged is unknown (exit 3), never a refusal: the PR
	// may well have landed (B-59; firstmate keeps the merge poll armed). A re-run refuses a merged PR (state gate), so the
	// reason says how to confirm instead of promising a re-run will.
	var readErr error
	for i := 0; i < ReadBackTries; i++ {
		if i > 0 {
			time.Sleep(ReadBackWait)
		}
		merged, err := f.Merged(pr)
		if err == nil && merged {
			r.State, r.Merged = MergeDone, true
			return r
		}
		readErr = err
	}
	r.State = MergeUndetermined
	confirm := fmt.Sprintf("; the merge may have landed: confirm with gh pr view %d (no ledger row was written)", pr.Number)
	if readErr != nil {
		r.Reasons = append(r.Reasons, reason("read-back", "the merge call succeeded but the outcome could not be read: "+readErr.Error()+confirm))
		return r
	}
	observed := "unreadable"
	if now, err := f.PR(in.Selector); err == nil {
		observed = orUnknownState(now.State)
	}
	r.Reasons = append(r.Reasons, reason("read-back", "the merge call succeeded but the PR reads "+observed+", not merged"+confirm))
	return r
}

// ReadBackTries and ReadBackWait bound the post-merge read-back: `gh pr merge` is synchronous, so the first read almost
// always confirms; the extra reads only absorb forge eventual consistency (4 s at most). Tests set ReadBackWait to 0.
var (
	ReadBackTries = 3
	ReadBackWait  = 2 * time.Second
)

// baseAllowed reports whether a PR base branch is one a merge may land on: the epic branch, or (when set) production.
func baseAllowed(base, epic, production string) bool {
	return base == epic || (production != "" && base == production)
}

// waive returns the checks with any whose name the caller waived (--allow-red <name>) dropped, so a named red check no
// longer forces a fail. An empty waiver list returns the checks unchanged.
func waive(checks []forge.Check, allowRed []string) []forge.Check {
	if len(allowRed) == 0 {
		return checks
	}
	waived := map[string]bool{}
	for _, n := range allowRed {
		waived[n] = true
	}
	out := make([]forge.Check, 0, len(checks))
	for _, c := range checks {
		if waived[c.Name] {
			continue
		}
		out = append(out, c)
	}
	return out
}

func orUnknownState(s string) string {
	if s == "" {
		return "unknown"
	}
	return s
}
