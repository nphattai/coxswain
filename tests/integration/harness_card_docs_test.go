package integration

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/nphattai/coxswain/internal/adapter/harness/registry"
)

// The capability card in code must match the table in docs/adapters/<name>.md, so the documentation never drifts from
// behavior (brief H). The check iterates the registry, so every implemented adapter (claude, codex, pi, and any future
// one) must ship a doc whose card table matches Card() exactly - no adapter can land without its documented card.
func TestHarnessCardsMatchDocs(t *testing.T) {
	for _, name := range registry.Names() {
		h, ok := registry.Adapter(name)
		if !ok {
			t.Fatalf("registry.Names() lists %q but Adapter(%q) has none", name, name)
		}
		c := h.Card()
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
			"| unsandboxed_ack | " + boolStr(c.UnsandboxedAck) + " |",
			"| busy_record | " + boolStr(c.BusyRecord) + " |",
			"| busy_sources | " + strings.Join(c.BusySources, ", ") + " |",
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
