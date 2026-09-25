// Port tests for the story brief (templates/story.md, rendered by internal/epic.Stories): firstmate's crewmate-brief
// guards, translated from firstmate@a8572f6 (epic cox-refresh, story cox-refresh-init-skills, delta group I). Firstmate
// names map to cox names as follows: crewmate brief -> story file, ship modes no-mistakes|direct-PR|local-only ->
// delivery.mode, scout brief -> kind: scout, `blocked [at=<epoch>]: {what you need}` -> `cox story report stuck` (its
// wake line is `blocked: <note>`), treehouse -> Orca (cox's worktree provider).
package templates_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nphattai/coxswain/internal/epic"
	"github.com/nphattai/coxswain/internal/workspace"
)

// renderBriefs renders one story per firstmate scaffold: the three ship modes, the pipo delivery style, and a scout.
// It returns the rendered text by scaffold name.
func renderBriefs(t *testing.T) map[string]string {
	t.Helper()
	wsRoot := t.TempDir()
	if _, err := workspace.Init(wsRoot, nil); err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	render := func(project, name string, spec epic.StorySpec) {
		dir := filepath.Join(wsRoot, project, "epics", "demo-"+name)
		if err := os.MkdirAll(filepath.Join(dir, "stories"), 0o755); err != nil {
			t.Fatal(err)
		}
		written, err := epic.Stories(dir, wsRoot, project, []epic.StorySpec{spec})
		if err != nil {
			t.Fatalf("render %s: %v", name, err)
		}
		b, err := os.ReadFile(written[0])
		if err != nil {
			t.Fatal(err)
		}
		out[name] = string(b)
	}
	for _, mode := range []string{"no-mistakes", "direct-PR", "local-only"} {
		render("proj", mode, epic.StorySpec{ID: "s-" + strings.ToLower(mode), Repo: "app", Title: "t", Mode: mode})
	}
	render("proj", "scout", epic.StorySpec{ID: "s-scout", Repo: "app", Title: "t", Kind: "scout"})
	pipo := filepath.Join(wsRoot, "pipo", "cox")
	if err := os.MkdirAll(pipo, 0o755); err != nil {
		t.Fatal(err)
	}
	override := `{"delivery":{"style":"pipo","mode":"direct-PR","why":"w","review_when":"r"}}`
	if err := os.WriteFile(filepath.Join(pipo, "policy.json"), []byte(override), 0o644); err != nil {
		t.Fatal(err)
	}
	render("pipo", "pipo", epic.StorySpec{ID: "s-pipo", Repo: "app", Title: "t"})
	return out
}

// rule returns the numbered working rule starting with prefix, up to the next numbered rule or section.
func rule(t *testing.T, brief, prefix string) string {
	t.Helper()
	i := strings.Index(brief, prefix)
	if i < 0 {
		return ""
	}
	rest := brief[i+len(prefix):]
	for _, end := range []string{"\n13. ", "\n14. ", "\n## "} {
		if j := strings.Index(rest, end); j >= 0 {
			rest = rest[:j]
		}
	}
	return prefix + rest
}

func TestFM(t *testing.T) {
	briefs := renderBriefs(t)

	// n/a within this case: the treehouse command list and the no-mistakes daemon half (cox has no shared daemon; Orca
	// is the one provider and is named instead), and the secondmate-charter exclusion (cox has no secondmates).
	t.Run("FM/fm-brief/crewmate_scaffolds_forbid_pool_administration", func(t *testing.T) {
		// fm: tests/fm-brief.test.sh:1248@a8572f6
		for name, brief := range briefs {
			for _, want := range []string{
				"worktree pool",
				"create, remove, return, prune, move, or reassign",
				"git worktree add|remove|move|prune",
				"any other worktree provider",
				"sibling slot",
				"cox story report stuck",
			} {
				if !strings.Contains(brief, want) {
					t.Errorf("%s brief lacks %q", name, want)
				}
			}
		}
		// One shared string: the rule is byte-identical across ship and scout, so an edit cannot fix one and miss the
		// other.
		ship := rule(t, briefs["no-mistakes"], "12. NEVER ADMINISTER")
		if ship == "" {
			t.Fatal("ship brief emitted no pool-administration rule to compare")
		}
		for name, brief := range briefs {
			if got := rule(t, brief, "12. NEVER ADMINISTER"); got != ship {
				t.Errorf("%s pool rule drifted from the ship rule:\n%s\n---\n%s", name, got, ship)
			}
		}
	})
}

// B-74 (Contract 3: follow firstmate, no new busy hooks): a worker that ends a turn waiting on its own background
// subagents or workflows reads as idle and then stale. Every brief tells it to declare the wait with a `paused:` status
// first, which the watcher already absorbs as a declared wait (internal/watch statusPaused / wedgeWaitEvidence).
func TestStoryBriefDeclaresBackgroundWaits(t *testing.T) {
	for name, brief := range renderBriefs(t) {
		if !strings.Contains(brief, `cox story report status --note "paused: waiting on N background agents"`) {
			t.Errorf("%s brief does not tell the worker to declare a background wait", name)
		}
	}
}
