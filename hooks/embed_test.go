package hooks

import (
	"encoding/json"
	"os"
	"regexp"
	"testing"
)

// B-26: the plugin must ship no hooks. hooks/hooks.json is Claude Code's default plugin hook path, so it must not
// exist; the embedded leader.json names only `cox hook <name>` commands (no plugin-relative script).
func TestPluginShipsNoHooks(t *testing.T) {
	if _, err := os.Stat("hooks.json"); err == nil {
		t.Fatal("hooks/hooks.json exists: Claude Code loads it as plugin hooks and every leader hook fires twice beside the workspace hooks")
	}
	var m struct {
		Hooks map[string][]struct {
			Hooks []struct{ Command string } `json:"hooks"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(JSON, &m); err != nil {
		t.Fatal(err)
	}
	cmdRe := regexp.MustCompile(`^cox hook [a-z0-9-]+$`)
	n := 0
	for ev, gs := range m.Hooks {
		for _, g := range gs {
			for _, h := range g.Hooks {
				n++
				if !cmdRe.MatchString(h.Command) {
					t.Errorf("%s command %q is not `cox hook <name>`", ev, h.Command)
				}
			}
		}
	}
	if n != 4 {
		t.Errorf("leader.json carries %d hooks, want the 4 leader hooks", n)
	}
}
