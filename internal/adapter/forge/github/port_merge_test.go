// Port tests (epic cox-refresh, story cox-refresh-merge): firstmate's GitHub merge outcome cases from
// tests/fm-pr-merge.test.sh translated against `cox ship merge`'s core (verdict.Merge) driving this adapter over a
// scripted gh. Firstmate pinned at a8572f6 (references/firstmate, read only). Every case is t.Run("FM/<suite>/<case>")
// with a `// fm: tests/<file>:<line>@a8572f6` citation.
//
// Name map (firstmate -> cox): fm-pr-merge.sh -> verdict.Merge over github.Client; the post-merge outcome read
// (github_read_outcome, `api graphql`) -> Client.Merged re-reading `gh pr view <n> --json state`; "merge poll remains
// armed" (the merge is not concluded either way) -> MergeUndetermined, exit 3, never MergeRefused (VISION three states).
package github

import (
	"errors"
	"strings"
	"testing"

	"github.com/nphattai/coxswain/internal/verdict"
)

// scriptedGH answers the calls one `cox ship merge` makes: the live PR read, the checks, the merge, and the post-merge
// outcome read (`pr view <n> --json state`), which answers outcome (or fails when outcome is ""). calls logs each argv.
type scriptedGH struct {
	mergeable string
	outcome   string
	merged    bool
	calls     []string
}

func (g *scriptedGH) run(_ string, args ...string) ([]byte, error) {
	line := strings.Join(args, " ")
	g.calls = append(g.calls, line)
	switch {
	case strings.HasPrefix(line, "pr view") && strings.HasSuffix(line, "--json state"):
		if g.outcome == "" {
			return nil, errors.New("gh: HTTP 502 while reading the outcome")
		}
		return []byte(`{"state":"` + g.outcome + `"}`), nil
	case strings.HasPrefix(line, "pr view"):
		state := "OPEN"
		if g.merged && g.outcome != "" {
			state = g.outcome
		}
		return []byte(`{"number":51,"headRefOid":"1010101010101010101010101010101010101010","headRefName":"story/x","baseRefName":"epic/x","state":"` + state + `","isDraft":false,"mergeable":"` + g.mergeable + `"}`), nil
	case strings.HasPrefix(line, "pr checks"):
		return []byte(`[{"name":"ci","state":"SUCCESS","bucket":"pass"}]`), nil
	case strings.HasPrefix(line, "pr merge"):
		g.merged = true
		return nil, nil
	}
	return nil, errors.New("unexpected gh call: " + line)
}

func mergeOver(t *testing.T, g *scriptedGH) verdict.MergeReport {
	t.Helper()
	old := verdict.ReadBackWait
	verdict.ReadBackWait = 0
	t.Cleanup(func() { verdict.ReadBackWait = old })
	c := New("/repo")
	c.run = g.run
	return verdict.Merge(c, verdict.MergeInput{Selector: "51", EpicBranch: "epic/x", Production: "main", Method: "squash", Captain: true})
}

// readAfterMerge reports whether an outcome read followed the merge call.
func (g *scriptedGH) readAfterMerge() bool {
	seenMerge := false
	for _, c := range g.calls {
		if strings.HasPrefix(c, "pr merge") {
			seenMerge = true
		}
		if seenMerge && strings.HasSuffix(c, "--json state") {
			return true
		}
	}
	return false
}

func TestPortPRMerge(t *testing.T) {
	// fm: tests/fm-pr-merge.test.sh:501@a8572f6 (a genuinely merged PR is verified by reading the outcome back)
	t.Run("FM/fm-pr-merge/github_merged_outcome_is_verified", func(t *testing.T) {
		g := &scriptedGH{mergeable: "MERGEABLE", outcome: "MERGED"}
		rep := mergeOver(t, g)
		if rep.State != verdict.MergeDone || !rep.Merged {
			t.Fatalf("red[Client.Merged live read]: a merged PR must read back merged: state=%s reasons=%v", rep.State, rep.Reasons)
		}
		if !g.readAfterMerge() {
			t.Errorf("red[Client.Merged live read]: the outcome was not read back after merging; calls=%v", g.calls)
		}
	})

	// fm: tests/fm-pr-merge.test.sh:547@a8572f6 (the merge call leaves the PR open: not proved, the poll stays armed,
	// the refusal names the observed state). The case keeps firstmate's name; firstmate's "refuses" with the poll armed
	// is cox's unknown (exit 3), per the name map above.
	t.Run("FM/fm-pr-merge/github_open_unqueued_outcome_refuses", func(t *testing.T) {
		for _, outcome := range []string{"OPEN", "CLOSED"} {
			g := &scriptedGH{mergeable: "MERGEABLE", outcome: outcome}
			rep := mergeOver(t, g)
			if rep.State != verdict.MergeUndetermined || rep.Merged {
				t.Fatalf("red[verdict.Merge read-back]: an unproved merge is unknown, not %s: reasons=%v", rep.State, rep.Reasons)
			}
			if want := "reads " + strings.ToLower(outcome); !strings.Contains(strings.Join(rep.Reasons, "\n"), want) {
				t.Errorf("the reason must name the observed state %q: %v", want, rep.Reasons)
			}
		}
	})

	// fm: tests/fm-pr-merge.test.sh:572@a8572f6 (an unreadable outcome after a successful merge call is not a merge and
	// not a refusal: the bookkeeping and poll are kept)
	t.Run("FM/fm-pr-merge/github_unreadable_outcome_keeps_pr_bookkeeping", func(t *testing.T) {
		g := &scriptedGH{mergeable: "MERGEABLE", outcome: ""}
		rep := mergeOver(t, g)
		if rep.State != verdict.MergeUndetermined || rep.Merged {
			t.Fatalf("red[Client.Merged live read]: an unreadable outcome is unknown, not %s: reasons=%v", rep.State, rep.Reasons)
		}
		if rep.PR != 51 {
			t.Errorf("the report must keep the PR it merged: pr=%d", rep.PR)
		}
	})
}
