package roles

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nphattai/coxswain/internal/workspace"
)

// testPolicy mirrors templates/policy.json's arena/harness sections.
func testPolicy() *workspace.Policy {
	p := &workspace.Policy{}
	p.Harness.Leader = workspace.HarnessRole{Options: []string{"claude", "codex"}, Default: "claude"}
	p.Harness.Arena.Adversary.Rule = "not-leader"
	p.Harness.Arena.Adversary.Default = "codex"
	p.Harness.Arena.Reviewer.Rule = "same-as-leader-new-session"
	return p
}

func TestResolveBothDirections(t *testing.T) {
	pol := testPolicy()

	claudeLed, err := Resolve(pol, "claude")
	if err != nil {
		t.Fatal(err)
	}
	if claudeLed[Adversary] != "codex" || claudeLed[Reviewer] != "claude" || claudeLed[Domain] != "codex" {
		t.Errorf("leader claude: %+v (want adversary codex, reviewer claude, domain codex)", claudeLed)
	}

	codexLed, err := Resolve(pol, "codex")
	if err != nil {
		t.Fatal(err)
	}
	if codexLed[Adversary] != "claude" || codexLed[Reviewer] != "codex" || codexLed[Domain] != "claude" {
		t.Errorf("leader codex: %+v (want adversary claude, reviewer codex, domain claude)", codexLed)
	}
}

func TestTriggerLevels(t *testing.T) {
	pol := testPolicy()

	if lvl, r := Trigger(Signals{Design: "a plain crud epic", Repos: 1}, pol); lvl != Lite {
		t.Errorf("plain design => %s (%v), want lite", lvl, r)
	}
	if lvl, _ := Trigger(Signals{Design: "adds a migration dropping a column", Repos: 1}, pol); lvl != Full {
		t.Errorf("migration => %s, want full", lvl)
	}
	if lvl, _ := Trigger(Signals{Design: "plain", Repos: 3}, pol); lvl != Full {
		t.Errorf("3 repos => %s, want full", lvl)
	}
	if lvl, _ := Trigger(Signals{Design: "plain", Repos: 1, Reason: "captain wants it"}, pol); lvl != Full {
		t.Errorf("captain reason => %s, want full", lvl)
	}
	if lvl, _ := Trigger(Signals{}, pol); lvl != None {
		t.Errorf("empty => %s, want none", lvl)
	}
}

func TestActiveRoles(t *testing.T) {
	if got := Active(Lite, nil); len(got) != 1 || got[0] != Adversary {
		t.Errorf("lite active = %v, want [adversary]", got)
	}
	full := Active(Full, []string{"3 repos (>= 3)"})
	if len(full) != 2 {
		t.Errorf("full non-sensitive active = %v, want adversary+reviewer", full)
	}
	sens := Active(Full, []string{"design mentions migration"})
	if len(sens) != 3 || sens[2] != Domain {
		t.Errorf("full sensitive active = %v, want adversary+reviewer+domain", sens)
	}
}

func TestRenderWritesStory(t *testing.T) {
	epicDir := t.TempDir()
	path, err := Render(epicDir, StoryData{
		Role: Adversary, Repo: "cox", Harness: "codex", Model: "",
		Slug: "e1", EpicDir: epicDir, PackPath: epicDir + "/reports/arena/context-pack.md",
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(path) != "arena-adversary.md" {
		t.Errorf("story path = %s", path)
	}
	b, _ := os.ReadFile(path)
	got := string(b)
	for _, want := range []string{"id: arena-adversary", "harness: codex", "readonly: true", "round: 1", "round-1-adversary.md", "epic-blocking"} {
		if !strings.Contains(got, want) {
			t.Errorf("rendered story missing %q:\n%s", want, got)
		}
	}
}

// A new round must not silently reuse a prior round's story: Render refuses when the file exists for a different round,
// and --force re-renders it for the new round (title and output path follow Round). Same round is an idempotent resume.
func TestRenderPerRound(t *testing.T) {
	epicDir := t.TempDir()
	data := func(round int) StoryData {
		return StoryData{Role: Adversary, Repo: "cox", Harness: "codex", Slug: "e1", EpicDir: epicDir,
			PackPath: epicDir + "/reports/arena/context-pack.md", Round: round}
	}

	// Round 1 renders.
	if _, err := Render(epicDir, data(1), false); err != nil {
		t.Fatal(err)
	}
	// Round 1 again: idempotent resume, no error.
	if _, err := Render(epicDir, data(1), false); err != nil {
		t.Fatalf("same-round re-render should reuse the file: %v", err)
	}
	// Round 2 without --force: refuse.
	if _, err := Render(epicDir, data(2), false); err == nil || !strings.Contains(err.Error(), "round 2") {
		t.Fatalf("round 2 should refuse over a round-1 story, got %v", err)
	}
	// The round-1 story is untouched by the refused render.
	b, _ := os.ReadFile(filepath.Join(epicDir, "stories", "arena-adversary.md"))
	if !strings.Contains(string(b), "round: 1") {
		t.Fatal("refused render must not modify the round-1 story")
	}
	// Round 2 with --force: re-render for round 2 (title and output path follow the new round).
	if _, err := Render(epicDir, data(2), true); err != nil {
		t.Fatalf("--force round 2: %v", err)
	}
	b, _ = os.ReadFile(filepath.Join(epicDir, "stories", "arena-adversary.md"))
	for _, want := range []string{"round: 2", "round 2", "round-2-adversary.md"} {
		if !strings.Contains(string(b), want) {
			t.Errorf("forced round-2 story missing %q:\n%s", want, string(b))
		}
	}
}
