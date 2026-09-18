package quota

import "testing"

// Merge: a fresh automatic reading always wins over a manual one for the same (harness, model); a manual reading is
// used only where the automatic source is unknown or absent.
func TestMergeAutomaticFreshWins(t *testing.T) {
	auto := []Reading{
		{Harness: "claude", Known: true, PercentRemaining: 39, Runway: RunwayProjected, Source: SourceQuotaAxi},
		{Harness: "codex", Known: false, Runway: RunwayUnknown, Source: SourceQuotaAxi, Reason: "auth denied"},
	}
	manual := []Reading{
		{Harness: "claude", Known: true, PercentRemaining: 15, Runway: RunwayUnknown, Source: SourceManual},
		{Harness: "codex", Known: true, PercentRemaining: 50, Runway: RunwayUnknown, Source: SourceManual},
	}
	got := Merge(auto, manual)

	byKey := map[string]Reading{}
	for _, r := range got {
		byKey[r.Harness] = r
	}
	if c := byKey["claude"]; c.Source != SourceQuotaAxi || c.PercentRemaining != 39 {
		t.Fatalf("claude: fresh automatic must win, got %+v", c)
	}
	if c := byKey["codex"]; c.Source != SourceManual || c.PercentRemaining != 50 {
		t.Fatalf("codex: manual must fill an unknown automatic, got %+v", c)
	}
}

// Merge: a manual-only harness (no automatic reading at all) is included.
func TestMergeManualOnlyKey(t *testing.T) {
	got := Merge(nil, []Reading{{Harness: "codex", Known: true, PercentRemaining: 20, Source: SourceManual}})
	if len(got) != 1 || got[0].Source != SourceManual {
		t.Fatalf("manual-only key dropped: %+v", got)
	}
}

// Pick: exact model beats family beats account-wide; a harness with no reading returns an unknown none-source reading.
func TestPick(t *testing.T) {
	readings := []Reading{
		{Harness: "claude", Model: "", Known: true, PercentRemaining: 39, Source: SourceQuotaAxi},
		{Harness: "claude", Model: "fable", Known: true, PercentRemaining: 35, Source: SourceQuotaAxi},
	}
	if r := Pick(readings, "claude", "claude-fable-5-1"); r.Model != "fable" || r.PercentRemaining != 35 {
		t.Fatalf("family match failed: %+v", r)
	}
	if r := Pick(readings, "claude", "claude-opus-4-8"); r.Model != "" || r.PercentRemaining != 39 {
		t.Fatalf("account-wide fallback failed: %+v", r)
	}
	miss := Pick(readings, "codex", "gpt-5.6-sol")
	if miss.Known || miss.Source != SourceNone || miss.Runway != RunwayUnknown {
		t.Fatalf("missing harness must be unknown/none: %+v", miss)
	}
}
