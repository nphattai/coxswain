// Port tests for the typed router's task text and per-rule confidence floors: firstmate commit 795e4b5, translated case
// by case from tests/fm-dispatch-resolve.test.sh@a8572f6 (epic cox-refresh, story cox-refresh-routing-pin, delta group
// G). Names map as: fm-dispatch-resolve.sh -> routing.ResolveTyped + routing.TaskText, `use` -> profiles, the rules file
// -> policy routing.rules, `# Task` > `## Captain's intent` / `## Firstmate spec` -> the story H1 > `## Goal` / `## Scope`
// / `## Acceptance criteria`, the scout contract line -> frontmatter `kind: scout`. Numbers print with %.3g, so fm's
// jq literal `0.30` reads `0.3` here.
package routing

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nphattai/coxswain/internal/quota"
	"github.com/nphattai/coxswain/internal/workspace"
)

// fmRulesWhen are the four rule conditions of fm-dispatch-resolve.test.sh's BASE_RULES; rule_2 resolves to claude and
// rule_4 to codex so a test can tell which rule's profiles ran. rule_3 needs captain approval.
func fmFloorPolicy(floors map[int]float64) *workspace.Policy {
	p := &workspace.Policy{}
	p.Harness.Worker = workspace.HarnessRole{Options: []string{"claude", "codex"}, Default: "claude"}
	p.Routing.Rules = []workspace.RoutingRule{
		{When: "The task touches the billing service.", Profiles: []workspace.RoutingProfile{{Harness: "claude"}}},
		{When: "The task generates images.", Profiles: []workspace.RoutingProfile{{Harness: "claude"}}},
		{When: "The task changes production infrastructure.", Approval: "captain", Profiles: []workspace.RoutingProfile{{Harness: "claude"}}},
		{When: "A simple bug fix with a stated root cause.", Profiles: []workspace.RoutingProfile{{Harness: "codex"}}},
	}
	for i, f := range floors {
		p.Routing.Rules[i].MinConfidence = &f
	}
	return p
}

// fmFloorReply is write_floor_response: a choice, a confidence and one probability per option.
func fmFloorReply(choice string, conf, r1, r2, r3, r4, def float64) string {
	b, _ := json.Marshal(map[string]any{"model": "jev-1.13.0", "answers": map[string]any{"rule": map[string]any{
		"choice": choice, "confidence": conf,
		"probabilities": map[string]float64{"rule_1": r1, "rule_2": r2, "rule_3": r3, "rule_4": r4, "default": def},
	}}})
	return string(b)
}

// fmResolve runs ResolveTyped against an httptest server returning reply and hands back the result and the request body.
func fmResolve(t *testing.T, pol *workspace.Policy, brief, reply string) (TypedResult, string) {
	t.Helper()
	var body string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		body = string(b)
		_, _ = io.WriteString(w, reply)
	}))
	defer srv.Close()
	readings := []quota.Reading{
		knownReading("claude", "", 80, sp(0.2), quota.RunwayThroughReset, quota.NoRunway),
		knownReading("codex", "", 60, sp(0.7), quota.RunwayThroughReset, quota.NoRunway),
	}
	res := ResolveTyped(context.Background(), TypedConfig{BaseURL: srv.URL}, testKey, "pager", brief, pol, testCards(), readings, Story{Effort: "low"})
	return res, body
}

// sentBrief is `jq -r .state.task.brief` of the request body.
func sentBrief(t *testing.T, body string) string {
	t.Helper()
	var req struct {
		State struct {
			Task struct {
				Brief string `json:"brief"`
			} `json:"task"`
		} `json:"state"`
	}
	if err := json.Unmarshal([]byte(body), &req); err != nil {
		t.Fatalf("request body is not JSON: %v\n%s", err, body)
	}
	return req.State.Task.Brief
}

