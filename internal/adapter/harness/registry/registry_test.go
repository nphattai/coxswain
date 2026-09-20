package registry

import (
	"strings"
	"testing"

	"github.com/nphattai/coxswain/internal/adapter/harness"
)

// A harness with no adapter (omp, opencode) is refused with a message that names the gap; claude, codex, and pi resolve.
func TestAdapter(t *testing.T) {
	for _, name := range []string{"claude", "codex", "pi"} {
		if _, ok := Adapter(name); !ok {
			t.Errorf("%s must have an adapter", name)
		}
	}
	for _, name := range []string{"omp", "opencode", ""} {
		if _, ok := Adapter(name); ok {
			t.Errorf("%s must not have an adapter", name)
		}
	}
}

// Names is the stable, deterministic adapter list doctor and docs iterate; pi is included.
func TestNames(t *testing.T) {
	got := Names()
	want := []string{"claude", "codex", "pi"}
	if len(got) != len(want) {
		t.Fatalf("Names() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Names() = %v, want %v", got, want)
		}
	}
}

// Notices returns the reduced-mode lines from the card, and refuses an unadaptered harness clearly.
func TestNotices(t *testing.T) {
	// codex is pull-wake + manual-checkpoint, so both reduced notices appear.
	n, err := Notices("codex", harness.RoleWorker)
	if err != nil {
		t.Fatalf("codex Notices err: %v", err)
	}
	if !containsSub(n, "pull wake") {
		t.Errorf("codex must print the pull-wake reduced notice, got %v", n)
	}
	if !containsSub(n, "manual checkpoint") {
		t.Errorf("codex must print the manual-checkpoint reduced notice, got %v", n)
	}
	// claude is push + auto, so no reduced notices.
	if n, err := Notices("claude", harness.RoleWorker); err != nil || len(n) != 0 {
		t.Errorf("claude Notices = (%v, %v), want (nil, nil)", n, err)
	}
	// omp has no adapter -> refused, naming the gap.
	_, err = Notices("omp", harness.RoleWorker)
	if err == nil || !strings.Contains(err.Error(), "no adapter") {
		t.Errorf("omp Notices err = %v, want a 'no adapter' refusal", err)
	}
}

// LaunchArgs is the single production entry point for adapter-owned argv: it delegates to the named adapter and errors
// (never launches a bare command) for an unadaptered name.
func TestLaunchArgs(t *testing.T) {
	argv, err := LaunchArgs("claude", harness.Launch{
		Role: harness.RoleWorker, Model: "claude-opus-4-8", Brief: harness.Brief{StoryPath: "/e/stories/s.md"},
	})
	if err != nil {
		t.Fatalf("LaunchArgs claude err: %v", err)
	}
	if len(argv) == 0 || argv[0] != "claude" {
		t.Fatalf("LaunchArgs claude argv = %v, want it to start with claude", argv)
	}
	if _, err := LaunchArgs("omp", harness.Launch{Role: harness.RoleWorker}); err == nil {
		t.Errorf("LaunchArgs for an unadaptered harness must error")
	}
}

func containsSub(lines []string, sub string) bool {
	for _, l := range lines {
		if strings.Contains(l, sub) {
			return true
		}
	}
	return false
}
