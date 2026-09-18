package verdict

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nphattai/coxswain/internal/adapter/forge"
	"github.com/nphattai/coxswain/internal/adapter/forge/fake"
)

// The 7 fixtures assert the three-state contract: no retrieval error or pending state is ever promoted to pass.
func TestAuditFixtures(t *testing.T) {
	cases := map[string]Verdict{
		"clean":             Pass,
		"new_commits":       Pass,
		"ci_pending":        Unknown,
		"no_diff":           Unknown,
		"stale_review":      Unknown,
		"ci_failed":         Fail,
		"changes_requested": Fail,
	}
	for name, want := range cases {
		t.Run(name, func(t *testing.T) {
			f, err := fake.Load(filepath.Join("..", "..", "tests", "fixtures", "verdict", name, "pr.json"))
			if err != nil {
				t.Fatal(err)
			}
			r := Audit(f, name, "story/"+name)
			if r.Verdict != want {
				t.Fatalf("%s: verdict=%s want %s (reasons=%v)", name, r.Verdict, want, r.Reasons)
			}
			// The invariant: a non-pass expectation must never come back pass.
			if want != Pass && r.Verdict == Pass {
				t.Fatalf("%s wrongly promoted to pass", name)
			}
			if r.Verdict != Unknown && r.Head == "" {
				t.Fatalf("%s: a decided verdict must carry a head sha", name)
			}
		})
	}
}

// A credential in the diff makes the verdict fail, and only the rule name (never the secret) is reported.
func TestAuditCredentialScanFails(t *testing.T) {
	f := &fake.Forge{F: fake.Fixture{
		PR:       forge.PR{Number: 200, Head: "sha200"},
		Diff:     "+ const jwt = \"eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.abcdefghij\";\n",
		Checks:   []forge.Check{{Name: "ci", Status: "completed", Conclusion: "success"}},
		Comments: []forge.Comment{{Author: "rs-pr-reviewer", Body: "✅"}},
	}}
	r := Audit(f, "s", "story/s")
	if r.Verdict != Fail {
		t.Fatalf("credential in diff must fail, got %s", r.Verdict)
	}
	if len(r.CredMatches) != 1 || r.CredMatches[0] != "jwt" {
		t.Fatalf("want [jwt], got %v", r.CredMatches)
	}
	// The secret itself must never appear in any reason.
	for _, reason := range r.Reasons {
		if strings.Contains(reason, "eyJ") {
			t.Fatalf("reason leaked the secret: %q", reason)
		}
	}
}

func TestScanCredentials(t *testing.T) {
	got := scanCredentials(`token = "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.abcdefghij" and phone 0912345678`)
	if len(got) != 2 || got[0] != "jwt" || got[1] != "vn-phone" {
		t.Fatalf("want [jwt vn-phone], got %v", got)
	}
	if len(scanCredentials("nothing secret here")) != 0 {
		t.Fatal("clean text should match no rules")
	}
}

func TestShipFetchFailureIsUnknownNotClean(t *testing.T) {
	git := func(dir string, args ...string) (string, error) {
		if len(args) > 0 && args[0] == "fetch" {
			return "", errors.New("network down")
		}
		return "", nil
	}
	rep := Ship([]RepoTarget{{Alias: "web", Dir: "/x", EpicBranch: "epic/demo", Production: "main"}}, git)
	r := rep.Repos[0]
	if r.Verdict != Unknown {
		t.Fatalf("fetch failure must be unknown, got %s", r.Verdict)
	}
	// Never a fabricated "no conflicts": Conflicts stays nil on unknown.
	if r.Conflicts != nil {
		t.Fatalf("unknown must not claim conflicts none, got %v", r.Conflicts)
	}
}

func TestShipMergeTreeFailureIsUnknown(t *testing.T) {
	git := func(dir string, args ...string) (string, error) {
		switch args[0] {
		case "fetch":
			return "", nil
		case "rev-list":
			return "0\t0", nil
		case "merge-tree":
			return "", errors.New("fatal: bad revision") // no output + error => real failure
		}
		return "", nil
	}
	rep := Ship([]RepoTarget{{Alias: "web", Dir: "/x", EpicBranch: "epic/demo", Production: "main"}}, git)
	if rep.Repos[0].Verdict != Unknown {
		t.Fatalf("merge-tree failure must be unknown, got %s", rep.Repos[0].Verdict)
	}
}

func TestShipCleanIsPassWithEmptyConflicts(t *testing.T) {
	git := func(dir string, args ...string) (string, error) {
		switch args[0] {
		case "fetch":
			return "", nil
		case "rev-list":
			return "3\t0", nil
		case "merge-tree":
			return "treeoid1234\n", nil // just the tree oid, no conflicting files
		}
		return "", nil
	}
	rep := Ship([]RepoTarget{{Alias: "web", Dir: "/x", EpicBranch: "epic/demo", Production: "main"}}, git)
	r := rep.Repos[0]
	if r.Verdict != Pass || r.Ahead != 3 || r.Conflicts == nil || len(r.Conflicts) != 0 {
		t.Fatalf("clean ship should be pass, ahead 3, empty (non-nil) conflicts: %+v", r)
	}
}

// ShipForge binds a three-state verdict to the PR head: green checks -> pass, a failed check -> fail, and no PR ->
// unknown that says so (never fail). Merged is reported three-state.
func TestShipForge(t *testing.T) {
	targets := []RepoTarget{{Alias: "api", Dir: "/x", EpicBranch: "epic/v2"}}

	green := &fake.Forge{F: fake.Fixture{
		PR:     forge.PR{Number: 9, Head: "deadbeef"},
		Checks: []forge.Check{{Status: "completed", Conclusion: "success"}},
		Merged: true,
	}}
	got := ShipForge(targets, func(string) forge.Forge { return green })
	if len(got) != 1 || got[0].Verdict != Pass || got[0].PR != 9 || got[0].Head != "deadbeef" || got[0].Merged != "true" {
		t.Fatalf("green ship forge wrong: %+v", got)
	}

	failing := &fake.Forge{F: fake.Fixture{
		PR:     forge.PR{Number: 9, Head: "d"},
		Checks: []forge.Check{{Status: "completed", Conclusion: "failure"}},
	}}
	if got := ShipForge(targets, func(string) forge.Forge { return failing }); got[0].Verdict != Fail {
		t.Fatalf("failed check must be Fail, got %+v", got[0])
	}

	// No PR (gh error) -> unknown, and a reason that says there is no PR; never Fail.
	noPR := &fake.Forge{F: fake.Fixture{Errors: map[string]string{"pr": "no PR for epic/v2"}}}
	got = ShipForge(targets, func(string) forge.Forge { return noPR })
	if got[0].Verdict != Unknown || len(got[0].Reasons) == 0 || !strings.Contains(got[0].Reasons[0], "no PR") {
		t.Fatalf("no-PR must be unknown with a reason: %+v", got[0])
	}
}