func TestFMDispatchResolveFloors(t *testing.T) {
	const pagerBrief = "Fix the off-by-one in the pager.\n"
	// FLOOR_RULES: rules[1].min_confidence = 0.9, rules[3].min_confidence = 0.1.
	floorRules := map[int]float64{1: 0.9, 3: 0.1}

	t.Run("FM/fm-dispatch-resolve/top_rule_below_own_floor_falls_to_runner_up", func(t *testing.T) {
		// fm: tests/fm-dispatch-resolve.test.sh:358@a8572f6
		res, body := fmResolve(t, fmFloorPolicy(floorRules), pagerBrief, fmFloorReply("rule_2", 0.76, 0.02, 0.76, 0.02, 0.18, 0.02))
		if res.Status != TypedClear {
			t.Fatalf("status = %q (%s), want clear", res.Status, res.Reason)
		}
		if res.Rule != "rule_2" || res.RuleWhen != "The task generates images." || res.Confidence != 0.76 {
			t.Errorf("the model's own pick must stay visible: rule=%q when=%q conf=%v", res.Rule, res.RuleWhen, res.Confidence)
		}
		want := "rule_4 (A simple bug fix with a stated root cause.) probability 0.18 clears its floor 0.1; rule_2 probability 0.76 is below its floor 0.9"
		if res.Fallback != want {
			t.Errorf("fallback = %q, want %q", res.Fallback, want)
		}
		if res.Choice == nil || res.Choice.Harness != "codex" {
			t.Errorf("the runner-up rule's profiles must resolve (codex): %+v", res.Choice)
		}
		if strings.Contains(body, "min_confidence") || strings.Contains(body, "0.9") {
			t.Errorf("the model must never see confidence floors: %s", body)
		}
	})

	t.Run("FM/fm-dispatch-resolve/no_runner_up_clearing_its_floor_is_ambiguous", func(t *testing.T) {
		// fm: tests/fm-dispatch-resolve.test.sh:367@a8572f6
		res, _ := fmResolve(t, fmFloorPolicy(floorRules), pagerBrief, fmFloorReply("rule_2", 0.76, 0.02, 0.76, 0.02, 0.08, 0.12))
		if res.Status != TypedAmbiguous || res.Reason != "rule_2 probability 0.76 below its floor 0.9; no other option clears its own floor" {
			t.Fatalf("got %q / %q", res.Status, res.Reason)
		}
		if res.Fallback != "" || res.Choice != nil {
			t.Errorf("no fallback and no profile when none is taken: %q %+v", res.Fallback, res.Choice)
		}
	})

	t.Run("FM/fm-dispatch-resolve/equal_runner_ups_never_break_by_option_order", func(t *testing.T) {
		// fm: tests/fm-dispatch-resolve.test.sh:376@a8572f6
		tie := map[int]float64{0: 0.1, 1: 0.9, 3: 0.1}
		res, _ := fmResolve(t, fmFloorPolicy(tie), pagerBrief, fmFloorReply("rule_2", 0.76, 0.12, 0.76, 0.0, 0.12, 0.0))
		if res.Status != TypedAmbiguous || res.Reason != "rule_2 probability 0.76 below its floor 0.9; runner-up tie" {
			t.Fatalf("got %q / %q", res.Status, res.Reason)
		}
	})

	t.Run("FM/fm-dispatch-resolve/declared_floor_below_global_lets_pick_resolve", func(t *testing.T) {
		// fm: tests/fm-dispatch-resolve.test.sh:383@a8572f6
		res, _ := fmResolve(t, fmFloorPolicy(floorRules), pagerBrief, fmFloorReply("rule_4", 0.45, 0.01, 0.01, 0.01, 0.45, 0.52))
		if res.Status != TypedClear || res.Choice == nil || res.Choice.Harness != "codex" {
			t.Fatalf("a declared floor below the global floor must let the pick resolve: %q %s %+v", res.Status, res.Reason, res.Choice)
		}
	})

	t.Run("FM/fm-dispatch-resolve/picked_rule_clears_declared_floor_on_its_probability", func(t *testing.T) {
		// fm: tests/fm-dispatch-resolve.test.sh:390@a8572f6
		res, _ := fmResolve(t, fmFloorPolicy(map[int]float64{1: 0.9, 3: 0.3}), pagerBrief, fmFloorReply("rule_4", 0.25, 0.25, 0.05, 0.05, 0.35, 0.30))
		if res.Status != TypedClear || res.Fallback != "" || res.Choice == nil || res.Choice.Harness != "codex" {
			t.Fatalf("probability 0.35 over floor 0.3 must resolve with no fallback: %q %q %+v", res.Status, res.Fallback, res.Choice)
		}
	})

	t.Run("FM/fm-dispatch-resolve/high_confidence_does_not_lift_pick_over_its_floor", func(t *testing.T) {
		// fm: tests/fm-dispatch-resolve.test.sh:397@a8572f6
		res, _ := fmResolve(t, fmFloorPolicy(map[int]float64{1: 0.9, 3: 0.3}), pagerBrief, fmFloorReply("rule_2", 0.95, 0.05, 0.55, 0.05, 0.30, 0.05))
		want := "rule_4 (A simple bug fix with a stated root cause.) probability 0.3 clears its floor 0.3; rule_2 probability 0.55 is below its floor 0.9"
		if res.Status != TypedClear || res.Fallback != want {
			t.Fatalf("got %q fallback %q, want clear %q", res.Status, res.Fallback, want)
		}
	})

	t.Run("FM/fm-dispatch-resolve/runner_up_below_its_own_floor_not_taken", func(t *testing.T) {
		// fm: tests/fm-dispatch-resolve.test.sh:403@a8572f6
		res, _ := fmResolve(t, fmFloorPolicy(map[int]float64{1: 0.9, 3: 0.3}), pagerBrief, fmFloorReply("rule_2", 0.55, 0.05, 0.55, 0.05, 0.25, 0.10))
		if res.Status != TypedAmbiguous || res.Reason != "rule_2 probability 0.55 below its floor 0.9; no other option clears its own floor" {
			t.Fatalf("got %q / %q", res.Status, res.Reason)
		}
	})

	t.Run("FM/fm-dispatch-resolve/no_declared_floors_keep_the_global_floor", func(t *testing.T) {
		// fm: tests/fm-dispatch-resolve.test.sh:410@a8572f6
		res, _ := fmResolve(t, fmFloorPolicy(nil), pagerBrief, fmFloorReply("rule_2", 0.55, 0.01, 0.55, 0.01, 0.42, 0.01))
		if res.Status != TypedAmbiguous || res.Reason != "confidence 0.55 below floor 0.6" || res.Fallback != "" {
			t.Fatalf("got %q / %q / fallback %q", res.Status, res.Reason, res.Fallback)
		}
	})
}

