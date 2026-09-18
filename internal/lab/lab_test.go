package lab

import (
	"strings"
	"testing"

	"github.com/nphattai/coxswain/internal/scorecard"
)

func TestNewAndAssign(t *testing.T) {
	dir := t.TempDir()
	exp, err := New(dir, "speed", "arena.trigger", "cost_usd")
	if err != nil {
		t.Fatal(err)
	}
	if len(exp.Variants) != 2 || exp.Variants[0] != "on" || exp.Variants[1] != "off" {
		t.Fatalf("variants = %v, want [on off]", exp.Variants)
	}
	// A second new refuses (it would drop assignments).
	if _, err := New(dir, "speed", "x", "cost_usd"); err == nil {
		t.Fatal("New must refuse an existing experiment")
	}

	// Assign alternates on, off, on and is idempotent for a repeated story.
	exp, v1 := Assign(exp, "s1")
	exp, v2 := Assign(exp, "s2")
	exp, v3 := Assign(exp, "s3")
	if v1 != "on" || v2 != "off" || v3 != "on" {
		t.Fatalf("assign order = %s,%s,%s, want on,off,on", v1, v2, v3)
	}
	before := len(exp.Assignments)
	exp, vAgain := Assign(exp, "s1")
	if vAgain != "on" || len(exp.Assignments) != before {
		t.Fatalf("re-assign s1 = %s (n=%d), want on with no new row", vAgain, len(exp.Assignments))
	}
}

func ptrF(v float64) *float64 { return &v }

func TestBuildReportGroupsByVariant(t *testing.T) {
	exp := Experiment{Name: "speed", Rule: "arena.trigger", Metric: "cost_usd", Variants: []string{"on", "off"}}
	exp, _ = Assign(exp, "s1") // on
	exp, _ = Assign(exp, "s2") // off
	exp, _ = Assign(exp, "s3") // on
	exp, _ = Assign(exp, "s4") // off (no scorecard row -> skipped)

	card := scorecard.Card{Stories: []scorecard.Story{
		{ID: "s1", Attempts: []scorecard.Attempt{{Attempt: 1, CostUSD: ptrF(2)}}},
		{ID: "s2", Attempts: []scorecard.Attempt{{Attempt: 1, CostUSD: ptrF(4)}}},
		{ID: "s3", Attempts: []scorecard.Attempt{{Attempt: 1, CostUSD: ptrF(6)}}},
		// s4 has no row: its variant "off" gets no value from it.
	}}
	rep := BuildReport(exp, card, 1)
	stat := map[string]VariantStat{}
	for _, s := range rep.Stats {
		stat[s.Variant] = s
	}
	if stat["on"].N != 2 || stat["on"].Mean != 4 || stat["on"].Variance != 4 {
		t.Fatalf("on stat = %+v, want n=2 mean=4 variance=4", stat["on"])
	}
	if stat["off"].N != 1 || stat["off"].Mean != 4 || stat["off"].Variance != 0 {
		t.Fatalf("off stat = %+v, want n=1 mean=4 variance=0", stat["off"])
	}
	if rep.EpicCount != 1 || rep.Bar != RetirementBar {
		t.Fatalf("epic count/bar wrong: %+v", rep)
	}
}

// A metric that is unknown (nil pointer) for a story is skipped, so n never counts an unmeasured attempt as 0.
func TestBuildReportSkipsUnknownMetric(t *testing.T) {
	exp := Experiment{Name: "e", Metric: "cost_usd", Variants: []string{"on", "off"}}
	exp, _ = Assign(exp, "s1") // on, cost known
	exp, _ = Assign(exp, "s2") // off, cost unknown (nil)
	card := scorecard.Card{Stories: []scorecard.Story{
		{ID: "s1", Attempts: []scorecard.Attempt{{Attempt: 1, CostUSD: ptrF(3)}}},
		{ID: "s2", Attempts: []scorecard.Attempt{{Attempt: 1, CostUSD: nil}}},
	}}
	rep := BuildReport(exp, card, 1)
	for _, s := range rep.Stats {
		if s.Variant == "off" && s.N != 0 {
			t.Fatalf("off n = %d, want 0 (unknown metric skipped)", s.N)
		}
	}
}

func TestDraftRetirement(t *testing.T) {
	rep := Report{Name: "speed", Rule: "arena.trigger", Metric: "cost_usd", EpicCount: 1, Bar: 5,
		Stats: []VariantStat{{Variant: "on", N: 2, Mean: 4, Variance: 4}, {Variant: "off", N: 1, Mean: 4}}}
	md := DraftRetirement(rep)
	for _, want := range []string{"# Draft: retire lab experiment speed", "does NOT change `policy.json`", "bar for retirement: 5", "Below the bar"} {
		if !strings.Contains(md, want) {
			t.Errorf("draft missing %q", want)
		}
	}
}

func TestKnownMetric(t *testing.T) {
	for _, m := range []string{"cost_usd", "ci_wall_incl_queue_s", "steers"} {
		if !KnownMetric(m) {
			t.Errorf("%q should be known", m)
		}
	}
	if KnownMetric("bogus") {
		t.Error("bogus metric should be unknown")
	}
}
