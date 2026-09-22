package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/nphattai/coxswain/internal/adapter/backend"
	"github.com/nphattai/coxswain/internal/adapter/forge"
	"github.com/nphattai/coxswain/internal/adapter/forge/github"
	"github.com/nphattai/coxswain/internal/quota"
	"github.com/nphattai/coxswain/internal/state"
	"github.com/nphattai/coxswain/internal/verdict"
)

// cmdState implements `cox state [<story>] --epic <dir> [--json]`. It folds the epic's event log and emits a
// coxswain.fleet.v1 view. The CLI wires the git observation from the epic checkout and, when a backend is configured,
// the real liveness by probing each story's saved session; with no backend or no saved session liveness stays
// "unknown" (F08: never inferred gone). Forge stays "unknown" until forge wiring lands, and the resolver keeps the
// event-log state regardless.
func cmdState(args []string) int {
	// The story is an optional leading positional (`cox state <story> --epic <dir>`). stdlib flag stops parsing at the
	// first non-flag arg, so peel a leading non-flag token as the story filter before parsing the flags.
	storyFilter := ""
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		storyFilter = args[0]
		args = args[1:]
	}

	fs := flag.NewFlagSet("state", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	epicDir := fs.String("epic", "", "epic directory (holds .cox/events.jsonl)")
	asJSON := fs.Bool("json", false, "emit coxswain.fleet.v1 JSON")
	noForge := fs.Bool("no-forge", false, "skip the GitHub forge probe (pr/checks/merged stay unknown)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *epicDir == "" {
		fmt.Fprintln(os.Stderr, "cox state: --epic <dir> is required")
		return 2
	}

	now := time.Now().UTC()
	fleet, warnings, err := buildFleet(*epicDir, storyFilter, now, *noForge)
	for _, w := range warnings {
		fmt.Fprintln(os.Stderr, "cox state: warning:", w)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "cox state:", err)
		return 1
	}

	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(fleet); err != nil {
			fmt.Fprintln(os.Stderr, "cox state:", err)
			return 1
		}
		return 0
	}
	fmt.Printf("epic %s (%s)\n", fleet.Epic, fleet.GeneratedAt)
	wi := watcherInfo(*epicDir)
	fmt.Println("  " + watcherLine(wi, now))
	if iss := watcherIssue(*epicDir, wi); iss != "" {
		fmt.Fprintln(os.Stderr, "ISSUE:", iss)
	}
	for _, s := range fleet.Stories {
		fmt.Printf("  %-24s %-16s attempt %d  liveness=%v  composer=%v  forge=%s  quota=%s\n",
			s.ID, s.State, s.Attempt, s.Observations["liveness"].Value, s.Observations["composer"].Value, forgeSummary(s.Observations["forge"]), quotaObsSummary(s.Observations["quota"]))
	}
	return 0
}

// quotaObsSummary renders a story's quota observation for the human table: "-" when the story is not in flight (no
// observation), "unknown" when the reading is not machine-readable, else "<percent>% <runway>".
func quotaObsSummary(obs state.Observation) string {
	q, ok := obs.Value.(quota.Reading)
	if !ok {
		return "-"
	}
	if !q.Known {
		return "unknown"
	}
	return fmt.Sprintf("%d%% %s", q.PercentRemaining, q.Runway)
}

// buildFleet folds the epic's event log and resolves the coxswain.fleet.v1 view: liveness and composer from the
// backend (unknown without one, F08), git from the epic checkout, and forge pr/checks/merged unless noForge. It is the
// shared builder for `cox state` and `cox board`, so both read the same fleet. An optional storyFilter keeps only one
// story. It returns the load warnings so the caller can surface them.
func buildFleet(epicDir, storyFilter string, now time.Time, noForge bool) (state.Fleet, []string, error) {
	events, warnings, err := state.Load(epicDir)
	if err != nil {
		return state.Fleet{}, warnings, err
	}
	snap := state.Fold(events)
	gitObs := gitObservation(epicDir, now)
	b, _ := newBackend(epicDir)
	fo := &forgeObserver{now: now, cache: map[string]state.Observation{}, disabled: noForge, newForge: func(dir string) forge.Forge { return github.New(dir) }}

	var stories []state.StoryState
	var readings []quota.Reading
	readingsLoaded := false
	for _, s := range snap.SortedStories() {
		if storyFilter != "" && s.ID != storyFilter {
			continue
		}
		// A scout story has no PR (item 9, B-05): skip the forge so `gh pr view` never errors, and report its report file.
		var forgeObs state.Observation
		if storyKind(epicDir, s.ID) == "scout" {
			forgeObs = scoutForgeObs(epicDir, s.ID, now)
		} else {
			forgeObs = fo.observe(epicDir, s)
		}
		in := state.Inputs{
			Liveness: probeLiveness(b, epicDir, s.ID),
			ProbeAt:  now,
			Git:      gitObs,
			Forge:    &forgeObs,
			Composer: &state.Observation{Value: probeComposer(b, epicDir, s), Source: "backend", ObservedAt: now.Format(time.RFC3339)},
		}
		st := state.ResolveStory(s, in, now)
		// Quota is a leader-side observation, not a probe, so it is attached here rather than in the resolver: for a story
		// in flight, show the reading for the harness/model it actually runs on (M11, observe-only).
		if s.State == state.Working || s.State == state.InputRequired {
			if !readingsLoaded {
				readings = mergedQuotaReadings(epicDir)
				readingsLoaded = true
			}
			h, m := storyHarnessModel(epicDir, s)
			q := quota.Pick(readings, h, m)
			st.Observations["quota"] = state.Observation{Value: q, Source: "quota", ObservedAt: nonEmpty(q.ObservedAt, now.Format(time.RFC3339))}
		}
		stories = append(stories, st)
	}
	return state.NewFleet(snap.Epic, stories, now), warnings, nil
}

