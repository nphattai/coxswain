package bearings

import (
	"strings"
	"testing"
	"time"
)

// A truncated digest names the stage that hit the bound, even though reaping that stage's subprocess lets the sealed
// digest goroutine run on through the later stages (regression: the banner once named "next-step").
func TestTruncationNamesTheStalledStage(t *testing.T) {
	for _, stage := range []string{"doctor", "fleet-state"} {
		d, err := Compose(Opts{Workspace: t.TempDir(), LeaderID: "me", Timeout: 500 * time.Millisecond,
			StageCmd: map[string][]string{stage: {"sleep", "30"}}})
		if err != nil || !d.Truncated {
			t.Fatalf("%s: truncated=%v err=%v", stage, d.Truncated, err)
		}
		if want := `stopped during the "` + stage + `" stage`; !strings.Contains(d.Text, want) {
			t.Errorf("banner does not name %q:\n%s", stage, d.Text)
		}
	}
}

func TestRunBoundedExitCodes(t *testing.T) {
	start := time.Now()
	if code, err := RunBounded(300*time.Millisecond, "sleep", "30"); err != nil || code != timeoutExit {
		t.Errorf("timed out run: code %d err %v, want 124", code, err)
	}
	if time.Since(start) > 5*time.Second {
		t.Error("the bound did not stop the run promptly")
	}
	if code, err := RunBounded(5*time.Second, "sh", "-c", "exit 3"); err != nil || code != 3 {
		t.Errorf("natural exit: code %d err %v, want 3", code, err)
	}
}
