package verdict

import (
	"strings"
	"testing"

	"github.com/nphattai/coxswain/internal/adapter/forge"
	"github.com/nphattai/coxswain/internal/adapter/forge/fake"
)

// greenPR is an open, non-draft, mergeable PR on the epic branch with one passing check - the base case a merge clears.
func greenPR() fake.Fixture {
	return fake.Fixture{
		PR:     forge.PR{Number: 7, Head: "abc123def456", Base: "epic/x", State: "open", Draft: false, Mergeable: true},
		Checks: []forge.Check{{Name: "ci", Status: "completed", Conclusion: "success"}},
	}
}

func captainInput() MergeInput {
	return MergeInput{Selector: "7", EpicBranch: "epic/x", Production: "main", Method: "squash", Captain: true}
}

func hasReason(reasons []string, key string) bool {
	for _, r := range reasons {
		if strings.HasPrefix(r, key+":") {
			return true
		}
	}
	return false
}

// Green PR, captain runs it: merged, and the read-back confirms it.
func TestMergeGreenMerges(t *testing.T) {
	f := &fake.Forge{F: greenPR()}
	rep := Merge(f, captainInput())
	if rep.State != MergeDone || !rep.Merged {
		t.Fatalf("green PR must merge: state=%s merged=%v reasons=%v", rep.State, rep.Merged, rep.Reasons)
	}
	if merged, _ := f.Merged(f.F.PR); !merged {
		t.Error("fake forge should report the PR merged after Merge")
	}
}

// A red check refuses and the reasons name the CI failure.
func TestMergeRedCheckRefused(t *testing.T) {
	fx := greenPR()
	fx.Checks = []forge.Check{{Name: "ci", Status: "completed", Conclusion: "failure"}}
	f := &fake.Forge{F: fx}
	rep := Merge(f, captainInput())
	if rep.State != MergeRefused {
		t.Fatalf("red check must refuse, got %s", rep.State)
	}
	if !hasReason(rep.Reasons, "ci") {
		t.Errorf("reasons must name ci, got %v", rep.Reasons)
	}
	if rep.Merged {
		t.Error("a refused merge must not report merged")
	}
}

// Pending CI is unknown (exit 3), never a guessed pass and never a refusal.
func TestMergePendingUnknown(t *testing.T) {
	fx := greenPR()
	fx.Checks = []forge.Check{{Name: "ci", Status: "in_progress"}}
	rep := Merge(&fake.Forge{F: fx}, captainInput())
	if rep.State != MergeUndetermined {
		t.Fatalf("pending CI must be unknown, got %s (reasons %v)", rep.State, rep.Reasons)
	}
}

// A head that moved between the read and the merge (the pinned-sha reject) refuses.
func TestMergeHeadMovedRefused(t *testing.T) {
	fx := greenPR()
	fx.HeadMoved = true
	f := &fake.Forge{F: fx}
	rep := Merge(f, captainInput())
	if rep.State != MergeRefused || !hasReason(rep.Reasons, "merge") {
		t.Fatalf("head-moved must refuse naming merge: state=%s reasons=%v", rep.State, rep.Reasons)
	}
	if merged, _ := f.Merged(fx.PR); merged {
		t.Error("a rejected merge must not leave the fake reporting merged")
	}
}

// yolo=false and no --captain refuses; the reason is authority.
func TestMergeYoloFalseNoCaptainRefused(t *testing.T) {
	in := captainInput()
	in.Captain = false // yolo defaults false
	rep := Merge(&fake.Forge{F: greenPR()}, in)
	if rep.State != MergeRefused || !hasReason(rep.Reasons, "authority") {
		t.Fatalf("yolo=false without --captain must refuse on authority: state=%s reasons=%v", rep.State, rep.Reasons)
	}
}

// A worker terminal (COX_STORY set) is always refused, even with --captain.
func TestMergeWorkerTerminalRefused(t *testing.T) {
	in := captainInput()
	in.Worker = true
	rep := Merge(&fake.Forge{F: greenPR()}, in)
	if rep.State != MergeRefused || !hasReason(rep.Reasons, "authority") {
		t.Fatalf("a worker terminal must be refused: state=%s reasons=%v", rep.State, rep.Reasons)
	}
}

// --check decides but never merges: a green PR reports it would merge (Merged=false) and the forge is untouched.
func TestMergeCheckNeverMerges(t *testing.T) {
	in := captainInput()
	in.Check = true
	f := &fake.Forge{F: greenPR()}
	rep := Merge(f, in)
	if rep.State != MergeDone || rep.Merged {
		t.Fatalf("--check on a green PR must say would-merge without merging: state=%s merged=%v", rep.State, rep.Merged)
	}
	if merged, _ := f.Merged(f.F.PR); merged {
		t.Error("--check must not call Merge on the forge")
	}
}

