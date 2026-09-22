package brief

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nphattai/coxswain/internal/state"
	"github.com/nphattai/coxswain/internal/workspace"
)

func TestBuildContextPack(t *testing.T) {
	epic := t.TempDir()
	// Give the story an attempt-2 event so Build reports the current attempt.
	if err := state.Append(epic, state.Event{Epic: "e", Story: "s", Attempt: 2, Actor: state.Leader, From: state.Parked, To: state.Working, ExternalConfirmed: true}); err != nil {
		t.Fatal(err)
	}
	path, err := Build(epic, "s")
	if err != nil {
		t.Fatal(err)
	}
	if got := filepath.Base(path); got != "context.md" {
		t.Fatalf("brief path = %s", path)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, want := range []string{
		"cox checkpoint inject",
		"attempt: 2",
		filepath.Join("stories", "s.md"),
		filepath.Join("inbox", "s"),
		filepath.Join("handoffs", "s.md"),
		"policy.json",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("brief context missing %q:\n%s", want, text)
		}
	}
}

// Item 8b: the brief carries a "Delivery contract: mode=<mode> yolo=<on|off>" line and a per-mode paragraph, resolved
// from the story's frontmatter mode and the epic policy's merge.yolo. Base-behavior probe: on the base sha Build writes no
// delivery-contract line, so every assertion here fails.
func TestBriefDeliveryContractPerMode(t *testing.T) {
	cases := []struct {
		mode      string
		yolo      bool
		wantLine  string
		wantWords string // a distinctive word from the mode paragraph
	}{
		{"no-mistakes", false, "Delivery contract: mode=no-mistakes yolo=off", "no-mistakes"},
		{"direct-PR", false, "Delivery contract: mode=direct-PR yolo=off", "direct-PR"},
		{"local-only", true, "Delivery contract: mode=local-only yolo=on", "local-only"},
	}
	for _, c := range cases {
		t.Run(c.mode, func(t *testing.T) {
			root := t.TempDir()
			if _, err := workspace.Init(root, nil); err != nil {
				t.Fatal(err)
			}
			if c.yolo {
				pj := filepath.Join(root, workspace.ControlDir, "policy.json")
				b, err := os.ReadFile(pj)
				if err != nil {
					t.Fatal(err)
				}
				out := strings.Replace(string(b), `"yolo": false`, `"yolo": true`, 1)
				if err := os.WriteFile(pj, []byte(out), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			epic := filepath.Join(root, "proj", "epics", "slug")
			if err := os.MkdirAll(filepath.Join(epic, "stories"), 0o755); err != nil {
				t.Fatal(err)
			}
			story := "s"
			fm := "---\nid: s\nmode: " + c.mode + "\n---\n\n# s\n"
			if err := os.WriteFile(filepath.Join(epic, "stories", story+".md"), []byte(fm), 0o644); err != nil {
				t.Fatal(err)
			}
			path, err := Build(epic, story)
			if err != nil {
				t.Fatal(err)
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			text := string(raw)
			if !strings.Contains(text, c.wantLine) {
				t.Errorf("brief missing %q:\n%s", c.wantLine, text)
			}
			if !strings.Contains(text, c.wantWords) {
				t.Errorf("brief missing mode paragraph keyword %q:\n%s", c.wantWords, text)
			}
		})
	}
}
