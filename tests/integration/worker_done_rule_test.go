package integration

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// F8(b): the "a re-steered worker ends with worker_done, not status" rule must live in the worker story template, the
// harness-neutral AGENTS.md, and the cox-dispatch skill, so it cannot silently drop from any of the three.
func TestWorkerDoneNotStatusRuleDocumented(t *testing.T) {
	files := []string{
		filepath.Join("..", "..", "templates", "story.md"),
		filepath.Join("..", "..", "AGENTS.md"),
		filepath.Join("..", "..", "skills", "cox-dispatch", "SKILL.md"),
	}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		s := strings.ToLower(string(b))
		if !strings.Contains(s, "worker_done") {
			t.Errorf("%s does not mention worker_done", f)
		}
		// It must contrast worker_done with status (a re-run ends with worker_done, not a status).
		if !strings.Contains(s, "status") || !strings.Contains(s, "idle_no_done") {
			t.Errorf("%s missing the status-vs-worker_done / idle_no_done rule", f)
		}
		// F9: it must document the done: status convention for a re-run (one worker_done per dispatch).
		if !strings.Contains(s, "done:") {
			t.Errorf("%s missing the done: status re-run convention (F9)", f)
		}
	}
}
