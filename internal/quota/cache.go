package quota

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// Cache windows (DESIGN): a projection is authoritative for 60s (the poll never calls quota-axi more than once per
// interval), and a cached projection older than 15 minutes is never served - it becomes unknown. Only the projection is
// cached, never raw quota-axi output.
const (
	cacheTTL      = 60 * time.Second
	cacheMaxStale = 15 * time.Minute
)

// cacheFile is what lands on disk: the schema id, when the projection was read, and the Readings. It carries no raw
// provider output, no stderr, and no identity fields, so nothing sensitive is copied into project state.
type cacheFile struct {
	Schema     string    `json:"schema"`
	ObservedAt string    `json:"observed_at"`
	Readings   []Reading `json:"readings"`
	LastKnown  []Reading `json:"last_known,omitempty"`
}

// CachePath is the projection cache location: <workspace>/cox/.cache/quota.json.
func CachePath(workspaceRoot string) string {
	return filepath.Join(workspaceRoot, "cox", ".cache", "quota.json")
}

// ReadCached returns the quota readings, serving the projection cache when it is fresh (< 60s) and otherwise refreshing
// through the provider and rewriting the cache. It returns calledLive=true whenever it invoked the provider, so the
// watcher can count real calls for telemetry. When a refresh fails but a cached projection under 15 minutes old exists,
// that projection is served; beyond 15 minutes with no refresh the result is unknown for every harness.
func ReadCached(ctx context.Context, workspaceRoot string, p Provider, now time.Time) (readings []Reading, calledLive bool, err error) {
	path := CachePath(workspaceRoot)
	previous, at, cached := loadCacheFile(path)
	if cached && now.Sub(at) < cacheTTL {
		return previous.Readings, false, nil
	}
	fresh, rerr := p.Read(ctx)
	if rerr == nil {
		_ = writeCacheFile(path, fresh, mergeLastKnown(previous, fresh, now), now)
		return fresh, true, nil
	}
	if cached && now.Sub(at) < cacheMaxStale {
		return previous.Readings, true, nil
	}
	return unknownAll(SourceQuotaAxi, "quota-axi unavailable and cache stale: "+rerr.Error(), now), true, rerr
}

// RecentLastKnown returns cached automatic Known readings observed within the last 15 minutes. They are display-only
// context for a current unknown reading and never replace the current projection used by routing, wakes, or gates.
func RecentLastKnown(workspaceRoot string, now time.Time) []Reading {
	cf, _, ok := loadCacheFile(CachePath(workspaceRoot))
	if !ok {
		return nil
	}
	return recentKnown(cf.LastKnown, now)
}

// loadCache reads the projection cache without following a symlink. A missing, unreadable, wrong-schema, or malformed
// cache is treated as absent (ok=false), never as a fatal error.
func loadCache(path string) ([]Reading, time.Time, bool) {
	cf, at, ok := loadCacheFile(path)
	return cf.Readings, at, ok
}

func loadCacheFile(path string) (cacheFile, time.Time, bool) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return cacheFile{}, time.Time{}, false
	}
	defer f.Close()
	data, err := io.ReadAll(f)
	if err != nil {
		return cacheFile{}, time.Time{}, false
	}
	var cf cacheFile
	if err := json.Unmarshal(data, &cf); err != nil || cf.Schema != Schema {
		return cacheFile{}, time.Time{}, false
	}
	at, err := time.Parse(time.RFC3339, cf.ObservedAt)
	if err != nil {
		return cacheFile{}, time.Time{}, false
	}
	return cf, at, true
}

// writeCache atomically writes the projection with owner-only permissions and no symlink following: it writes a temp
// file (O_CREATE|O_EXCL|O_NOFOLLOW, 0600) in the same directory and renames it over the target, so a concurrent reader
// never sees a half-written file and a pre-planted symlink at the target is replaced, not written through.
func writeCache(path string, readings []Reading, now time.Time) error {
	return writeCacheFile(path, readings, recentKnown(readings, now), now)
}

func writeCacheFile(path string, readings, lastKnown []Reading, now time.Time) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(cacheFile{Schema: Schema, ObservedAt: now.UTC().Format(time.RFC3339), Readings: readings, LastKnown: lastKnown})
	if err != nil {
		return err
	}
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

func mergeLastKnown(previous cacheFile, fresh []Reading, now time.Time) []Reading {
	byKey := map[string]Reading{}
	var order []string
	put := func(r Reading) {
		if !r.Known {
			return
		}
		key := r.Harness + "\x00" + r.Model
		if _, ok := byKey[key]; !ok {
			order = append(order, key)
		}
		byKey[key] = r
	}
	for _, r := range previous.LastKnown {
		put(r)
	}
	for _, r := range previous.Readings {
		put(r)
	}
	for _, r := range fresh {
		put(r)
	}
	out := make([]Reading, 0, len(order))
	for _, key := range order {
		out = append(out, byKey[key])
	}
	return recentKnown(out, now)
}

func recentKnown(readings []Reading, now time.Time) []Reading {
	out := make([]Reading, 0, len(readings))
	for _, r := range readings {
		if !r.Known {
			continue
		}
		observed, err := time.Parse(time.RFC3339, r.ObservedAt)
		if err != nil || now.Sub(observed) > cacheMaxStale {
			continue
		}
		out = append(out, r)
	}
	return out
}
