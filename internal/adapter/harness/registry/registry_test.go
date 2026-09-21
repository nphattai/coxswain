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

// Notices returns the reduced-mode lines from the card, gates unsandboxed dispatch, and refuses an unadaptered harness.
func TestNotices(t *testing.T) {
	// codex is pull-wake + manual-checkpoint (and sandboxed), so both reduced notices appear and no sandbox gate fires.
	n, auth, err := Notices("codex", harness.RoleWorker, false)
	if err != nil {
		t.Fatalf("codex Notices err: %v", err)
	}
	if !containsSub(n, "pull wake") || !containsSub(n, "manual checkpoint") {
		t.Errorf("codex must print the pull-wake + manual-checkpoint notices, got %v", n)
	}
	if auth != "" {
		t.Errorf("codex is sandboxed, authority must be empty, got %q", auth)
	}
	// claude is push + auto and unsandboxed but carries a standing ack: it authorizes silently (no new notice, no flag),
	// so its dispatch is unchanged.
	if n, auth, err := Notices("claude", harness.RoleWorker, false); err != nil || len(n) != 0 || auth != "standing-ack" {
		t.Errorf("claude Notices = (%v, %q, %v), want (nil, standing-ack, nil)", n, auth, err)
	}
	// pi is unsandboxed with no standing ack: refused without the flag.
	if _, _, err := Notices("pi", harness.RoleWorker, false); err == nil || !strings.Contains(err.Error(), "unsandboxed") {
		t.Errorf("pi Notices without --allow-unsandboxed must refuse, got %v", err)
	}
	// pi with the flag: authorized (authority=flag) with a prominent UNSANDBOXED warning.
	n, auth, err = Notices("pi", harness.RoleWorker, true)
	if err != nil || auth != "flag" {
		t.Fatalf("pi Notices with flag = (%v, %q, %v), want authority=flag no err", n, auth, err)
	}
	if !containsSub(n, "UNSANDBOXED") {
		t.Errorf("pi authorized-by-flag must print a prominent UNSANDBOXED warning, got %v", n)
	}
	// omp has no adapter -> refused, naming the gap.
	if _, _, err := Notices("omp", harness.RoleWorker, false); err == nil || !strings.Contains(err.Error(), "no adapter") {
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
