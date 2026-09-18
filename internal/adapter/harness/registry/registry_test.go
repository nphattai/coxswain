package registry

import (
	"strings"
	"testing"

	"github.com/nphattai/coxswain/internal/adapter/harness"
)

// A harness with no adapter (omp, opencode) is refused with a message that names the gap; claude and codex resolve.
func TestAdapter(t *testing.T) {
	for _, name := range []string{"claude", "codex"} {
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

func containsSub(lines []string, sub string) bool {
	for _, l := range lines {
		if strings.Contains(l, sub) {
			return true
		}
	}
	return false
}
