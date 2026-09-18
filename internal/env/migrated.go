package env

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Migrated allocation states.
const (
	// StateMigratedUnverified is a v1 allocation carried into v2 by cox migrate that has not yet been probed.
	StateMigratedUnverified = "migrated-unverified"
	// StateReleased is an allocation cox env reconcile confirmed is free (port not listening, env file gone).
	StateReleased = "released"
)

// MigratedAlloc is one story's v1 machine-layer allocation (port/db/redis/sim/env) carried into v2 for verification,
// stored at <epic>/.cox/env/<story>.json. v1's port/db/redis/sim allocation does not map onto the v2 env model, so
// migrate records it here as migrated-unverified rather than pretending it is live ownership; cox env reconcile probes
// each one and, once the port is free and the env file gone, releases it.
type MigratedAlloc struct {
	Story   string `json:"story"`
	Port    int    `json:"port,omitempty"`
	DB      string `json:"db,omitempty"`
	Redis   string `json:"redis,omitempty"`
	Sim     string `json:"sim,omitempty"`
	EnvFile string `json:"env_file,omitempty"`
	State   string `json:"state"`
}

// migratedDir is <epic>/.cox/env, the directory of per-story migrated allocation files.
func migratedDir(epicDir string) string { return filepath.Join(epicDir, ".cox", "env") }

// migratedPath is <epic>/.cox/env/<story>.json.
func migratedPath(epicDir, story string) string {
	return filepath.Join(migratedDir(epicDir), story+".json")
}

// WriteMigratedAlloc writes one story's migrated allocation file atomically.
func WriteMigratedAlloc(epicDir string, a MigratedAlloc) error {
	if err := os.MkdirAll(migratedDir(epicDir), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(a, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(migratedPath(epicDir, a.Story), append(b, '\n'), 0o644)
}

// LoadMigratedAllocs reads every <epic>/.cox/env/<story>.json, sorted by story. A missing directory is not an error
// (no migrated allocation is a valid state); a corrupt file is a hard error naming it.
func LoadMigratedAllocs(epicDir string) ([]MigratedAlloc, error) {
	entries, err := os.ReadDir(migratedDir(epicDir))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read migrated env dir: %w", err)
	}
	var out []MigratedAlloc
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		p := filepath.Join(migratedDir(epicDir), e.Name())
		b, err := os.ReadFile(p)
		if err != nil {
			return nil, err
		}
		var a MigratedAlloc
		if err := json.Unmarshal(b, &a); err != nil {
			return nil, fmt.Errorf("parse %s: %w", p, err)
		}
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Story < out[j].Story })
	return out, nil
}

// PortHeldByMigrated reports the story of the first non-released migrated allocation that holds port, or ("", false)
// when none does. cox env up consults it so it never hands out a port a migrated allocation still claims (until cox env
// reconcile has released it).
func PortHeldByMigrated(epicDir string, port int) (string, bool) {
	if port == 0 {
		return "", false
	}
	allocs, err := LoadMigratedAllocs(epicDir)
	if err != nil {
		return "", false
	}
	for _, a := range allocs {
		if a.Port == port && a.State != StateReleased {
			return a.Story, true
		}
	}
	return "", false
}

// MigratedResult is the outcome of reconciling one migrated allocation, for the CLI table.
type MigratedResult struct {
	Story    string
	Port     int
	State    string // the state after this pass (released when apply flipped it)
	PortLive bool   // a process is listening on the port
	PortPID  int    // the listener's pid (when PortLive)
	EnvGone  bool   // the env file no longer exists
	Note     string
}

// ReconcileMigrated probes every migrated allocation with ops.PortOwner (lsof) and reports each one. A port that is free
// with its env file gone is releasable: with apply it flips state to released and rewrites the file. A port still being
// listened on is kept and its pid reported. A probe error (no lsof, unreadable) is treated as unknown - never released.
func (a *Allocator) ReconcileMigrated(apply bool) ([]MigratedResult, error) {
	allocs, err := LoadMigratedAllocs(a.EpicDir)
	if err != nil {
		return nil, err
	}
	var out []MigratedResult
	for _, m := range allocs {
		r := MigratedResult{Story: m.Story, Port: m.Port, State: m.State}
		if m.EnvFile == "" {
			r.EnvGone = true
		} else if _, err := os.Stat(m.EnvFile); os.IsNotExist(err) {
			r.EnvGone = true
		}
		if m.Port > 0 {
			pid, _, occupied, perr := a.Ops.PortOwner(m.Port)
			switch {
			case perr != nil:
				r.Note = "port probe unknown (" + perr.Error() + "); kept"
				out = append(out, r)
				continue
			case occupied:
				r.PortLive, r.PortPID = true, pid
				r.Note = fmt.Sprintf("port %d listening (pid %d); kept", m.Port, pid)
				out = append(out, r)
				continue
			}
		}
		// Port free (or none) and env file gone => releasable.
		if r.EnvGone && m.State != StateReleased {
			if apply {
				m.State = StateReleased
				if err := WriteMigratedAlloc(a.EpicDir, m); err != nil {
					return out, err
				}
				r.State = StateReleased
				r.Note = "released (port free, env file gone)"
			} else {
				r.Note = "releasable (port free, env file gone); use --apply"
			}
		} else if m.State == StateReleased {
			r.Note = "already released"
		} else {
			r.Note = "port free but env file still present; kept"
		}
		out = append(out, r)
	}
	return out, nil
}
