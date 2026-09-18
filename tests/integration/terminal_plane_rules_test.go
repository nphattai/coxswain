package integration

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ADR 0012 item 13: the terminal-plane worker/leader rules (report through cox, ask via report question + question
// wait, reply file-based, no heartbeat) must live in the worker story template, AGENTS.md, and the cox-dispatch skill,
// while the orchestration-plane text is kept behind its condition. Guards all three so a plane's rules cannot drop.
func TestTerminalPlaneRulesDocumented(t *testing.T) {
	story := read(t, filepath.Join("..", "..", "templates", "story.md"))
	agents := read(t, filepath.Join("..", "..", "AGENTS.md"))
	skill := read(t, filepath.Join("..", "..", "skills", "cox-dispatch", "SKILL.md"))

	// Worker template: report through cox, ask via report question + question wait, and name the plane switch.
	for _, w := range []string{"cox story report done", "cox story report question", "cox question wait", "COX_PLANE=terminal"} {
		if !strings.Contains(story, w) {
			t.Errorf("templates/story.md missing terminal-plane rule %q", w)
		}
	}
	// The worker must be told not to heartbeat on the terminal plane.
	if !strings.Contains(strings.ToLower(story), "no heartbeat") {
		t.Errorf("templates/story.md must tell the terminal-plane worker to send no heartbeat")
	}
	// Leader files: file-based reply on the terminal plane, and both planes still named.
	for _, w := range []string{"cox reply <story> qNNN", "terminal plane", "orchestration plane"} {
		if !strings.Contains(strings.ToLower(agents), strings.ToLower(w)) {
			t.Errorf("AGENTS.md missing %q", w)
		}
	}
	if !strings.Contains(skill, "cox reply <story> qNNN") || !strings.Contains(strings.ToLower(skill), "terminal plane") {
		t.Errorf("skills/cox-dispatch/SKILL.md missing terminal-plane reply rule")
	}
	// The orchestration text is kept (both planes coexist behind the switch).
	for _, w := range []string{story, agents, skill} {
		if !strings.Contains(w, "orca orchestration") {
			t.Errorf("orchestration-plane text was dropped from a worker/leader doc")
		}
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}