// currentHarness returns the harness a story is currently running on (folded state + frontmatter/route), defaulting to
// claude. It is the "from" of a reroute resume.
func currentHarness(epicDir, story string) string {
	if events, _, err := state.Load(epicDir); err == nil {
		if snap := state.Fold(events).Stories[story]; snap != nil {
			h, _ := storyHarnessModel(epicDir, snap)
			return h
		}
	}
	if h := readStoryMeta(epicDir, story).Harness; h != "" && h != "auto" {
		return h
	}
	return "claude"
}

// storyHarnessModel returns the harness and (aliased) model a story runs on: its frontmatter, or the routed harness/model
// recorded on the dispatch event when the frontmatter is `auto`, defaulting to claude when neither resolves.
func storyHarnessModel(epicDir string, s *state.StorySnap) (string, string) {
	meta := readStoryMeta(epicDir, s.ID)
	h, m := meta.Harness, meta.Model
	if h == "" || h == "auto" {
		if route, ok := s.LastEvent.Evidence["route"].(map[string]any); ok {
			if rh, ok := route["harness"].(string); ok && rh != "" {
				h = rh
			}
			if rm, ok := route["model"].(string); ok && rm != "" && m == "" {
				m = rm
			}
		}
	}
	if h == "" || h == "auto" {
		h = "claude"
	}
	return h, modelAlias(m)
}

// scoutVal is the forge-column value for a scout story: it carries no PR, only whether the report was written (item 9).
type scoutVal struct {
	Kind   string `json:"kind"`
	Report string `json:"report"`
}

// scoutForgeObs builds a scout story's forge observation without touching the forge: kind=scout plus the report path or
// "missing" (B-05: a scout never triggers a `gh pr view` error).
func scoutForgeObs(epicDir, story string, now time.Time) state.Observation {
	report := filepath.Join(epicDir, "reports", story+".md")
	status := "missing"
	if _, err := os.Stat(report); err == nil {
		status = report
	}
	return state.Observation{Value: scoutVal{Kind: "scout", Report: status}, Source: "forge", ObservedAt: now.Format(time.RFC3339)}
}

// forgeSummary renders a forge observation for the human table: "PR #<n> checks=<v> merged=<m>" when a PR was found,
// "kind=scout report=<path|missing>" for a scout, else a short classified reason. The full error stays in the JSON
// (obs.Error); this only shortens the human line so a story with no PR reads "no-pr" instead of the whole gh error (M8 A0).
func forgeSummary(obs state.Observation) string {
	if v, ok := obs.Value.(scoutVal); ok {
		return fmt.Sprintf("kind=scout report=%s", v.Report)
	}
	if v, ok := obs.Value.(forgeVal); ok && v.PR != nil {
		return fmt.Sprintf("PR #%d checks=%s merged=%s", *v.PR, v.Checks, v.Merged)
	}
	if obs.Error != "" {
		return classifyForgeError(obs.Error)
	}
	return "unknown"
}

// classifyForgeError shortens a forge probe error for the human table: a head with no PR -> "no-pr"; gh missing or not
// logged in -> "unknown: gh unavailable"; anything else -> "unknown: <first line>". The full error is kept in the JSON.
func classifyForgeError(errStr string) string {
	low := strings.ToLower(errStr)
	switch {
	case strings.Contains(low, "no pull requests found") || strings.Contains(low, "no pr for head"):
		return "no-pr"
	case strings.Contains(low, "executable file not found") ||
		strings.Contains(low, "not logged in") || strings.Contains(low, "no github hosts") ||
		strings.Contains(low, "gh auth login") || strings.Contains(low, "authentication"):
		return "unknown: gh unavailable"
	default:
		line := errStr
		if i := strings.IndexByte(line, '\n'); i >= 0 {
			line = line[:i]
		}
		return "unknown: " + strings.TrimSpace(line)
	}
}

// probeComposer returns the worker terminal's composer state ("empty"|"pending"|"busy"|"unknown") for a working or
// input_required story via Backend.Composer, so the leader sees at a glance whether the worker is idle. It stays
// "unknown" (never inferred idle) when the story is not working, there is no backend, no saved session, or the probe
// fails - mirroring probeLiveness's F08 caution.
func probeComposer(b backend.Backend, epicDir string, s *state.StorySnap) string {
	if b == nil || (s.State != state.Working && s.State != state.InputRequired) {
		return backend.ComposerUnknown
	}
	sess, err := loadSession(epicDir, s.ID)
	if err != nil {
		return backend.ComposerUnknown
	}
	cs, err := b.Composer(sess)
	if err != nil || cs == "" {
		return backend.ComposerUnknown
	}
	return cs
}

