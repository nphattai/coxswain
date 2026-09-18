package integration

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/nphattai/coxswain/internal/adapter/harness"
	"github.com/nphattai/coxswain/internal/adapter/harness/claude"
	"github.com/nphattai/coxswain/internal/adapter/harness/codex"
)

// The capability card in code must match the table in docs/adapters/<name>.md, so the documentation never drifts from
// behavior (brief H). The check is a set of substring assertions against the doc for each card field.
func TestHarnessCardsMatchDocs(t *testing.T) {
	cards := []harness.Capability{claude.New().Card(), codex.New().Card()}
	for _, c := range cards {
		doc, err := os.ReadFile("../../docs/adapters/" + c.Name + ".md")
		if err != nil {
			t.Fatalf("read doc for %s: %v", c.Name, err)
		}
		text := string(doc)
		wants := []string{
			"| name | " + c.Name + " |",
			"| wake | " + string(c.Wake) + " |",
			"| checkpoint | " + string(c.Checkpoint) + " |",
			"| doorbell | " + boolStr(c.Doorbell) + " |",
			"| interrupt | " + boolStr(c.Interrupt) + " |",
			"| telemetry | " + boolStr(c.Telemetry) + " |",
			"| sandbox | " + boolStr(c.Sandbox) + " |",
			"| instructions | " + c.Instructions + " |",
		}
		for _, w := range wants {
			if !strings.Contains(text, w) {
				t.Errorf("%s.md missing card row %q", c.Name, w)
			}
		}
	}
}

func boolStr(b bool) string { return fmt.Sprintf("%t", b) }
