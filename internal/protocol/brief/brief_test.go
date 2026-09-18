package brief

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nphattai/coxswain/internal/state"
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
