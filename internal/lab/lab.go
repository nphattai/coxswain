// Package lab defines and reports self-improvement experiments that turn a policy rule on or off and compare a
// scorecard metric across the two variants. It only computes and proposes (ADR 0011): it never edits policy.json and
// never retires a rule on its own - a retirement is a draft the captain reads. Every report carries the sample size
// (n) and the count of v2 epics next to the numbers, so a one-epic experiment can never read as a rule.
package lab

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/nphattai/coxswain/internal/scorecard"
)

// Schema is the experiment file schema id.
const Schema = "coxswain.lab.v1"

// RetirementBar is the number of v2 epics with scorecards required before a lab report may be read without the
// epic-count caveat (ADR 0011).
const RetirementBar = 5

// Assignment records that a story ran under a variant.
type Assignment struct {
	Story   string `json:"story"`
	Variant string `json:"variant"`
	At      string `json:"at"`
}

// Experiment is one cox/lab/<name>.json file: which policy rule is toggled, which scorecard metric is compared, the
// two variants (on|off), and the story-to-variant assignments made so far.
type Experiment struct {
	Schema      string       `json:"schema"`
	Name        string       `json:"name"`
	Rule        string       `json:"rule"`
	Metric      string       `json:"metric"`
	Variants    []string     `json:"variants"`
	Assignments []Assignment `json:"assignments"`
	CreatedAt   string       `json:"created_at"`
}

// path returns <labDir>/<name>.json.
func path(labDir, name string) string { return filepath.Join(labDir, name+".json") }

// New creates an experiment file with the on|off variants and no assignments. It refuses to overwrite an existing
// experiment (a rerun of new would drop its assignments).
func New(labDir, name, rule, metric string) (Experiment, error) {
	if name == "" || rule == "" || metric == "" {
		return Experiment{}, fmt.Errorf("lab new needs a name, --rule, and --metric")
	}
	if _, err := os.Stat(path(labDir, name)); err == nil {
		return Experiment{}, fmt.Errorf("experiment %q already exists at %s", name, path(labDir, name))
	}
	exp := Experiment{
		Schema:    Schema,
		Name:      name,
		Rule:      rule,
		Metric:    metric,
		Variants:  []string{"on", "off"},
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
	}
	return exp, Save(labDir, exp)
}

// Load reads an experiment file.
func Load(labDir, name string) (Experiment, error) {
	b, err := os.ReadFile(path(labDir, name))
	if err != nil {
		return Experiment{}, fmt.Errorf("read experiment %q: %w", name, err)
	}
	var exp Experiment
	if err := json.Unmarshal(b, &exp); err != nil {
		return Experiment{}, fmt.Errorf("parse experiment %q: %w", name, err)
	}
	return exp, nil
}

// Save writes the experiment file (pretty-printed), creating the lab dir as needed.
func Save(labDir string, exp Experiment) error {
	if err := os.MkdirAll(labDir, 0o755); err != nil {
		return fmt.Errorf("create lab dir: %w", err)
	}
	b, err := json.MarshalIndent(exp, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path(labDir, exp.Name), append(b, '\n'), 0o644)
}

// Assign returns the variant for a story, alternating on|off by assignment order. A story already assigned keeps its
// variant (idempotent), so re-running assign never flips a story or double-counts it. The returned Experiment carries
// the new assignment; the caller Saves it.
func Assign(exp Experiment, story string) (Experiment, string) {
	for _, a := range exp.Assignments {
		if a.Story == story {
			return exp, a.Variant // idempotent
		}
	}
	variant := exp.Variants[len(exp.Assignments)%len(exp.Variants)]
	exp.Assignments = append(exp.Assignments, Assignment{Story: story, Variant: variant, At: time.Now().UTC().Format(time.RFC3339)})
	return exp, variant
}

// VariantStat is the per-variant aggregate of the experiment's metric.
type VariantStat struct {
	Variant  string    `json:"variant"`
	N        int       `json:"n"`
	Mean     float64   `json:"mean"`
	Variance float64   `json:"variance"`
	Values   []float64 `json:"values"`
}

// Report is the grouped result: the metric aggregated per variant, plus the v2 epic count against the retirement bar.
type Report struct {
	Name      string        `json:"name"`
	Rule      string        `json:"rule"`
	Metric    string        `json:"metric"`
	Stats     []VariantStat `json:"stats"`
	EpicCount int           `json:"epics_on_v2"`
	Bar       int           `json:"retirement_bar"`
}

