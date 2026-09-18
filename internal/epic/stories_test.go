package epic

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nphattai/coxswain/internal/workspace"
)

// makeEpicDir creates a minimal epic dir (stories/) under <wsRoot>/<project>/epics/<slug>.
func makeEpicDir(t *testing.T, wsRoot, project, slug string) string {
	t.Helper()
	dir := filepath.Join(wsRoot, project, "epics", slug)
	if err := os.MkdirAll(filepath.Join(dir, "stories"), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestStoriesDefaultStyleWithProvenance(t *testing.T) {
	wsRoot := t.TempDir()
	if _, err := workspace.Init(wsRoot, nil); err != nil {
		t.Fatal(err)
	}
	epicDir := makeEpicDir(t, wsRoot, "proj", "demo")
	written, err := Stories(epicDir, wsRoot, "proj", []StorySpec{{ID: "demo-api", Repo: "backend", Title: "backend slice"}})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(written[0])
	s := string(b)
	if !strings.Contains(s, "delivery: default") {
		t.Errorf("default delivery not stamped:\n%s", s)
	}
	if !strings.Contains(s, "policy_source: cox/policy.json@") {
		t.Errorf("provenance not stamped:\n%s", s)
	}
	if !strings.Contains(s, "DRAFT PR") || strings.Contains(s, "COMMITS STAY LOCAL") {
		t.Errorf("default style body wrong:\n%s", s)
	}
	// Context thresholds resolved from policy.
	if !strings.Contains(s, "400000") || !strings.Contains(s, "500000") {
		t.Errorf("context thresholds not rendered:\n%s", s)
	}
	// F14: the withdrawn "no PNG" rule must be gone; the frame-look rule present.
	if strings.Contains(s, "No PNG enters your context") {
		t.Errorf("withdrawn no-PNG rule still present")
	}
	if !strings.Contains(s, "LOOK at every frame") {
		t.Errorf("frame-look rule missing")
	}
}

func TestStoriesPipoStyleFromProjectOverride(t *testing.T) {
	wsRoot := t.TempDir()
	if _, err := workspace.Init(wsRoot, nil); err != nil {
		t.Fatal(err)
	}
	// Project pipo overrides delivery to pipo.
	projDir := filepath.Join(wsRoot, "pipo")
	if err := os.MkdirAll(filepath.Join(projDir, "cox"), 0o755); err != nil {
		t.Fatal(err)
	}
	override := `{"delivery":{"style":"pipo","why":"pipo commits stay local, one final push","review_when":"if the repo adopts draft-PR flow"}}`
	if err := os.WriteFile(filepath.Join(projDir, "cox", "policy.json"), []byte(override), 0o644); err != nil {
		t.Fatal(err)
	}
	epicDir := makeEpicDir(t, wsRoot, "pipo", "admin")
	written, err := Stories(epicDir, wsRoot, "pipo", []StorySpec{{ID: "admin-cicd", Repo: "admin", Title: "cicd"}})
	if err != nil {
		t.Fatal(err)
	}
	s := string(mustRead(t, written[0]))
	if !strings.Contains(s, "delivery: pipo") {
		t.Errorf("pipo delivery not stamped:\n%s", s)
	}
	if !strings.Contains(s, "COMMITS STAY LOCAL") || strings.Contains(s, "open a DRAFT PR") {
		t.Errorf("pipo style body wrong:\n%s", s)
	}
	// Provenance names the project override.
	if !strings.Contains(s, "pipo/cox/policy.json@") {
		t.Errorf("project override provenance missing:\n%s", s)
	}
}

func mustRead(t *testing.T, p string) []byte {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
