package pi

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// extensionFS embeds the Coxswain Pi extension sources so the cox binary carries them with no runtime file lookup:
// `cox workspace hooks --harness pi` and pi worker dispatch write them project-local. The deterministic test
// (extension/cox-supervisor.test.ts) is not embedded - it is not needed at runtime.
//
//go:embed extension/cox-pi.ts extension/cox-supervisor.ts
var extensionFS embed.FS

// Extension install layout (project-local, never user-level Pi config). The entry file is what worker launch loads
// with `-e`; the hash marker records the installed content hash so dispatch can verify the extension before claiming
// the push/auto card.
const (
	ExtensionRelDir  = ".pi/extensions" // project-local dir, relative to the worktree/clone root
	ExtensionEntry   = "cox-pi.ts"      // the -e entry file
	extensionSupport = "cox-supervisor.ts"
	extensionMarker  = ".cox-pi.hash" // load/hash marker written by cox
)

// extensionSources returns the embedded {basename: content} the extension is made of, in a stable order.
func extensionSources() map[string][]byte {
	src := map[string][]byte{}
	for _, name := range []string{ExtensionEntry, extensionSupport} {
		b, err := extensionFS.ReadFile("extension/" + name)
		if err != nil {
			// Embedded content is compiled in; a read error here is a build defect, not a runtime condition.
			panic(fmt.Sprintf("pi: embedded extension %q missing: %v", name, err))
		}
		src[name] = b
	}
	return src
}

// ExtensionHash is the deterministic sha256 over the embedded extension files (name + content, name-sorted). The
// installed copy is verified against it so a tampered or stale install is caught before dispatch claims push/auto.
func ExtensionHash() string {
	return hashSources(extensionSources())
}

func hashSources(src map[string][]byte) string {
	names := make([]string, 0, len(src))
	for n := range src {
		names = append(names, n)
	}
	sort.Strings(names)
	h := sha256.New()
	for _, n := range names {
		h.Write([]byte(n))
		h.Write([]byte{0})
		h.Write(src[n])
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// InstallExtension writes the embedded extension (entry + support) into <root>/.pi/extensions/ and a hash marker, and
// returns the absolute entry path to load with `-e`. It never touches user-level Pi config. Idempotent: a re-run
// rewrites the same bytes and marker.
func InstallExtension(root string) (entryPath string, err error) {
	dir := filepath.Join(root, ExtensionRelDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("pi InstallExtension: mkdir %s: %w", dir, err)
	}
	src := extensionSources()
	for name, content := range src {
		if err := os.WriteFile(filepath.Join(dir, name), content, 0o644); err != nil {
			return "", fmt.Errorf("pi InstallExtension: write %s: %w", name, err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, extensionMarker), []byte(hashSources(src)+"\n"), 0o644); err != nil {
		return "", fmt.Errorf("pi InstallExtension: write marker: %w", err)
	}
	abs, err := filepath.Abs(filepath.Join(dir, ExtensionEntry))
	if err != nil {
		return "", fmt.Errorf("pi InstallExtension: resolve entry: %w", err)
	}
	return abs, nil
}

// VerifyExtension reports whether the extension installed under root is present and byte-identical to the embedded
// version (the marker is present and the installed files re-hash to the embedded hash). It returns the entry path to
// load with `-e` when ok. A missing, partial, or tampered install returns ok=false, so dispatch downgrades the
// effective card rather than claiming push/auto over an unverified extension.
func VerifyExtension(root string) (entryPath string, ok bool) {
	dir := filepath.Join(root, ExtensionRelDir)
	if _, err := os.Stat(filepath.Join(dir, extensionMarker)); err != nil {
		return "", false // no load marker: cox did not install it here
	}
	installed := map[string][]byte{}
	for name := range extensionSources() {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return "", false // missing or unreadable file
		}
		installed[name] = b
	}
	if hashSources(installed) != ExtensionHash() {
		return "", false // tampered or stale relative to the embedded version
	}
	abs, err := filepath.Abs(filepath.Join(dir, ExtensionEntry))
	if err != nil {
		return "", false
	}
	return abs, true
}