// probeLiveness returns the real liveness of a story's saved session (.cox/sessions/<story>.json) via Backend.Probe.
// With no backend, no saved session, or a probe error it returns Unknown - never inferred gone (F08), which the
// resolver treats as "keep the event-log state".
func probeLiveness(b backend.Backend, epicDir, story string) backend.Liveness {
	if b == nil {
		return backend.Unknown
	}
	sess, err := loadSession(epicDir, story)
	if err != nil {
		return backend.Unknown
	}
	live, err := b.Probe(sess)
	if err != nil {
		return backend.Unknown
	}
	return live
}

// gitObservation reports HEAD, dirty, and ahead/behind of the epic checkout's upstream, tagged source=git. If the
// dir is not a git repo the value is "unknown".
func gitObservation(dir string, now time.Time) *state.Observation {
	obs := &state.Observation{Source: "git", ObservedAt: now.Format(time.RFC3339)}
	head, err := gitOut(dir, "rev-parse", "--short", "HEAD")
	if err != nil {
		obs.Value = "unknown"
		return obs
	}
	val := map[string]any{"head": head}
	if status, err := gitOut(dir, "status", "--porcelain"); err == nil {
		val["dirty"] = status != ""
	}
	if ab, err := gitOut(dir, "rev-list", "--left-right", "--count", "@{u}...HEAD"); err == nil {
		if parts := strings.Fields(ab); len(parts) == 2 {
			if behind, err := strconv.Atoi(parts[0]); err == nil {
				val["behind"] = behind
			}
			if ahead, err := strconv.Atoi(parts[1]); err == nil {
				val["ahead"] = ahead
			}
		}
	}
	obs.Value = val
	return obs
}

// forgeVal is the value of a story's forge observation: the PR number (nil when there is no PR), the CI verdict
// (pass|fail|unknown) with a run count, and merged as a three-state string. It is emitted inside a coxswain.fleet.v1
// observation whose source is "forge" and observed_at is the probe time.
type forgeVal struct {
	PR          *int   `json:"pr"`
	Checks      string `json:"checks"`
	ChecksCount int    `json:"checks_count"`
	Merged      string `json:"merged"`
}

// forgeObserver resolves each story's forge observation (pr, checks, merged) through a forge, caching per head within
// one cox state run so gh is not called twice for the same branch. A disabled observer (--no-forge) and any retrieval
// error both resolve to the three-state "unknown" (never a guessed pass), and cox state still exits 0 (F12, P5).
type forgeObserver struct {
	now      time.Time
	cache    map[string]state.Observation
	disabled bool
	newForge func(dir string) forge.Forge
}

// observe returns the forge observation for one story. The head is the recorded PR (evidence.pr) when present, else the
// story branch story/<id>; the forge runs in the story worktree (or the epic dir when the worktree is not recorded), so
// gh reads the right repo.
func (o *forgeObserver) observe(epicDir string, s *state.StorySnap) state.Observation {
	at := o.now.Format(time.RFC3339)
	if o.disabled {
		return state.Observation{Value: "unknown", Source: "forge", ObservedAt: at, Error: "forge disabled (--no-forge)"}
	}
	dir := readWorktree(epicDir, s.ID)
	if dir == "" {
		dir = epicDir
	}
	head := forgeHead(s)
	key := dir + "|" + head
	if obs, ok := o.cache[key]; ok {
		return obs
	}
	obs := o.probe(dir, head, at)
	o.cache[key] = obs
	return obs
}

// probe runs PR -> Checks -> Merged and builds the observation. Any error (gh missing, no PR, rate limit) yields the
// three-state unknown with the reason, never a fabricated pass.
func (o *forgeObserver) probe(dir, head, at string) state.Observation {
	f := o.newForge(dir)
	pr, err := f.PR(head)
	if err != nil {
		return state.Observation{Value: "unknown", Source: "forge", ObservedAt: at, Error: err.Error()}
	}
	val := forgeVal{Checks: "unknown", Merged: "unknown"}
	num := pr.Number
	val.PR = &num
	if checks, err := f.Checks(pr); err == nil {
		val.Checks = string(verdict.CI(checks))
		val.ChecksCount = len(checks)
	}
	if merged, err := f.Merged(pr); err == nil {
		val.Merged = strconv.FormatBool(merged)
	}
	return state.Observation{Value: val, Source: "forge", ObservedAt: at}
}

// forgeHead returns the forge selector for a story: the recorded PR number (evidence.pr) when one exists, else the
// story branch story/<id>. evidence.pr may be a string or a JSON number, so both are accepted.
func forgeHead(s *state.StorySnap) string {
	switch pr := s.LastEvent.Evidence["pr"].(type) {
	case string:
		if pr != "" {
			return pr
		}
	case float64:
		return strconv.Itoa(int(pr))
	}
	return "story/" + s.ID
}

func gitOut(dir string, args ...string) (string, error) {
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}
