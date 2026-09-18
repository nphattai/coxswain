package quota

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// ManualDirName is the per-epic directory holding captain-declared quota files, one per harness.
const ManualDirName = "quota-manual"

// LeaderActor is the only actor allowed to write a manual quota entry: manual readings are privileged routing input, so
// a worker (a real COX_STORY) can never set, suppress, or steer them.
const LeaderActor = "_leader"

// ManualEntry is one captain-declared quota reading, persisted at <epic>/.cox/quota-manual/<harness>.json (0600,
// O_NOFOLLOW). It is a fallback used only when the automatic source is unknown, it expires (Until is mandatory), and it
// can never assert a runway - a bare percentage cannot prove reset-and-pace, so a manual Reading is always Runway
// unknown, never through_reset.
type ManualEntry struct {
	Harness string `json:"harness"`
	Model   string `json:"model,omitempty"`
	Percent int    `json:"percent"`
	Until   string `json:"until"` // RFC3339 expiry (mandatory)
	Actor   string `json:"actor"` // who declared it (always _leader; workers are refused)
	At      string `json:"at"`    // RFC3339 write time
}

// Manual reads the captain-declared quota files for an epic. It fills coxswain.quota.v1 from ManualEntry files, expiring
// each at its Until and always leaving Runway unknown.
type Manual struct {
	EpicDir string
	Now     func() time.Time
}

func (m *Manual) now() time.Time {
	if m.Now != nil {
		return m.Now()
	}
	return time.Now()
}

// ManualDir is <epic>/.cox/quota-manual.
func ManualDir(epicDir string) string {
	return filepath.Join(epicDir, ".cox", ManualDirName)
}

func manualPath(epicDir, harness string) string {
	return filepath.Join(ManualDir(epicDir), harness+".json")
}

// Read returns one Reading per manual file: known when unexpired, unknown (with a reason) once past Until. Runway is
// always unknown. A file that cannot be read or parsed is skipped (an absent manual source is not an error).
func (m *Manual) Read(context.Context) ([]Reading, error) {
	entries, err := os.ReadDir(ManualDir(m.EpicDir))
	if err != nil {
		return nil, nil // no manual dir => no manual readings
	}
	now := m.now()
	var out []Reading
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		me, err := loadManual(filepath.Join(ManualDir(m.EpicDir), e.Name()))
		if err != nil {
			continue // skip a corrupt or unreadable manual file
		}
		out = append(out, me.reading(now))
	}
	return out, nil
}

// reading projects a ManualEntry to a Reading at time now, expiring it at Until.
func (e ManualEntry) reading(now time.Time) Reading {
	base := Reading{
		Harness:             e.Harness,
		Model:               e.Model,
		Runway:              RunwayUnknown,
		UsableRunwaySeconds: NoRunway,
		Source:              SourceManual,
		ObservedAt:          e.At,
		ResetsAt:            e.Until,
	}
	until, err := time.Parse(time.RFC3339, e.Until)
	if err != nil {
		base.Reason = "manual quota has an invalid until; treated as expired"
		return base
	}
	if !now.Before(until) {
		base.Reason = fmt.Sprintf("manual quota expired at %s (set by %s)", e.Until, e.Actor)
		return base
	}
	base.Known = true
	base.PercentRemaining = e.Percent
	base.Reason = fmt.Sprintf("manual (set by %s, expires %s)", e.Actor, e.Until)
	return base
}

// loadManual reads a manual entry file without following a symlink.
func loadManual(path string) (ManualEntry, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return ManualEntry{}, err
	}
	defer f.Close()
	data, err := io.ReadAll(f)
	if err != nil {
		return ManualEntry{}, err
	}
	var e ManualEntry
	if err := json.Unmarshal(data, &e); err != nil {
		return ManualEntry{}, err
	}
	return e, nil
}

// WriteManual persists a manual entry (owner-only, atomic, no symlink following). The caller has already enforced that
// only the leader may write and that Until is a valid future RFC3339 time.
func WriteManual(epicDir string, e ManualEntry) error {
	dir := ManualDir(epicDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(e, "", "  ")
	if err != nil {
		return err
	}
	path := manualPath(epicDir, e.Harness)
	tmp := path + ".tmp"
	_ = os.Remove(tmp)
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// ClearManual removes a harness's manual entry. A missing file is not an error (unset is idempotent).
func ClearManual(epicDir, harness string) error {
	if err := os.Remove(manualPath(epicDir, harness)); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
