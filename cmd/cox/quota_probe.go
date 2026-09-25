package main

import (
	"time"

	"github.com/nphattai/coxswain/internal/state"
	"github.com/nphattai/coxswain/internal/watch"
)

// quotaSettings are the resolved quota thresholds for the watcher pass and the dispatch gate.
type quotaSettings struct {
	LowPercent     int
	MinRunwayHours int
	PollMinutes    int
}

// quotaSettingsFor resolves the quota thresholds for an epic from its policy quota section, falling back to the code
// defaults when policy is absent or unset.
func quotaSettingsFor(epicDir string) quotaSettings {
	pol := loadPolicyQuiet(epicDir)
	return quotaSettings{
		LowPercent:     pol.QuotaLowPercent(),
		MinRunwayHours: pol.QuotaMinRunwayHours(),
		PollMinutes:    pol.QuotaPollMinutes(),
	}
}

// quotaProbe implements watch.QuotaProbe: it feeds the watcher's quota pass the merged/automatic readings, the targets
// (leader harness plus every working story's harness/model), and the policy thresholds.
type quotaProbe struct {
	epicDir  string
	settings quotaSettings
}

func newQuotaProbe(epicDir string) *quotaProbe {
	return &quotaProbe{epicDir: epicDir, settings: quotaSettingsFor(epicDir)}
}

func (p *quotaProbe) Read() (watch.QuotaSnapshot, error) {
	merged, auto, live := quotaSnapshot(p.epicDir)
	return watch.QuotaSnapshot{Merged: merged, Auto: auto, CalledLive: live}, nil
}

func (p *quotaProbe) Targets() []watch.QuotaTarget {
	leaderH := "claude"
	if pol := loadPolicyQuiet(p.epicDir); pol != nil && pol.Harness.Leader.Default != "" {
		leaderH = pol.Harness.Leader.Default
	}
	out := []watch.QuotaTarget{{Harness: leaderH, Role: "leader"}}
	events, _, err := state.Load(p.epicDir)
	if err != nil {
		return out
	}
	for _, s := range state.Fold(events).SortedStories() {
		if s.State != state.Working && s.State != state.InputRequired {
			continue
		}
		h, m := storyHarnessModel(p.epicDir, s)
		out = append(out, watch.QuotaTarget{Harness: h, Model: m, Role: "worker", Story: s.ID})
	}
	return out
}

func (p *quotaProbe) LowPercent() int         { return p.settings.LowPercent }
func (p *quotaProbe) MinRunwaySeconds() int64 { return int64(p.settings.MinRunwayHours) * 3600 }
func (p *quotaProbe) PollInterval() time.Duration {
	return time.Duration(p.settings.PollMinutes) * time.Minute
}
