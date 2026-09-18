package integration

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nphattai/coxswain/internal/adapter/backend/fake"
	"github.com/nphattai/coxswain/internal/arena"
	"github.com/nphattai/coxswain/internal/arena/roles"
	"github.com/nphattai/coxswain/internal/state"
	"github.com/nphattai/coxswain/internal/workspace"
)

// A full-trigger arena resolves the right roles on the right harnesses and never loses the pack when dispatch fails.
// The fake backend returns a worktree path that does not exist, so worktree.Ensure fails for every role: each role is
// recorded pending_external and the already-written pack survives (the pack is built before any dispatch).
func TestArenaFullTriggerResolvesRolesAndKeepsPack(t *testing.T) {
	epicDir := setupArenaEpic(t, "migration to normalized tiers") // "migration" is a sensitive trigger -> full + domain
	pol := loadTemplatePolicy(t)

	res, err := arena.Run(fake.New(), arena.Options{
		EpicDir: epicDir, WsRoot: filepath.Dir(filepath.Dir(filepath.Dir(epicDir))),
		Policy: pol, Leader: "claude", Round: 1,
	})
	if err != nil {
		t.Fatalf("arena.Run: %v", err)
	}
	if res.Level != roles.Full {
		t.Fatalf("level = %q, want full", res.Level)
	}
	if len(res.Roles) != 3 {
		t.Fatalf("got %d roles, want 3 (adversary, reviewer, domain): %+v", len(res.Roles), res.Roles)
	}

	// The pack was written before dispatch and is not lost by the dispatch failures.
	if res.PackPath == "" {
		t.Fatal("no pack path")
	}
	if _, err := os.Stat(res.PackPath); err != nil {
		t.Fatalf("pack not on disk: %v", err)
	}

	// Harness resolution, end to end through the rendered story: leader claude -> adversary/domain codex, reviewer claude.
	wantHarness := map[roles.Role]string{roles.Adversary: "codex", roles.Reviewer: "claude", roles.Domain: "codex"}
	for _, r := range res.Roles {
		if r.Err == nil {
			t.Fatalf("%s dispatched despite a non-existent worktree path", r.Story)
		}
		if r.Harness != wantHarness[r.Role] {
			t.Errorf("%s harness = %q, want %q", r.Role, r.Harness, wantHarness[r.Role])
		}
		story := filepath.Join(epicDir, "stories", r.Story+".md")
		b, err := os.ReadFile(story)
		if err != nil {
			t.Fatalf("read %s: %v", story, err)
		}
		if !strings.Contains(string(b), "harness: "+wantHarness[r.Role]) {
			t.Errorf("%s frontmatter missing harness: %s", r.Story, wantHarness[r.Role])
		}
	}

	// Every failed dispatch is recorded pending_external (not dropped, not marked working).
	events, _, err := state.Load(epicDir)
	if err != nil {
		t.Fatalf("state.Load: %v", err)
	}
	pending := 0
	for _, ev := range events {
		if ev.To == state.PendingExternal && ev.ExternalConfirmed == false {
			pending++
		}
	}
	if pending != 3 {
		t.Fatalf("got %d pending_external events, want 3", pending)
	}
}

// A below-the-bar design triggers arena-lite: a single adversary.
func TestArenaLiteTriggerIsOneAdversary(t *testing.T) {
	epicDir := setupArenaEpic(t, "a small refactor with nothing sensitive")
	pol := loadTemplatePolicy(t)
	res, err := arena.Run(fake.New(), arena.Options{
		EpicDir: epicDir, WsRoot: filepath.Dir(filepath.Dir(filepath.Dir(epicDir))),
		Policy: pol, Leader: "codex", Round: 1,
	})
	if err != nil {
		t.Fatalf("arena.Run: %v", err)
	}
	if res.Level != roles.Lite || len(res.Roles) != 1 || res.Roles[0].Role != roles.Adversary {
		t.Fatalf("want one adversary at lite, got level %q roles %+v", res.Level, res.Roles)
	}
	// Leader codex -> adversary claude (not-leader, the other direction).
	if res.Roles[0].Harness != "claude" {
		t.Errorf("adversary harness = %q, want claude (not-leader of codex)", res.Roles[0].Harness)
	}
}

// setupArenaEpic writes a minimal epic dir (DESIGN.md + one repo) under a temp workspace and returns the epic dir.
func setupArenaEpic(t *testing.T, design string) string {
	t.Helper()
	ws := t.TempDir()
	epicDir := filepath.Join(ws, "proj", "epics", "e1")
	if err := os.MkdirAll(epicDir, 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(epicDir, "DESIGN.md"), "# Design\n\n"+design+"\n")
	write(t, filepath.Join(epicDir, "repos"), "billing billing-repo\n")
	return epicDir
}

func loadTemplatePolicy(t *testing.T) *workspace.Policy {
	t.Helper()
	pol, err := workspace.LoadPolicyFile(filepath.Join("..", "..", "templates", "policy.json"))
	if err != nil {
		t.Fatalf("load template policy: %v", err)
	}
	return pol
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
