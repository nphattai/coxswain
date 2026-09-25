package pi

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// InstallExtension writes the entry + support files and a hash marker under <root>/.pi/extensions/, and VerifyExtension
// then accepts the install and returns the -e entry path.
func TestInstallAndVerifyExtension(t *testing.T) {
	root := t.TempDir()
	entry, err := InstallExtension(root, "/Users/x/epics/v2")
	if err != nil {
		t.Fatalf("InstallExtension: %v", err)
	}
	dir := filepath.Join(root, ExtensionRelDir)
	for _, name := range []string{ExtensionEntry, extensionSupport, extensionCmds, extensionMarker, extensionEpic} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("expected %s installed: %v", name, err)
		}
	}
	// The epic marker carries the binding the extension reads when COX_EPIC is absent.
	if b, _ := os.ReadFile(filepath.Join(dir, extensionEpic)); strings.TrimSpace(string(b)) != "/Users/x/epics/v2" {
		t.Errorf("epic marker = %q, want the epic dir", string(b))
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
	if _, err := InstallExtension(root, ""); err != nil {
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

// The activation handshake distinguishes an intact install (bytes) from a confirmed runtime load: ExtensionActivated is
// false until the marker exists, ClearActivation removes it, and WaitActivation returns as soon as it appears.
func TestActivationHandshake(t *testing.T) {
	root := t.TempDir()
	if _, err := InstallExtension(root, ""); err != nil {
		t.Fatal(err)
	}
	// Freshly installed: verified bytes, but NOT activated (Pi has not loaded it yet).
	if _, ok := VerifyExtension(root); !ok {
		t.Fatal("install must verify")
	}
	if ExtensionActivated(root) {
		t.Fatal("must not be activated before the extension runs")
	}
	// A short wait with no marker times out (unconfirmed -> caller downgrades).
	if WaitActivation(root, 200*time.Millisecond) {
		t.Fatal("WaitActivation must be false when the marker never appears")
	}
	// The extension writes the marker on load; WaitActivation then confirms.
	go func() {
		time.Sleep(50 * time.Millisecond)
		_ = os.WriteFile(activationPath(root), []byte("2026-09-20T10:00:00Z"), 0o644)
	}()
	if !WaitActivation(root, 2*time.Second) {
		t.Fatal("WaitActivation must confirm once the marker appears")
	}
	if !ExtensionActivated(root) {
		t.Fatal("ExtensionActivated must be true after the marker is written")
	}
	// ClearActivation removes a stale marker so the next launch confirms itself, not a prior run.
	if err := ClearActivation(root); err != nil {
		t.Fatalf("ClearActivation: %v", err)
	}
	if ExtensionActivated(root) {
		t.Fatal("ClearActivation must remove the marker")
	}
}

// ExtensionHash is stable and non-empty (used as the load/hash marker).
func TestExtensionHashStable(t *testing.T) {
	if h := ExtensionHash(); h == "" || h != ExtensionHash() {
		t.Fatalf("ExtensionHash must be stable and non-empty, got %q", h)
	}
}

// ExtensionCurrent is true only for exactly what InstallExtension would write: a fresh root, a tampered file and a
// different epic binding all read not current (B-55a).
func TestExtensionCurrent(t *testing.T) {
	root := t.TempDir()
	if ExtensionCurrent(root, "") {
		t.Fatal("an empty root reads current")
	}
	if _, err := InstallExtension(root, "/e1"); err != nil {
		t.Fatal(err)
	}
	if !ExtensionCurrent(root, "/e1") {
		t.Fatal("a fresh install does not read current")
	}
	if ExtensionCurrent(root, "/e2") || ExtensionCurrent(root, "") {
		t.Error("a different binding (another epic, or unbound) reads current")
	}
	// An unbound install over a bound one drops the marker, and then reads current unbound.
	if _, err := InstallExtension(root, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, ExtensionRelDir, extensionEpic)); !os.IsNotExist(err) {
		t.Errorf("an unbound install kept the old epic marker (err %v)", err)
	}
	if !ExtensionCurrent(root, "") {
		t.Error("an unbound install does not read current unbound")
	}
	if _, err := InstallExtension(root, "/e1"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ExtensionRelDir, ExtensionEntry), []byte("// tampered\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if ExtensionCurrent(root, "/e1") {
		t.Error("a tampered install reads current")
	}
}