// BuildReport groups the experiment's metric across its assigned stories by variant, using each story's latest attempt
// in the scorecard. A story with no scorecard row, or a metric that is unknown (nil) for it, is skipped for that
// variant (n reflects only measured values). epicCount is the number of v2 epics with scorecards, carried next to the
// numbers per ADR 0011.
func BuildReport(exp Experiment, card scorecard.Card, epicCount int) Report {
	latest := map[string]scorecard.Attempt{}
	for _, s := range card.Stories {
		if len(s.Attempts) > 0 {
			latest[s.ID] = s.Attempts[len(s.Attempts)-1]
		}
	}
	byVariant := map[string][]float64{}
	for _, a := range exp.Assignments {
		at, ok := latest[a.Story]
		if !ok {
			continue
		}
		if v, ok := metricValue(at, exp.Metric); ok {
			byVariant[a.Variant] = append(byVariant[a.Variant], v)
		}
	}
	rep := Report{Name: exp.Name, Rule: exp.Rule, Metric: exp.Metric, EpicCount: epicCount, Bar: RetirementBar}
	for _, variant := range exp.Variants {
		vals := byVariant[variant]
		mean, variance := meanVar(vals)
		rep.Stats = append(rep.Stats, VariantStat{Variant: variant, N: len(vals), Mean: mean, Variance: variance, Values: vals})
	}
	return rep
}

// metricValue returns a scorecard metric's value by its JSON name for one attempt, and ok=false when it is unknown
// (a nil pointer) or the name is not a known metric.
func metricValue(a scorecard.Attempt, metric string) (float64, bool) {
	i := func(p *int) (float64, bool) {
		if p == nil {
			return 0, false
		}
		return float64(*p), true
	}
	switch metric {
	case "wall_working_s":
		return i(a.WallWorkingS)
	case "wall_parked_s":
		return i(a.WallParkedS)
	case "steers":
		return i(a.Steers)
	case "questions":
		return i(a.Questions)
	case "resumes":
		return i(a.Resumes)
	case "tokens_in":
		return i(a.TokensIn)
	case "tokens_out":
		return i(a.TokensOut)
	case "ci_wall_incl_queue_s":
		return i(a.CIWallInclQueueS)
	case "cost_usd":
		if a.CostUSD == nil {
			return 0, false
		}
		return *a.CostUSD, true
	default:
		return 0, false
	}
}

// meanVar returns the mean and population variance of vals (0, 0 for an empty slice).
func meanVar(vals []float64) (float64, float64) {
	if len(vals) == 0 {
		return 0, 0
	}
	var sum float64
	for _, v := range vals {
		sum += v
	}
	mean := sum / float64(len(vals))
	var sq float64
	for _, v := range vals {
		d := v - mean
		sq += d * d
	}
	return mean, sq / float64(len(vals))
}

// KnownMetric reports whether metric is a recognised scorecard metric name (for `cox lab new` validation).
func KnownMetric(metric string) bool {
	_, ok := metricValue(scorecard.Attempt{
		WallWorkingS: new(int), WallParkedS: new(int), Steers: new(int), Questions: new(int),
		Resumes: new(int), TokensIn: new(int), TokensOut: new(int), CIWallInclQueueS: new(int), CostUSD: new(float64),
	}, metric)
	return ok
}

// DraftPath returns the retirement proposal path for an experiment under docs/decisions.
func DraftPath(decisionsDir, name string) string {
	return filepath.Join(decisionsDir, "draft-lab-"+name+".md")
}

// DraftRetirement renders the retirement proposal markdown from a report. It states the numbers, the sample caveat when
// below the bar, and that policy.json is unchanged (the captain applies or rejects the proposal by hand).
func DraftRetirement(rep Report) string {
	var b []byte
	add := func(format string, a ...any) { b = append(b, []byte(fmt.Sprintf(format, a...))...) }
	add("# Draft: retire lab experiment %s\n\n", rep.Name)
	add("- Status: Proposed (draft, not accepted)\n")
	add("- Rule under test: `%s`\n", rep.Rule)
	add("- Metric: `%s`\n\n", rep.Metric)
	add("## Numbers\n\n")
	add("| variant | n | mean | variance |\n|---|---|---|---|\n")
	for _, s := range rep.Stats {
		add("| %s | %d | %.3f | %.3f |\n", s.Variant, s.N, s.Mean, s.Variance)
	}
	add("\nEpics on v2 with scorecards: %d (bar for retirement: %d).\n\n", rep.EpicCount, rep.Bar)
	if rep.EpicCount < rep.Bar {
		add("**Below the bar.** With fewer than %d v2 epics this is a smoke, not evidence; every number above is read next to n and the epic count, never as a rule (ADR 0011).\n\n", rep.Bar)
	}
	add("## Proposal\n\n")
	add("This draft only reports. It does NOT change `policy.json`. The captain decides whether to apply, keep, or ")
	add("discard the `%s` rule after reading the numbers; any change is a separate, signed edit.\n", rep.Rule)
	return string(b)
}
