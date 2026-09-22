package workspace

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nphattai/coxswain/internal/adapter/harness"
)

// validBasePolicy loads the shipped template into a fresh temp workspace; it is a fully-justified, valid policy the
// routing tests then mutate one field at a time.
func validBasePolicy(t *testing.T) *Policy {
	t.Helper()
	root := t.TempDir()
	if _, err := Init(root, nil); err != nil {
		t.Fatal(err)
	}
	pol, err := LoadPolicy(root)
	if err != nil {
		t.Fatalf("template policy must validate: %v", err)
	}
	if err := pol.Validate(); err != nil {
		t.Fatalf("base policy invalid before mutation: %v", err)
	}
	return pol
}

// Base-behavior probe (item 10): on the base sha the Routing type has no Rules field, so these malformed rules would be
// ignored and Validate would return nil - this test then fails (expected an error naming the field, got none). Each case
// mutates one field of an otherwise valid policy and asserts Validate refuses it and names the field.
func TestMalformedRoutingRulesRefusedAtLoad(t *testing.T) {
	claude := RoutingProfile{Harness: "claude"}
	cases := []struct {
		name  string
		rules []RoutingRule
		def   []RoutingProfile
		eff   map[string]string
		want  string
	}{
		{"empty profiles", []RoutingRule{{When: "w"}}, nil, nil, "empty"},
		{"missing when", []RoutingRule{{Profiles: []RoutingProfile{claude}}}, nil, nil, "missing when"},
		{"bad approval", []RoutingRule{{When: "w", Approval: "maybe", Profiles: []RoutingProfile{claude}}}, nil, nil, "invalid approval"},
		{"missing harness", []RoutingRule{{When: "w", Profiles: []RoutingProfile{{Model: "m"}}}}, nil, nil, "missing harness"},
		{"bad effort class", []RoutingRule{{When: "w", Profiles: []RoutingProfile{{Harness: "claude", Effort: "turbo"}}}}, nil, nil, "unknown effort class"},
		{"bad floor class", []RoutingRule{{When: "w", Profiles: []RoutingProfile{{Harness: "claude", Floor: "turbo"}}}}, nil, nil, "unknown floor class"},
		{"bad provider", []RoutingRule{{When: "w", Profiles: []RoutingProfile{{Harness: "claude", Provider: "Bad_Provider"}}}}, nil, nil, "invalid provider"},
		{"duplicate profile", []RoutingRule{{When: "w", Profiles: []RoutingProfile{claude, claude}}}, nil, nil, "duplicate profile"},
		{"empty default_profiles", nil, []RoutingProfile{}, nil, "empty"},
		{"bad default profile", nil, []RoutingProfile{{Model: "m"}}, nil, "missing harness"},
		{"bad effort by kind", nil, nil, map[string]string{"ship": "turbo"}, "routing.effort"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			pol := validBasePolicy(t)
			pol.Routing.Rules = c.rules
			if c.def != nil {
				pol.Routing.DefaultProfiles = c.def
			}
			if c.eff != nil {
				pol.Routing.Effort = c.eff
			}
			err := pol.Validate()
			if err == nil {
				t.Fatalf("malformed routing must be refused (%s)", c.name)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("refusal must name the field %q, got: %v", c.want, err)
			}
		})
	}
}

