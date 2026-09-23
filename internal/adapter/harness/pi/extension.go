package pi

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// extensionFS embeds the Coxswain Pi extension sources so the cox binary carries them with no runtime file lookup:
// `cox workspace hooks --harness pi` and pi worker dispatch write them project-local. The deterministic test
// (extension/cox-supervisor.test.ts) is not embedded - it is not needed at runtime.
//
//go:embed extension/cox-pi.ts extension/cox-supervisor.ts extension/cox-commands.ts
var extensionFS embed.FS

// Extension install layout (project-local, never user-level Pi config). The entry file is what worker launch loads
// with `-e`; the hash marker records the installed content hash so dispatch can verify the extension before claiming
// the push/auto card.
const (
	// ExtensionRelDir is a project-local SUBDIR (relative to the worktree/clone root). Pi's extension discovery treats
	// each top-level `.ts`/`.js` in `.pi/extensions/` as its own extension, but resolves a SUBDIRECTORY to a single
	// extension via its `index.ts` (its helper modules are imported, never scanned). Installing under `coxswain/` so pi
	// loads only the entry - a flat layout made pi try to load cox-supervisor.ts / cox-commands.ts as extensions and fail
	// (dogfood, once `--no-extensions` was dropped for global-package parity).
	ExtensionRelDir  = ".pi/extensions/coxswain"
	ExtensionEntry   = "index.ts" // the entry pi auto-discovers for the subdir (and the -e path); content is cox-pi.ts
	extensionSupport = "cox-supervisor.ts"
	extensionCmds    = "cox-commands.ts"
	extensionMarker  = ".cox-pi.hash"      // load/hash marker written by cox
	extensionEpic    = "cox-pi.epic"       // epic binding the extension reads when COX_EPIC is absent (bound leader install)
	extensionActive  = ".cox-pi.activated" // runtime handshake: the extension writes this on session_start when Pi loads it
)

// extensionSources returns the embedded {installed-basename: content} the extension is made of, in a stable order. The
// entry is installed as index.ts (from the embedded cox-pi.ts) so pi discovers the coxswain/ subdir as one extension.
func extensionSources() map[string][]byte {
	files := map[string]string{
		ExtensionEntry:   "extension/cox-pi.ts",
		extensionSupport: "extension/cox-supervisor.ts",
		extensionCmds:    "extension/cox-commands.ts",
	}
	src := map[string][]byte{}
	for installed, embedPath := range files {
		b, err := extensionFS.ReadFile(embedPath)
		if err != nil {
			// Embedded content is compiled in; a read error here is a build defect, not a runtime condition.
			panic(fmt.Sprintf("pi: embedded extension %q missing: %v", embedPath, err))
		}
		src[installed] = b
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

// InstallExtension writes the embedded extension sources into <root>/.pi/extensions/coxswain/ (the entry as index.ts), a
// hash marker, and (when epic is non-empty) an epic-binding marker the extension reads when COX_EPIC is absent (a bound
// leader install). It returns the absolute entry path (index.ts) pi loads. It never touches user-level Pi config.
// Idempotent: a re-run rewrites the same bytes and markers.
func InstallExtension(root, epic string) (entryPath string, err error) {
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
	if epic != "" {
		if err := os.WriteFile(filepath.Join(dir, extensionEpic), []byte(epic+"\n"), 0o644); err != nil {
			return "", fmt.Errorf("pi InstallExtension: write epic marker: %w", err)
		}
	}
	abs, err := filepath.Abs(filepath.Join(dir, ExtensionEntry))
	if err != nil {
		return "", fmt.Errorf("pi InstallExtension: resolve entry: %w", err)
	}
	return abs, nil
}

// activationPath is the runtime activation marker the extension writes when Pi loads it at session_start.
func activationPath(root string) string {
	return filepath.Join(root, ExtensionRelDir, extensionActive)
}

// ClearActivation removes any stale activation marker before a spawn, so a prior run's marker cannot falsely confirm
// this launch. A missing marker is not an error.
func ClearActivation(root string) error {
	if err := os.Remove(activationPath(root)); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// ExtensionActivated reports whether the extension has confirmed it loaded at runtime (the marker exists). Byte-level
// VerifyExtension proves the install is intact; ExtensionActivated proves Pi actually loaded and ran it, which a
// version/API/load failure would leave false.
func ExtensionActivated(root string) bool {
	_, err := os.Stat(activationPath(root))
	return err == nil
}

// WaitActivation polls for the activation marker up to timeout, returning true as soon as the extension confirms it
// loaded. It is the startup handshake dispatch uses to decide whether Pi's push/auto card holds or must downgrade.
func WaitActivation(root string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for {
		if ExtensionActivated(root) {
			return true
		}
		if !time.Now().Before(deadline) {
			return false
		}
		time.Sleep(250 * time.Millisecond)
	}
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
