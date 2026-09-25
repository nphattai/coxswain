// Package supervision is the supervision-need registry, ported from firstmate bin/fm-supervision-lib.sh
// fm_supervision_status, bin/fm-check-register.sh, bin/fm-check-unregister.sh and bin/fm-check-lib.sh (pinned
// a8572f6). Beyond its open stories, an epic needs a live watcher while it has a registered process-event source
// (<control>/procevent/<id>.source) or a registered custom check: a <control>/<id>.check.sh bound to its bytes by a
// <control>/<id>.check-trust record that Register writes. Presence of the binding is the whole need test - a check
// whose bytes drifted still needs the watcher, so the watcher's sweep can report the rejection instead of going quiet.
package supervision

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// trustVersion is the check-trust record's first line (firstmate fm-custom-check-v1).
const trustVersion = "cox-custom-check-v1"

// idRe is the id grammar a check or source may use: path-safe, no leading dot (firstmate fm_task_id_path_safe).
var idRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// Need is what an epic's control dir registers beyond its stories.
type Need struct {
	Sources int // registered process-event sources
	Checks  int // registered custom checks
}

// Status counts the registered sources and checks under control (an epic's .cox dir).
func Status(control string) Need {
	var n Need
	if m, _ := filepath.Glob(filepath.Join(control, "procevent", "*.source")); m != nil {
		n.Sources = len(m)
	}
	checks, _ := filepath.Glob(filepath.Join(control, "*.check.sh"))
	for _, c := range checks {
		id := strings.TrimSuffix(filepath.Base(c), ".check.sh")
		if _, err := os.Stat(filepath.Join(control, id+".check-trust")); err == nil {
			n.Checks++
		}
	}
	return n
}

func sha256File(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

// privateRegular reports a regular, non-symlink file with exactly mode perm.
func privateRegular(path string, perm os.FileMode) bool {
	info, err := os.Lstat(path)
	return err == nil && info.Mode().IsRegular() && info.Mode().Perm() == perm
}

// Register binds <control>/<id>.check.sh (a private 0700 regular file) to its current bytes.
func Register(control, id string) error {
	if !idRe.MatchString(id) {
		return errors.New("invalid custom check registration")
	}
	check := filepath.Join(control, id+".check.sh")
	if !privateRegular(check, 0o700) {
		return errors.New("custom check is unavailable (want a regular 0700 file " + check + ")")
	}
	trust := filepath.Join(control, id+".check-trust")
	if info, err := os.Lstat(trust); err == nil && !info.Mode().IsRegular() {
		return errors.New("custom check trust path is unavailable")
	}
	hash, err := sha256File(check)
	if err != nil {
		return fmt.Errorf("custom check hash is unavailable: %w", err)
	}
	tmp, err := os.CreateTemp(control, ".cox-custom-check-trust.*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := fmt.Fprintf(tmp, "%s\n%s\n", trustVersion, hash); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), trust); err != nil {
		return err
	}
	if !Registered(control, id) {
		_ = os.Remove(trust)
		return errors.New("custom check registration could not be verified")
	}
	return nil
}

// Registered reports whether the check's current bytes still match its trust record (fm_custom_check_registered).
func Registered(control, id string) bool {
	_, ok := RegisteredBytes(control, id)
	return ok
}

// RegisteredBytes reads the check once and returns those bytes only when they are exactly what its trust record vouches
// for, so a caller runs the bytes that were verified (fm_custom_check_snapshot_prepare hashes the private copy it runs).
func RegisteredBytes(control, id string) ([]byte, bool) {
	if !idRe.MatchString(id) {
		return nil, false
	}
	trust := filepath.Join(control, id+".check-trust")
	if !privateRegular(trust, 0o600) {
		return nil, false
	}
	b, err := os.ReadFile(trust)
	if err != nil {
		return nil, false
	}
	lines := strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
	if len(lines) != 2 || lines[0] != trustVersion || !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(lines[1]) {
		return nil, false
	}
	check := filepath.Join(control, id+".check.sh")
	if !privateRegular(check, 0o700) {
		return nil, false
	}
	body, err := os.ReadFile(check)
	if err != nil {
		return nil, false
	}
	sum := sha256.Sum256(body)
	if hex.EncodeToString(sum[:]) != lines[1] {
		return nil, false
	}
	return body, true
}

// Unregister retires a check and its binding; a non-regular artifact is refused rather than removed.
func Unregister(control, id string) error {
	if !idRe.MatchString(id) {
		return errors.New("invalid custom check registration")
	}
	paths := []string{filepath.Join(control, id+".check.sh"), filepath.Join(control, id+".check-trust")}
	for _, p := range paths {
		if info, err := os.Lstat(p); err == nil && !info.Mode().IsRegular() {
			return errors.New("custom check is unsafe to remove")
		}
	}
	for _, p := range paths {
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("custom check could not be removed: %w", err)
		}
	}
	return nil
}