// Every failing condition is listed, not just the first (draft + wrong base + red check together).
func TestMergeListsEveryReason(t *testing.T) {
	fx := greenPR()
	fx.PR.Draft = true
	fx.PR.Base = "some/other-branch"
	fx.Checks = []forge.Check{{Name: "ci", Status: "completed", Conclusion: "failure"}}
	rep := Merge(&fake.Forge{F: fx}, captainInput())
	if rep.State != MergeRefused {
		t.Fatalf("want refused, got %s", rep.State)
	}
	for _, key := range []string{"draft", "base", "ci"} {
		if !hasReason(rep.Reasons, key) {
			t.Errorf("reasons must include %q; got %v", key, rep.Reasons)
		}
	}
}

// A named --allow-red check is waived, so an otherwise-green PR merges.
func TestMergeAllowRedWaivesCheck(t *testing.T) {
	fx := greenPR()
	fx.Checks = []forge.Check{
		{Name: "flaky-e2e", Status: "completed", Conclusion: "failure"},
		{Name: "ci", Status: "completed", Conclusion: "success"},
	}
	in := captainInput()
	in.AllowRed = []string{"flaky-e2e"}
	rep := Merge(&fake.Forge{F: fx}, in)
	if rep.State != MergeDone || !rep.Merged {
		t.Fatalf("waived red check must let a green PR merge: state=%s reasons=%v", rep.State, rep.Reasons)
	}
}

// A forge read failure is unknown (exit 3), never a refusal.
func TestMergeForgeReadFailureUnknown(t *testing.T) {
	fx := greenPR()
	fx.Errors = map[string]string{"pr": "gh: not logged in"}
	rep := Merge(&fake.Forge{F: fx}, captainInput())
	if rep.State != MergeUndetermined {
		t.Fatalf("a forge read failure must be unknown, got %s", rep.State)
	}
}

// B-59: GitHub may report the PR open for a moment after the merge call; the bounded read-back absorbs it and the merge
// is confirmed, not refused.
func TestMergeReadBackLagIsAbsorbed(t *testing.T) {
	old := ReadBackWait
	ReadBackWait = 0
	t.Cleanup(func() { ReadBackWait = old })
	fx := greenPR()
	fx.ReadBackLag = ReadBackTries - 1
	rep := Merge(&fake.Forge{F: fx}, captainInput())
	if rep.State != MergeDone || !rep.Merged {
		t.Fatalf("a lag within the poll must confirm the merge: state=%s reasons=%v", rep.State, rep.Reasons)
	}
	fx.ReadBackLag = ReadBackTries
	rep = Merge(&fake.Forge{F: fx}, captainInput())
	if rep.State != MergeUndetermined || rep.Merged {
		t.Fatalf("a merge never confirmed within the poll is unknown (exit 3), got %s: %v", rep.State, rep.Reasons)
	}
}

// B-60: mergeable UNKNOWN (GitHub recomputing after a sibling merge) never merges, and with no definite refusal it is
// unknown (exit 3, re-run), not refused like a conflict. A definite refusal still wins; CONFLICTING stays refused.
// Firstmate refuses on it too (bin/fm-pr-merge.sh:648@a8572f6); the ruling keeps that and only makes it three-state.
func TestMergeMergeableUnknownIsUndetermined(t *testing.T) {
	fx := greenPR()
	fx.PR.Mergeable, fx.PR.MergeableUnknown = false, true
	f := &fake.Forge{F: fx}
	rep := Merge(f, captainInput())
	if rep.State != MergeUndetermined || !hasReason(rep.Reasons, "mergeable") {
		t.Fatalf("UNKNOWN mergeable must be unknown naming mergeable: state=%s reasons=%v", rep.State, rep.Reasons)
	}
	if merged, _ := f.Merged(fx.PR); merged {
		t.Error("UNKNOWN mergeable must never call Merge")
	}
	in := captainInput()
	in.Check = true
	if rep := Merge(&fake.Forge{F: fx}, in); rep.State != MergeUndetermined {
		t.Errorf("--check with UNKNOWN mergeable must be unknown, got %s", rep.State)
	}

	fx.PR.Draft = true
	if rep := Merge(&fake.Forge{F: fx}, captainInput()); rep.State != MergeRefused || !hasReason(rep.Reasons, "draft") {
		t.Errorf("a definite refusal must win over UNKNOWN: state=%s reasons=%v", rep.State, rep.Reasons)
	}

	fx = greenPR()
	fx.PR.Mergeable = false // CONFLICTING
	if rep := Merge(&fake.Forge{F: fx}, captainInput()); rep.State != MergeRefused || !hasReason(rep.Reasons, "mergeable") {
		t.Errorf("CONFLICTING must refuse naming mergeable: state=%s reasons=%v", rep.State, rep.Reasons)
	}
}
