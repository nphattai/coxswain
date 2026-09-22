package routing

// Typed resolution is the opt-in path that lets typesafe.ai's System One model (Jev) make the rule MATCH from a story
// brief, in one short tool turn, while Go keeps every mechanical decision (DESIGN wave-4 item 10, ported from firstmate
// bin/fm-dispatch-resolve.sh). The model is shown only the brief and each rule's natural-language `when` as one Choice
// question; it never sees quota, catalogs, approvals, or profiles. In code we then apply the confidence floor, validate
// the probabilities, and run the matched rule's profile array through the SAME three gates and spendPriority ranking as
// the leader path (resolveProfiles). The key never reaches argv, a log, or stdout: the caller reads it, passes it here,
// and it is used only as a request header.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/nphattai/coxswain/internal/adapter/harness"
	"github.com/nphattai/coxswain/internal/quota"
	"github.com/nphattai/coxswain/internal/workspace"
)

const (
	// TypedConfidenceFloor is the minimum confidence a rule match needs before code applies it; below it the outcome is
	// ambiguous and the caller decides as it would with no typed path (DESIGN: floor 0.6).
	TypedConfidenceFloor = 0.6
	typedModel           = "jev-latest"
	typedBaseURL         = "https://api.typesafe.ai"
	typedTimeout         = 5 * time.Second
	// TypedNoneCriterion is the fixed extra option offered alongside every rule's `when`, so the model can say no rule fits.
	TypedNoneCriterion = "No listed rule applies to this task."
)

// Typed status values (the TOON block's `status:`).
const (
	TypedClear     = "clear"
	TypedAmbiguous = "ambiguous"
	TypedEscalate  = "escalate"
	TypedError     = "error"
)

// TypedConfig configures one typed resolution. Zero values fall back to the production endpoint, jev-latest, a 5s
// timeout, and http.DefaultClient; tests inject BaseURL (an httptest server) and Now.
type TypedConfig struct {
	BaseURL string
	Model   string
	Timeout time.Duration
	Client  *http.Client
	Now     func() time.Time
}

func (c TypedConfig) baseURL() string {
	if c.BaseURL != "" {
		return c.BaseURL
	}
	return typedBaseURL
}

func (c TypedConfig) model() string {
	if c.Model != "" {
		return c.Model
	}
	return typedModel
}

func (c TypedConfig) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func (c TypedConfig) client() *http.Client {
	if c.Client != nil {
		return c.Client
	}
	t := c.Timeout
	if t <= 0 {
		t = typedTimeout
	}
	return &http.Client{Timeout: t}
}

// TypedResult is one typed resolution's outcome, rendered as the TOON block by the caller. Choice carries the resolved
// candidates and the pick (or the escalate) when a rule matched; it is nil for an error/ambiguous outcome that never ran
// the gates.
type TypedResult struct {
	Status        string
	Model         string
	LatencyMS     int64
	InputTokens   int
	OutputTokens  int
	HasUsage      bool
	Confidence    float64
	Probabilities map[string]float64
	Rule          string // "rule_<n>" | "default"
	RuleWhen      string
	Reason        string
	Choice        *Choice
}

// typedRequest/typedResponse mirror the System One Choice contract (one `rule` question).
type typedRequest struct {
	Model     string            `json:"model"`
	State     map[string]any    `json:"state"`
	Questions map[string]typedQ `json:"questions"`
}

type typedQ struct {
	Type         string            `json:"type"`
	Instructions string            `json:"instructions"`
	Criteria     map[string]string `json:"criteria"`
}

type typedResponse struct {
	Model   string `json:"model"`
	Answers struct {
		Rule struct {
			Choice        string             `json:"choice"`
			Confidence    float64            `json:"confidence"`
			Probabilities map[string]float64 `json:"probabilities"`
		} `json:"rule"`
	} `json:"answers"`
	Usage *struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
}