// scaffoldStory is SCAFFOLD_BRIEF in cox's story template shape: frontmatter, the H1, boilerplate before and after the
// task sections, and a fenced `## Setup` inside a task section.
const scaffoldStory = "---\nid: s\nkind: ship\ndelivery: default\nmode: direct-PR\n---\n\n# s\n\n## Read first\nBOILERPLATE-READ\n\n" +
	"## Goal\nAdd a flag to the pager.\n\n" +
	"## Scope\nTouch pager.sh only.\n```sh\n# Not a heading inside a fence\n## Setup\n```\n### Out of scope\nAnything else.\n\n" +
	"## Acceptance criteria\nThe flag works.\n\n" +
	"## Working rules\nBOILERPLATE-RULES never push to the default branch.\n"

func TestFMDispatchResolveTaskSections(t *testing.T) {
	send := func(t *testing.T, brief, kind string) string {
		t.Helper()
		_, body := fmResolve(t, fmFloorPolicy(nil), TaskText(brief, kind), fmFloorReply("rule_4", 0.9, 0.025, 0.025, 0.025, 0.9, 0.025))
		return sentBrief(t, body)
	}

	t.Run("FM/fm-dispatch-resolve/brief_without_task_headings_rides_whole", func(t *testing.T) {
		// fm: tests/fm-dispatch-resolve.test.sh:239@a8572f6
		brief := "---\nid: s\n---\nFix the off-by-one in the pager.\n"
		if got := send(t, brief, "ship"); got != brief {
			t.Fatalf("a brief without task headings must ride whole, sent %q", got)
		}
	})

	t.Run("FM/fm-dispatch-resolve/only_task_sections_reach_the_model", func(t *testing.T) {
		// fm: tests/fm-dispatch-resolve.test.sh:443@a8572f6
		sent := send(t, scaffoldStory, "ship")
		for _, want := range []string{
			"## Goal\nAdd a flag to the pager.",
			"## Scope\nTouch pager.sh only.",
			"# Not a heading inside a fence\n## Setup\n```\n### Out of scope\nAnything else.",
			"## Acceptance criteria\nThe flag works.",
		} {
			if !strings.Contains(sent, want) {
				t.Errorf("sent text lacks %q:\n%s", want, sent)
			}
		}
		for _, not := range []string{"BOILERPLATE", "# s\n", "Brief kind:", "id: s", "mode="} {
			if strings.Contains(sent, not) {
				t.Errorf("sent text must not carry %q:\n%s", not, sent)
			}
		}
	})

	t.Run("FM/fm-dispatch-resolve/one_recognized_section_is_enough", func(t *testing.T) {
		// fm: tests/fm-dispatch-resolve.test.sh:454@a8572f6
		brief := "# s\n## Scope\nSpec text.\n## Working rules\nRULES-TEXT\n"
		if got := send(t, brief, "ship"); strings.TrimRight(got, "\n") != "## Scope\nSpec text." {
			t.Fatalf("sent %q", got)
		}
	})

	t.Run("FM/fm-dispatch-resolve/heading_with_trailing_blanks_is_not_a_section", func(t *testing.T) {
		// fm: tests/fm-dispatch-resolve.test.sh:459@a8572f6
		brief := "# s\n## Scope   \nSpec text.\n## Working rules\nRULES-TEXT\n"
		if got := send(t, brief, "ship"); got != brief {
			t.Fatalf("a heading with trailing blanks is not a section; sent %q", got)
		}
	})

	t.Run("FM/fm-dispatch-resolve/section_outside_the_task_heading_is_not_a_task_section", func(t *testing.T) {
		// fm: tests/fm-dispatch-resolve.test.sh:464@a8572f6
		brief := "Preamble.\n## Scope\nSpec text.\n"
		if got := send(t, brief, "ship"); got != brief {
			t.Fatalf("a section with no enclosing H1 is not a task section; sent %q", got)
		}
	})

	t.Run("FM/fm-dispatch-resolve/ship_brief_sends_sections_without_kind_or_mode", func(t *testing.T) {
		// fm: tests/fm-dispatch-resolve.test.sh:471@a8572f6
		sent := send(t, scaffoldStory, "ship")
		if !strings.Contains(sent, "## Goal\nAdd a flag to the pager.") || strings.Contains(sent, "Brief kind:") || strings.Contains(sent, "mode=") || strings.Contains(sent, "direct-PR") {
			t.Fatalf("a ship story sends its sections, no kind line, no delivery mode:\n%s", sent)
		}
	})

	t.Run("FM/fm-dispatch-resolve/scout_brief_names_its_kind", func(t *testing.T) {
		// fm: tests/fm-dispatch-resolve.test.sh:479@a8572f6
		scout := strings.Replace(scaffoldStory, "kind: ship", "kind: scout", 1)
		sent := send(t, scout, "scout")
		if !strings.HasPrefix(sent, "Brief kind: scout (report only)\n\n## Goal") {
			t.Fatalf("a scout story leads with its kind line:\n%s", sent)
		}
		if strings.Contains(sent, "\nkind: scout") || strings.Contains(sent, "id: s") {
			t.Errorf("the scout contract (frontmatter) itself is not sent:\n%s", sent)
		}
	})

	t.Run("FM/fm-dispatch-resolve/brief_with_neither_heading_is_sent_whole", func(t *testing.T) {
		// fm: tests/fm-dispatch-resolve.test.sh:484@a8572f6
		brief := "# s\n\n## Read first\nx\n\n## Working rules\ny\n"
		if got := send(t, brief, "scout"); got != brief {
			t.Fatalf("a story with none of the task sections is sent whole; sent %q", got)
		}
	})
}
