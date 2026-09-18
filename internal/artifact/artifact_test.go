package artifact

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSidecarPath(t *testing.T) {
	if got := SidecarPath("/a/reports/visual/design.html"); got != "/a/reports/visual/design.artifact.json" {
		t.Fatalf("sidecar path: %s", got)
	}
}

func TestWriteAndList(t *testing.T) {
	epic := t.TempDir()
	if err := os.MkdirAll(VisualDir(epic), 0o755); err != nil {
		t.Fatal(err)
	}
	// A source file whose sha the sidecar records.
	src := filepath.Join(epic, "DESIGN.md")
	if err := os.WriteFile(src, []byte("# design\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	source, err := NewSource(epic, src)
	if err != nil {
		t.Fatal(err)
	}
	if source.Path != "DESIGN.md" {
		t.Fatalf("source path should be epic-relative, got %q", source.Path)
	}
	if len(source.SHA256) != 64 {
		t.Fatalf("sha256 should be full hex, got %q", source.SHA256)
	}

	page := filepath.Join(VisualDir(epic), "design.html")
	if err := os.WriteFile(page, []byte("<html></html>"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Write(page, Sidecar{Kind: KindArena, Sources: []Source{source}, GeneratedAt: "2026-09-16T00:00:00Z", Generator: GenCox, SynthesisSHA: "abc123"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(SidecarPath(page)); err != nil {
		t.Fatalf("sidecar not written: %v", err)
	}

	cards, err := List(epic)
	if err != nil {
		t.Fatal(err)
	}
	if len(cards) != 1 {
		t.Fatalf("want 1 artifact, got %d", len(cards))
	}
	c := cards[0]
	if c.Schema != Schema || c.Kind != KindArena || c.Generator != GenCox || c.SynthesisSHA != "abc123" {
		t.Fatalf("bad sidecar: %+v", c)
	}
	if c.HTML != "design.html" {
		t.Fatalf("HTML basename: %q", c.HTML)
	}
	if len(c.Sources) != 1 || c.Sources[0].Path != "DESIGN.md" {
		t.Fatalf("sources: %+v", c.Sources)
	}
}

func TestListMissingDir(t *testing.T) {
	cards, err := List(t.TempDir())
	if err != nil || cards != nil {
		t.Fatalf("missing visual dir should yield no artifacts and no error, got %v %v", cards, err)
	}
}