// The card-fit half of routing validation (run at dispatch): an unknown harness has no card, and an effort a card does
// not accept is refused, each naming the field.
func TestValidateRoutingCards(t *testing.T) {
	cards := map[string]harness.Capability{
		"claude": {Name: "claude", Efforts: []string{"low", "medium", "high", "xhigh", "max"}},
		"codex":  {Name: "codex", Efforts: []string{"low", "medium", "high", "xhigh"}},
	}
	pol := validBasePolicy(t)
	pol.Routing.Rules = []RoutingRule{{When: "w", Profiles: []RoutingProfile{{Harness: "opencode"}}}}
	if err := pol.ValidateRoutingCards(cards); err == nil || !strings.Contains(err.Error(), "unknown harness") {
		t.Fatalf("unknown harness must be refused with a card check, got: %v", err)
	}
	pol.Routing.Rules = []RoutingRule{{When: "w", Profiles: []RoutingProfile{{Harness: "codex", Effort: "max"}}}}
	if err := pol.ValidateRoutingCards(cards); err == nil || !strings.Contains(err.Error(), "not supported") {
		t.Fatalf("effort a card does not support must be refused, got: %v", err)
	}
	// A well-formed profile passes.
	pol.Routing.Rules = []RoutingRule{{When: "w", Profiles: []RoutingProfile{{Harness: "codex", Effort: "high"}, {Harness: "claude", Effort: "max"}}}}
	if err := pol.ValidateRoutingCards(cards); err != nil {
		t.Fatalf("a well-formed profile array must pass card validation: %v", err)
	}
}

// A policy.json whose routing.rules is not an array is a load error (malformed JSON).
func TestRoutingRulesWrongTypeRefused(t *testing.T) {
	root := t.TempDir()
	if _, err := Init(root, nil); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, ControlDir, "policy.json")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// Inject a rules value of the wrong type into the routing section.
	bad := strings.Replace(string(b), `"default": "policy",`, `"default": "policy", "rules": "notanarray",`, 1)
	if bad == string(b) {
		t.Fatal("template did not contain the routing.default line to extend")
	}
	if err := os.WriteFile(path, []byte(bad), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadPolicy(root); err == nil {
		t.Fatal("a routing.rules of the wrong type must be refused at load")
	}
}

// Effort defaults by kind (item 10): scout xhigh, ship low, arena high; an unknown kind falls back to ship; a policy
// routing.effort override wins; a nil policy still yields the code default.
func TestEffortForKind(t *testing.T) {
	var nilPol *Policy
	for kind, want := range map[string]string{"scout": "xhigh", "ship": "low", "arena": "high", "": "low", "weird": "low"} {
		if got := nilPol.EffortForKind(kind); got != want {
			t.Errorf("nil policy EffortForKind(%q) = %q, want %q", kind, got, want)
		}
	}
	pol := &Policy{}
	pol.Routing.Effort = map[string]string{"ship": "high"}
	if got := pol.EffortForKind("ship"); got != "high" {
		t.Errorf("routing.effort override EffortForKind(ship) = %q, want high", got)
	}
	if got := pol.EffortForKind("scout"); got != "xhigh" {
		t.Errorf("unset kind should keep the code default, got %q", got)
	}
}

// The runway floor and tie epsilon fall back to the code defaults when unset (nil or zero).
func TestRoutingThresholdDefaults(t *testing.T) {
	var nilPol *Policy
	if nilPol.RoutingMinRunwaySeconds() != DefaultMinRunwaySeconds {
		t.Errorf("nil RoutingMinRunwaySeconds = %d, want %d", nilPol.RoutingMinRunwaySeconds(), DefaultMinRunwaySeconds)
	}
	if nilPol.RoutingTieEpsilon() != DefaultTieEpsilon {
		t.Errorf("nil RoutingTieEpsilon = %v, want %v", nilPol.RoutingTieEpsilon(), DefaultTieEpsilon)
	}
	pol := &Policy{}
	pol.Routing.MinRunwaySeconds = 7200
	pol.Routing.TieEpsilon = 0.05
	if pol.RoutingMinRunwaySeconds() != 7200 || pol.RoutingTieEpsilon() != 0.05 {
		t.Errorf("explicit thresholds not honored: %d %v", pol.RoutingMinRunwaySeconds(), pol.RoutingTieEpsilon())
	}
}

