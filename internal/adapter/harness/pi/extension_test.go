package pi

import (
	"os"
	"path/filepath"
	"testing"
)

// InstallExtension writes the entry + support files and a hash marker under <root>/.pi/extensions/, and VerifyExtension
// then accepts the install and returns the -e entry path.
func TestInstallAndVerifyExtension(t *testing.T) {
	root := t.TempDir()
	entry, err := InstallExtension(root)
	if err != nil {
		t.Fatalf("InstallExtension: %v", err)
	}
	dir := filepath.Join(root, ExtensionRelDir)
	for _, name := range []string{ExtensionEntry, extensionSupport, extensionMarker} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("expected %s installed: %v", name, err)
		}
	}
	got, ok := VerifyExtension(root)
	if !ok {
		t.Fatalf("VerifyExtension must accept a fresh install")
	}
	if got != entry {
		t.Errorf("VerifyExtension entry = %q, want %q", got, entry)
	}
	if filepath.Base(entry) != ExtensionEntry {
		t.Errorf("entry basename = %q, want %q", filepath.Base(entry), ExtensionEntry)
	}
}

// A root with no installed extension (no marker) verifies false, so dispatch downgrades rather than claiming push/auto.
func TestVerifyExtensionMissing(t *testing.T) {
	if _, ok := VerifyExtension(t.TempDir()); ok {
		t.Fatalf("VerifyExtension must be false with no install")
	}
}

// A tampered (or stale) installed file no longer matches the embedded hash, so VerifyExtension returns false.
func TestVerifyExtensionTampered(t *testing.T) {
	root := t.TempDir()
	if _, err := InstallExtension(root); err != nil {
		t.Fatal(err)
	}
	entryPath := filepath.Join(root, ExtensionRelDir, ExtensionEntry)
	if err := os.WriteFile(entryPath, []byte("// tampered\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ok := VerifyExtension(root); ok {
		t.Fatalf("VerifyExtension must reject a tampered install")
	}
}

// ExtensionHash is stable and non-empty (used as the load/hash marker).
func TestExtensionHashStable(t *testing.T) {
	if h := ExtensionHash(); h == "" || h != ExtensionHash() {
		t.Fatalf("ExtensionHash must be stable and non-empty, got %q", h)
	}
}
