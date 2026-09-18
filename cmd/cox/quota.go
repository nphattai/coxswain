package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/nphattai/coxswain/internal/quota"
	"github.com/nphattai/coxswain/internal/state"
)

// cmdQuota implements `cox quota [--json]` (the table, item 4) and `cox quota set|unset` (the captain-declared manual
// source, leader-owned). Manual readings are a fallback used only when the automatic source is unknown; they expire, are
// provenance-tagged, and can never assert a runway.
func cmdQuota(args []string) int {
	if len(args) > 0 {
		switch args[0] {
		case "set":
			return quotaSet(args[1:])
		case "unset":
			return quotaUnset(args[1:])
		}
	}
	return quotaTable(args)
}

// quotaSet implements `cox quota set <harness> <percent> --until <RFC3339> [--model m] --epic <dir>`. Only the leader may
// write (a real COX_STORY worker is refused); the entry is owner-only, atomic, no-symlink, carries actor+timestamp, has a
// mandatory future expiry, and never asserts a runway. It appends a quota_manual_set audit event.
func quotaSet(args []string) int {
	harnessName, percentStr, rest := twoPositional(args)
	fs := flag.NewFlagSet("quota set", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	epicDir := fs.String("epic", "", "epic directory")
	until := fs.String("until", "", "RFC3339 expiry (required); the reading is unknown after it")
	model := fs.String("model", "", "optional model family this reading is scoped to")
	if err := fs.Parse(rest); err != nil {
		return 2
	}
	if *epicDir == "" || harnessName == "" || percentStr == "" || *until == "" {
		return usageErr("cox quota set <harness> <percent> --until <RFC3339> [--model m] --epic <dir>")
	}
	actor := strings.TrimSpace(os.Getenv("COX_STORY"))
	if actor == "" {
		actor = quota.LeaderActor
	}
	if actor != quota.LeaderActor {
		return fail("manual quota is leader-owned; COX_STORY=%s (a worker) cannot set it", actor)
	}
	percent, err := parsePercent(percentStr)
	if err != nil {
		return fail("%v", err)
	}
	untilTime, err := time.Parse(time.RFC3339, *until)
	if err != nil {
		return fail("--until must be RFC3339 (e.g. 2026-09-20T04:00:00Z): %v", err)
	}
	now := time.Now().UTC()
	if !untilTime.After(now) {
		return fail("--until %s is not in the future; a manual reading must expire later than now", *until)
	}
	entry := quota.ManualEntry{
		Harness: harnessName,
		Model:   *model,
		Percent: percent,
		Until:   untilTime.Format(time.RFC3339),
		Actor:   actor,
		At:      now.Format(time.RFC3339),
	}
	if err := quota.WriteManual(*epicDir, entry); err != nil {
		return fail("write manual quota: %v", err)
	}
	if err := appendQuotaManualEvent(*epicDir, "set", entry); err != nil {
		return fail("record quota_manual_set: %v", err)
	}
	fmt.Printf("manual quota set: %s %d%% until %s (model=%s, actor=%s)\n", harnessName, percent, entry.Until, orNone(*model), actor)
	return 0
}

// quotaUnset implements `cox quota unset <harness> --epic <dir>`: it clears the harness's manual entry (idempotent) and
// records a quota_manual_set audit event with action=unset. Only the leader may clear.
func quotaUnset(args []string) int {
	harnessName, rest := onePositional(args)
	fs := flag.NewFlagSet("quota unset", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	epicDir := fs.String("epic", "", "epic directory")
	if err := fs.Parse(rest); err != nil {
		return 2
	}
	if *epicDir == "" || harnessName == "" {
		return usageErr("cox quota unset <harness> --epic <dir>")
	}
	actor := strings.TrimSpace(os.Getenv("COX_STORY"))
	if actor == "" {
		actor = quota.LeaderActor
	}
	if actor != quota.LeaderActor {
		return fail("manual quota is leader-owned; COX_STORY=%s (a worker) cannot unset it", actor)
	}
	if err := quota.ClearManual(*epicDir, harnessName); err != nil {
		return fail("clear manual quota: %v", err)
	}
	if err := appendQuotaManualEvent(*epicDir, "unset", quota.ManualEntry{Harness: harnessName, Actor: actor, At: time.Now().UTC().Format(time.RFC3339)}); err != nil {
		return fail("record quota_manual_set: %v", err)
	}
	fmt.Printf("manual quota unset: %s\n", harnessName)
	return 0
}

// appendQuotaManualEvent records an epic-scoped quota_manual_set fact (set or unset) for the audit trail. It is a
// non-lifecycle event (Type set, story _epic), so the story fold ignores it.
func appendQuotaManualEvent(epicDir, action string, e quota.ManualEntry) error {
	ev := map[string]any{"action": action, "harness": e.Harness, "actor": e.Actor}
	if action == "set" {
		ev["percent"] = e.Percent
		ev["until"] = e.Until
		if e.Model != "" {
			ev["model"] = e.Model
		}
	}
	return state.Append(epicDir, state.Event{
		Type:     state.QuotaManualSet,
		Epic:     filepath.Base(epicDir),
		Story:    state.EpicStory,
		Actor:    state.Leader,
		Evidence: ev,
	})
}

// quotaTable implements `cox quota [--json]`: the merged (automatic + manual) readings, one row per (harness, model),
// showing percent, runway, resets, source, observed, and reason. It never fails on a missing source (a missing quota-axi
// or an absent manual file just renders unknown rows).
func quotaTable(args []string) int {
	fs := flag.NewFlagSet("quota", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	epicDir := fs.String("epic", "", "epic directory")
	asJSON := fs.Bool("json", false, "emit coxswain.quota.v1 readings as JSON")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *epicDir == "" {
		return usageErr("cox quota [--json] --epic <dir>  |  cox quota set|unset ...")
	}
	readings := mergedQuotaReadings(*epicDir)
	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(readings); err != nil {
			return fail("%v", err)
		}
		return 0
	}
	lastKnown := recentLastKnownQuotaReadings(*epicDir)
	fmt.Printf("%-8s %-8s %-8s %-20s %-9s %-20s %s\n", "harness", "model", "percent", "runway", "source", "observed", "reason/resets")
	for _, r := range readings {
		fmt.Printf("%-8s %-8s %-8s %-20s %-9s %-20s %s\n",
			r.Harness, orDash(r.Model), quotaPercent(r), r.Runway, r.Source, orDash(r.ObservedAt), quotaTail(r, quota.Pick(lastKnown, r.Harness, r.Model)))
	}
	return 0
}

func recentLastKnownQuotaReadings(epicDir string) []quota.Reading {
	ws, err := findWorkspaceRoot(epicDir)
	if err != nil {
		return nil
	}
	return quota.RecentLastKnown(ws, time.Now())
}

// mergedQuotaReadings is the single path every surface uses: the merged automatic+manual readings.
func mergedQuotaReadings(epicDir string) []quota.Reading {
	merged, _, _ := quotaSnapshot(epicDir)
	return merged
}

// quotaSnapshot reads the automatic source through the projection cache (or directly when no workspace root is found)
// and the manual source, and returns the merged readings, the automatic-only readings, and whether a live call was made.
// It is best-effort - a source that cannot be read contributes unknown rows, never an error.
func quotaSnapshot(epicDir string) (merged, auto []quota.Reading, calledLive bool) {
	now := time.Now()
	axi := &quota.QuotaAxi{Config: quotaAxiConfig(epicDir)}
	if ws, err := findWorkspaceRoot(epicDir); err == nil {
		auto, calledLive, _ = quota.ReadCached(context.Background(), ws, axi, now)
	} else {
		auto, _ = axi.Read(context.Background())
		calledLive = true
	}
	man, _ := (&quota.Manual{EpicDir: epicDir}).Read(context.Background())
	return quota.Merge(auto, man), auto, calledLive
}

// quotaDispatchGate reads the quota for the chosen (harness, model) and enforces the dispatch gate: exhausted_now
// refuses (exit 1, unless force), a known percent below low_percent warns, and everything else passes. It returns
// (exitCode, blocked): blocked=true means the caller must return exitCode without dispatching. It never changes the
// harness (ADR 0011, observe-only).
func quotaDispatchGate(epicDir, harnessName, model string, force bool) (int, bool) {
	q := quota.Pick(mergedQuotaReadings(epicDir), harnessName, model)
	block, warn := quotaGateDecision(q, quotaSettingsFor(epicDir).LowPercent, force)
	switch {
	case block:
		return fail("refusing to dispatch: %s (pass --force-quota to override)", quotaReadingLine(q)), true
	case q.Runway == quota.RunwayExhaustedNow && force:
		fmt.Fprintf(os.Stderr, "cox: warning: %s is exhausted_now; dispatching anyway (--force-quota)\n", quotaReadingLine(q))
	case warn:
		fmt.Fprintf(os.Stderr, "cox: warning: %s is below low_percent; dispatching\n", quotaReadingLine(q))
	}
	return 0, false
}

// quotaGateDecision is the pure dispatch-gate rule: exhausted_now blocks unless force; a known percent below low warns.
// It never blocks on a low-but-not-exhausted reading and never on unknown (dispatch always proceeds when quota is
// unknown).
func quotaGateDecision(q quota.Reading, lowPercent int, force bool) (block, warn bool) {
	if q.Runway == quota.RunwayExhaustedNow {
		return !force, false
	}
	if q.Known && q.PercentRemaining < lowPercent {
		return false, true
	}
	return false, false
}

// quotaAxiConfig derives the quota-axi adapter config from the epic's policy quota section: the binary override and the
// npx opt-in. When policy cannot be loaded the default is a PATH lookup with no npx.
func quotaAxiConfig(epicDir string) quota.QuotaAxiConfig {
	pol := loadPolicyQuiet(epicDir)
	if pol == nil {
		return quota.QuotaAxiConfig{}
	}
	cfg := quota.QuotaAxiConfig{Binary: pol.Quota.Binary}
	if n := pol.Quota.NPX; n != nil && n.Version != "" {
		cfg.NPX = &quota.NPXOptIn{Version: n.Version, Integrity: n.Integrity}
	}
	return cfg
}

func quotaPercent(r quota.Reading) string {
	if !r.Known {
		return "unknown"
	}
	return strconv.Itoa(r.PercentRemaining) + "%"
}

// quotaTail shows the resets time for a known reading and the reason for an unknown one.
func quotaTail(r, lastKnown quota.Reading) string {
	if r.Known {
		if r.ResetsAt != "" {
			return "resets " + r.ResetsAt
		}
		return ""
	}
	tail := r.Reason
	if lastKnown.Known {
		if tail != "" {
			tail += "; "
		}
		tail += fmt.Sprintf("last known %d%% at %s", lastKnown.PercentRemaining, lastKnown.ObservedAt)
	}
	return tail
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// parsePercent reads a percent from "15" or "15%", bounded to 0..100.
func parsePercent(s string) (int, error) {
	s = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(s), "%"))
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("percent must be a number (0-100), got %q", s)
	}
	if n < 0 || n > 100 {
		return 0, fmt.Errorf("percent must be 0-100, got %d", n)
	}
	return n, nil
}

// twoPositional peels up to two leading non-flag tokens (harness, percent) before the flags.
func twoPositional(args []string) (first, second string, rest []string) {
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		first = args[0]
		args = args[1:]
	}
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		second = args[0]
		args = args[1:]
	}
	return first, second, args
}