// Item 8: the default template resolves delivery.mode = direct-PR (today's behaviour) and merge.yolo = false (the captain
// merges). Base-behavior probe: on the base sha there is no delivery.mode or merge section, so DeliveryMode/MergeYolo do
// not exist and the template carries neither key.
func TestDeliveryModeAndMergeYoloTemplate(t *testing.T) {
	root := t.TempDir()
	if _, err := Init(root, nil); err != nil {
		t.Fatal(err)
	}
	pol, err := LoadPolicy(root)
	if err != nil {
		t.Fatalf("default policy must validate: %v", err)
	}
	if pol.Delivery.Mode != ModeDirectPR {
		t.Errorf("template delivery.mode = %q, want %q", pol.Delivery.Mode, ModeDirectPR)
	}
	if pol.DeliveryMode() != ModeDirectPR {
		t.Errorf("DeliveryMode() = %q, want %q", pol.DeliveryMode(), ModeDirectPR)
	}
	if pol.MergePosture.Yolo {
		t.Error("template merge.yolo must default false (the captain merges)")
	}
	if pol.MergeYolo() {
		t.Error("MergeYolo() must be false for the default template")
	}
}

// DeliveryMode falls back to direct-PR for a nil policy and an unset mode; MergeYolo is false for a nil policy.
func TestDeliveryModeDefaults(t *testing.T) {
	var nilPol *Policy
	if nilPol.DeliveryMode() != DefaultDeliveryMode {
		t.Errorf("nil policy DeliveryMode = %q, want %q", nilPol.DeliveryMode(), DefaultDeliveryMode)
	}
	if nilPol.MergeYolo() {
		t.Error("nil policy MergeYolo must be false")
	}
	if got := (&Policy{}).DeliveryMode(); got != DefaultDeliveryMode {
		t.Errorf("empty policy DeliveryMode = %q, want %q", got, DefaultDeliveryMode)
	}
}

// A merge.yolo: true policy loads and MergeYolo reports it (a project may opt into an unattended merge).
func TestMergeYoloOptIn(t *testing.T) {
	root := t.TempDir()
	if _, err := Init(root, nil); err != nil {
		t.Fatal(err)
	}
	projDir := filepath.Join(root, "yolo")
	if err := os.MkdirAll(filepath.Join(projDir, "cox"), 0o755); err != nil {
		t.Fatal(err)
	}
	override := `{"merge":{"yolo":true,"why":"trusted unattended merge","review_when":"never"}}`
	if err := os.WriteFile(filepath.Join(projDir, "cox", "policy.json"), []byte(override), 0o644); err != nil {
		t.Fatal(err)
	}
	merged, err := Resolve(root, projDir)
	if err != nil {
		t.Fatalf("resolve with merge override: %v", err)
	}
	if !merged.MergeYolo() {
		t.Error("merge.yolo override did not resolve to true")
	}
}

// Base-behavior probe (item 8): a policy with an unrecognised delivery.mode is refused at load. On the base sha the mode
// field does not exist, so the loader ignores it and returns no error - this test then fails (expected an error, got nil).
func TestMalformedDeliveryModeRefused(t *testing.T) {
	root := t.TempDir()
	if _, err := Init(root, nil); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, ControlDir, "policy.json")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// Corrupt the delivery mode to an unknown value; everything else stays valid.
	bad := strings.Replace(string(b), `"mode": "direct-PR"`, `"mode": "bogus"`, 1)
	if bad == string(b) {
		t.Fatal("template did not contain the delivery.mode line to corrupt")
	}
	if err := os.WriteFile(path, []byte(bad), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = LoadPolicy(root)
	if err == nil {
		t.Fatal("a policy with an invalid delivery.mode must be refused")
	}
	if !strings.Contains(err.Error(), "mode") {
		t.Errorf("refusal must name the bad mode, got: %v", err)
	}
}

// The merge section is justified: a merge override that drops its why is caught by the whole-policy validation.
func TestMergeSectionIsJustified(t *testing.T) {
	root := t.TempDir()
	if _, err := Init(root, nil); err != nil {
		t.Fatal(err)
	}
	projDir := filepath.Join(root, "nowhy")
	if err := os.MkdirAll(filepath.Join(projDir, "cox"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projDir, "cox", "policy.json"), []byte(`{"merge":{"yolo":true}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Resolve(root, projDir); err == nil || !strings.Contains(err.Error(), "merge") {
		t.Fatalf("merge override without why must be refused naming merge, got: %v", err)
	}
}