// ResolveTyped runs the opt-in typed match for a brief and applies the mechanical gates in code. apiKey is used only as
// the Authorization header (never logged or returned). With no rules it escalates without a network call. Every outcome
// returns a TypedResult and a nil error; err is non-nil only for a usage/config fault the caller should exit 2 on (none
// arise here today, but the signature reserves it). Network, HTTP, and response faults are TypedError outcomes, not
// errors, so an intake is never blocked by the tool.
func ResolveTyped(ctx context.Context, cfg TypedConfig, apiKey, project, briefText string, pol *workspace.Policy, cards map[string]harness.Capability, readings []quota.Reading, story Story) TypedResult {
	rules := pol.Routing.Rules
	if len(rules) == 0 {
		return TypedResult{Status: TypedEscalate, Model: cfg.model(), Reason: "no rules to match"}
	}
	// Build the ONE Choice question: rule_1..rule_N -> each rule's `when`, plus the fixed default option. The model sees
	// only the brief and these conditions.
	criteria := map[string]string{"default": TypedNoneCriterion}
	choices := []string{"default"}
	for i, r := range rules {
		key := "rule_" + strconv.Itoa(i+1)
		criteria[key] = r.When
		choices = append(choices, key)
	}
	req := typedRequest{
		Model: cfg.model(),
		State: map[string]any{"task": map[string]any{"project": project, "brief": briefText}},
		Questions: map[string]typedQ{"rule": {
			Type:         "choice",
			Instructions: "Which ONE dispatch rule best fits `task` (read `task.brief` and `task.project`)? Each option is the rule's own matching condition; pick `default` when no rule's condition is met.",
			Criteria:     criteria,
		}},
	}
	body, err := json.Marshal(req)
	if err != nil {
		return typedErr(cfg, "encode request: "+err.Error())
	}

	t0 := cfg.now()
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.baseURL()+"/v1/systemone", bytes.NewReader(body))
	if err != nil {
		return typedErr(cfg, "build request: "+err.Error())
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+apiKey) // header only; never argv, log, or output
	resp, err := cfg.client().Do(httpReq)
	latency := cfg.now().Sub(t0).Milliseconds()
	if err != nil {
		return typedErrLat(cfg, latency, "request failed: "+netErr(err))
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return typedErrLat(cfg, latency, fmt.Sprintf("http %d: %s", resp.StatusCode, snippet(raw)))
	}
	var tr typedResponse
	if err := json.Unmarshal(raw, &tr); err != nil {
		return typedErrLat(cfg, latency, "response is not JSON")
	}
	if msg := validateTypedResponse(tr, choices); msg != "" {
		return typedErrLat(cfg, latency, msg)
	}

	res := TypedResult{
		Status: TypedClear, Model: firstNonEmpty(tr.Model, cfg.model()), LatencyMS: latency,
		Confidence: tr.Answers.Rule.Confidence, Probabilities: tr.Answers.Rule.Probabilities,
		Rule: tr.Answers.Rule.Choice,
	}
	if tr.Usage != nil {
		res.HasUsage = true
		res.InputTokens = tr.Usage.InputTokens
		res.OutputTokens = tr.Usage.OutputTokens
	}

	choice := tr.Answers.Rule.Choice
	res.RuleWhen = ruleWhenForChoice(rules, choice)
	if tr.Answers.Rule.Confidence < TypedConfidenceFloor {
		res.Status = TypedAmbiguous
		res.Reason = fmt.Sprintf("confidence %.3g below floor %.3g", tr.Answers.Rule.Confidence, TypedConfidenceFloor)
		return res
	}

	// Apply the matched rule (or the default array) through the same gates + ranking as the leader path.
	var ch Choice
	if idx, ok := ruleIndexForChoice(choice, len(rules)); ok {
		r := rules[idx]
		ch = resolveProfiles(r.Profiles, story, cards, readings, pol, choice, "jev", r.Approval, ruleWhenExcerpt(r))
	} else if len(pol.Routing.DefaultProfiles) > 0 {
		ch = resolveProfiles(pol.Routing.DefaultProfiles, story, cards, readings, pol, "default", "jev", "", "default_profiles")
	} else {
		res.Status = TypedEscalate
		res.Reason = "no rule matched and no default profiles configured"
		return res
	}
	ch.Confidence = floatPtr(tr.Answers.Rule.Confidence)
	res.Choice = &ch
	if ch.Escalate {
		res.Status = TypedEscalate
		res.Reason = ch.EscalateReason
	}
	return res
}

// validateTypedResponse mirrors the firstmate jq guard: choice a string, confidence a number in [0,1], probabilities a
// map whose keys are exactly the offered choices, each in [0,1] and summing to ~1. It returns "" when valid.
func validateTypedResponse(tr typedResponse, choices []string) string {
	a := tr.Answers.Rule
	if a.Choice == "" {
		return "response has no rule choice"
	}
	if a.Confidence < 0 || a.Confidence > 1 {
		return "response confidence out of range"
	}
	if a.Probabilities == nil {
		return "response has no probabilities"
	}
	keys := make([]string, 0, len(a.Probabilities))
	var sum float64
	for k, v := range a.Probabilities {
		keys = append(keys, k)
		if v < 0 || v > 1 {
			return "a probability is out of range"
		}
		sum += v
	}
	sort.Strings(keys)
	want := append([]string(nil), choices...)
	sort.Strings(want)
	if strings.Join(keys, ",") != strings.Join(want, ",") {
		return "probabilities do not cover exactly the offered choices"
	}
	if sum < 0.99 || sum > 1.01 {
		return "probabilities do not sum to ~1"
	}
	return ""
}

func ruleIndexForChoice(choice string, n int) (int, bool) {
	if !strings.HasPrefix(choice, "rule_") {
		return 0, false
	}
	num, err := strconv.Atoi(strings.TrimPrefix(choice, "rule_"))
	if err != nil || num < 1 || num > n {
		return 0, false
	}
	return num - 1, true
}

func ruleWhenForChoice(rules []workspace.RoutingRule, choice string) string {
	if idx, ok := ruleIndexForChoice(choice, len(rules)); ok {
		return ruleWhenExcerpt(rules[idx])
	}
	return TypedNoneCriterion
}

func typedErr(cfg TypedConfig, reason string) TypedResult {
	return TypedResult{Status: TypedError, Model: cfg.model(), Reason: reason}
}

func typedErrLat(cfg TypedConfig, latency int64, reason string) TypedResult {
	return TypedResult{Status: TypedError, Model: cfg.model(), LatencyMS: latency, Reason: reason}
}

func floatPtr(f float64) *float64 { return &f }

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func snippet(b []byte) string {
	s := strings.TrimSpace(string(b))
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) > 120 {
		return s[:120]
	}
	return s
}

// netErr renders a transport error without leaking the URL's query (defensive; the URL carries no secret today).
func netErr(err error) string {
	return strings.ReplaceAll(err.Error(), "\n", " ")
}
