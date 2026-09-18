package quota

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeProvider returns fixed readings and counts calls, so cache freshness and live-call telemetry can be asserted.
type fakeProvider struct {
	readings []Reading
	calls    int
}

func (f *fakeProvider) Read(context.Context) ([]Reading, error) {
	f.calls++
	return f.readings, nil
}

// The cache file holds only the projection: none of email, account, token, or stderr can appear, even after caching a
// projection derived from a real provider report.
func TestCacheNoRawFields(t *testing.T) {
	ws := t.TempDir()
	readings := parseQuotaAxi(mustFixture(t, "schema5-claude-fresh.json"), time.Now())
	if err := writeCache(CachePath(ws), readings, time.Now()); err != nil {
		t.Fatalf("writeCache: %v", err)
	}
	b, err := os.ReadFile(CachePath(ws))
	if err != nil {
		t.Fatalf("read cache: %v", err)
	}
	low := strings.ToLower(string(b))
	for _, banned := range []string{"email", "account", "token", "stderr"} {
		if strings.Contains(low, banned) {
			t.Fatalf("cache leaked %q: %s", banned, b)
		}
	}
	// The cache file is owner-only.
	info, err := os.Stat(CachePath(ws))
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("cache mode = %v, want 0600", info.Mode().Perm())
	}
}

// ReadCached serves a fresh cache without a live call, and refreshes once the TTL has passed.
func TestReadCachedTTL(t *testing.T) {
	ws := t.TempDir()
	p := &fakeProvider{readings: []Reading{{Harness: "claude", Known: true, PercentRemaining: 40, Source: SourceQuotaAxi}}}
	now := time.Now()

	if _, live, _ := ReadCached(context.Background(), ws, p, now); !live || p.calls != 1 {
		t.Fatalf("first read should call live (calls=%d live=%v)", p.calls, live)
	}
	if _, live, _ := ReadCached(context.Background(), ws, p, now.Add(30*time.Second)); live || p.calls != 1 {
		t.Fatalf("within TTL should serve cache (calls=%d live=%v)", p.calls, live)
	}
	if _, live, _ := ReadCached(context.Background(), ws, p, now.Add(90*time.Second)); !live || p.calls != 2 {
		t.Fatalf("past TTL should refresh (calls=%d live=%v)", p.calls, live)
	}
}

// A projection cache older than 15 minutes is never served when a refresh cannot help; it round-trips within the window.
func TestLoadCacheRoundTrip(t *testing.T) {
	ws := t.TempDir()
	want := []Reading{{Harness: "codex", Known: true, PercentRemaining: 46, Runway: RunwayThroughReset, UsableRunwaySeconds: NoRunway, Source: SourceQuotaAxi}}
	at := time.Now()
	if err := writeCache(CachePath(ws), want, at); err != nil {
		t.Fatalf("writeCache: %v", err)
	}
	got, gotAt, ok := loadCache(CachePath(ws))
	if !ok || len(got) != 1 || got[0].PercentRemaining != 46 || gotAt.Unix() != at.Unix() {
		t.Fatalf("round trip: got=%v at=%v ok=%v", got, gotAt, ok)
	}
	if _, _, ok := loadCache(filepath.Join(ws, "nope.json")); ok {
		t.Fatalf("absent cache should be ok=false")
	}
}

func TestReadCachedRetainsRecentLastKnownWhenAutomaticTurnsUnknown(t *testing.T) {
	ws := t.TempDir()
	now := time.Date(2026, 9, 16, 8, 0, 0, 0, time.UTC)
	known := Reading{Harness: "claude", Known: true, PercentRemaining: 42, Runway: RunwayThroughReset, Source: SourceQuotaAxi, ObservedAt: now.Format(time.RFC3339)}
	p := &fakeProvider{readings: []Reading{known}}
	if _, _, err := ReadCached(context.Background(), ws, p, now); err != nil {
		t.Fatal(err)
	}

	unknownAt := now.Add(2 * time.Minute)
	p.readings = []Reading{{Harness: "claude", Known: false, Runway: RunwayUnknown, Source: SourceQuotaAxi, Reason: "rate limited", ObservedAt: unknownAt.Format(time.RFC3339)}}
	current, live, err := ReadCached(context.Background(), ws, p, unknownAt)
	if err != nil || !live || len(current) != 1 || current[0].Known {
		t.Fatalf("current reading must remain live unknown: current=%+v live=%v err=%v", current, live, err)
	}
	last := RecentLastKnown(ws, unknownAt)
	if len(last) != 1 || last[0].PercentRemaining != 42 || last[0].ObservedAt != known.ObservedAt {
		t.Fatalf("recent last known reading not retained: %+v", last)
	}
	if stale := RecentLastKnown(ws, now.Add(16*time.Minute)); len(stale) != 0 {
		t.Fatalf("last known older than 15 minutes must not be shown: %+v", stale)
	}
}
