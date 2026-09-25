package watch

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nphattai/coxswain/internal/adapter/backend/fake"
	"github.com/nphattai/coxswain/internal/workspace"
	"github.com/nphattai/coxswain/templates"
)

// leaderWorkspace builds <ws>/cox/{workspace.json,policy.json} with harness.leader.default = leader and returns the
// epic dir <ws>/<project>/epics/e, so leaderIsPush resolves the policy the way a real epic does.
func leaderWorkspace(t *testing.T, leader string) string {
	t.Helper()
	ws := t.TempDir()
	cox := filepath.Join(ws, workspace.ControlDir)
	must(t, os.MkdirAll(cox, 0o755))
	must(t, os.WriteFile(filepath.Join(cox, "workspace.json"), []byte("{}\n"), 0o644))
	pol, err := templates.File("policy.json")
	must(t, err)
	const def = `"leader": { "options": ["claude", "codex", "pi"], "default": "claude" }`
	if !strings.Contains(string(pol), def) {
		t.Fatalf("template policy lost its leader line %q", def)
	}
	pol = []byte(strings.Replace(string(pol), def, `"leader": { "options": ["claude", "codex", "pi"], "default": "`+leader+`" }`, 1))
	must(t, os.WriteFile(filepath.Join(cox, "policy.json"), pol, 0o644))
	epic := filepath.Join(ws, "proj", "epics", "e")
	must(t, os.MkdirAll(epic, 0o755))
	return epic
}

// B-73 (+B-57): an urgent unacked backlog never types into a push leader (claude, pi) - the hook rewake is its only
// wake, so no Send, no doorbell-failure count and no alarm - while a pull leader (codex) keeps its doorbell.
// fm: docs/supervision-protocols/claude.md:6@a8572f6 (the Stop asyncRewake owns the wake)
// fm: bin/fm-claude-stop-autoarm.sh:105@a8572f6 (exit 2 carries the rewake; nothing is typed)
func TestNudgeNeverTypesIntoPushLeader(t *testing.T) {
	for _, tc := range []struct {
		leader string
		sends  int
	}{{"claude", 0}, {"pi", 0}, {"codex", 1}} {
		t.Run(tc.leader, func(t *testing.T) {
			epic := leaderWorkspace(t, tc.leader)
			writeLeader(t, epic, "term_leader")
			seedUrgentWake(t, epic)
			now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
			var alarms int
			b := fake.New()
			b.SendRang = true
			w := &Watcher{
				EpicDir: epic, Backend: b, NudgeWindow: time.Hour, AlarmChannel: "command:true",
				AlarmRun: func(string, string) error { alarms++; return nil },
				Now:      func() time.Time { return now },
			}
			for i := 0; i <= DoorbellFailAlarm; i++ {
				if tc.sends == 0 {
					b.FailNext("Send", nil) // were anything sent, the failure ladder would count it
				}
				w.nudgeLeader()
				now = now.Add(time.Second)
			}
			if got := countCalls(b.Calls, "Send"); got != tc.sends {
				t.Fatalf("%s leader: %d doorbell(s) typed, want %d", tc.leader, got, tc.sends)
			}
			if tc.sends == 0 && (DoorbellFailMax(epic) != 0 || alarms != 0) {
				t.Fatalf("%s leader: doorbell ladder ran (fails=%d alarms=%d)", tc.leader, DoorbellFailMax(epic), alarms)
			}
		})
	}
}
