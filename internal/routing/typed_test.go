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

// The typed resolver is tested ONLY against an httptest server; typesafe.ai is never called from a test, and the API key
// is never printed, logged, or placed on argv (DESIGN wave-4 item 10b). The secret sentinel below must never appear in
// any TypedResult a test renders.
const testKey = "sk-secret-never-leak-1234567890"

// typedServer returns an httptest server that records the Authorization header it saw and replies with the given JSON.
func typedServer(t *testing.T, reply string, sawAuth *string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if sawAuth != nil {
			*sawAuth = r.Header.Get("Authorization")
		}
		// The body must never leak quota/catalogs/approvals/profiles: only the brief and the rule whens.
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), "spendPriority") || strings.Contains(string(body), "profiles") {
			t.Errorf("request body leaked mechanical data to the model: %s", body)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, reply)
	}))
}

func typedTestPolicy() *workspace.Policy {
	p := &workspace.Policy{}
	p.Harness.Worker = workspace.HarnessRole{Options: []string{"claude", "codex"}, Default: "claude"}
	p.Routing.Rules = []workspace.RoutingRule{
		{When: "backend API work", Profiles: []workspace.RoutingProfile{{Harness: "claude"}, {Harness: "codex"}}},
		{When: "risky migration", Approval: "captain", Profiles: []workspace.RoutingProfile{{Harness: "claude"}}},
	}
	return p
}

func typedReadings() []quota.Reading {
	return []quota.Reading{
		knownReading("claude", "", 80, sp(0.2), quota.RunwayThroughReset, quota.NoRunway),
		knownReading("codex", "", 60, sp(0.7), quota.RunwayThroughReset, quota.NoRunway),
	}
}

func resultHasKey(t *testing.T, res TypedResult) {
	t.Helper()
	b, _ := json.Marshal(res)
	if strings.Contains(string(b), testKey) {
		t.Fatalf("the API key leaked into the TypedResult: %s", b)
	}
}

// A confident rule match resolves to `clear` and ranks the profile array by spendPriority; the key is sent as a Bearer
// header and never surfaces in the result.
func TestResolveTypedClear(t *testing.T) {
	var auth string
	reply := `{"model":"jev-latest","answers":{"rule":{"choice":"rule_1","confidence":0.9,"probabilities":{"rule_1":0.9,"rule_2":0.05,"default":0.05}}},"usage":{"input_tokens":10,"output_tokens":2}}`
	srv := typedServer(t, reply, &auth)
	defer srv.Close()
	res := ResolveTyped(context.Background(), TypedConfig{BaseURL: srv.URL}, testKey, "proj", "the brief text",
		typedTestPolicy(), testCards(), typedReadings(), Story{Effort: "low"})
	if res.Status != TypedClear {
		t.Fatalf("status = %q, want clear (reason %q)", res.Status, res.Reason)
	}
	if res.Choice == nil || res.Choice.Harness != "codex" {
		t.Fatalf("clear must pick codex (spendPriority 0.7 > 0.2): %+v", res.Choice)
	}
	if auth != "Bearer "+testKey {
		t.Fatalf("the key must be sent as a Bearer header, saw %q", auth)
	}
	if !res.HasUsage || res.InputTokens != 10 {
		t.Fatalf("usage not carried: %+v", res)
	}
	resultHasKey(t, res)
}

// A confidence below the floor is `ambiguous`; the gates are not run.
func TestResolveTypedAmbiguous(t *testing.T) {
	reply := `{"answers":{"rule":{"choice":"rule_1","confidence":0.4,"probabilities":{"rule_1":0.4,"rule_2":0.3,"default":0.3}}}}`
	srv := typedServer(t, reply, nil)
	defer srv.Close()
	res := ResolveTyped(context.Background(), TypedConfig{BaseURL: srv.URL}, testKey, "proj", "brief",
		typedTestPolicy(), testCards(), typedReadings(), Story{Effort: "low"})
	if res.Status != TypedAmbiguous || !strings.Contains(res.Reason, "below floor") {
		t.Fatalf("low confidence must be ambiguous: %+v", res)
	}
	if res.Choice != nil {
		t.Fatalf("ambiguous must not run the gates: %+v", res.Choice)
	}
}

// An approval-gated matched rule escalates.
func TestResolveTypedApprovalEscalates(t *testing.T) {
	reply := `{"answers":{"rule":{"choice":"rule_2","confidence":0.95,"probabilities":{"rule_1":0.02,"rule_2":0.95,"default":0.03}}}}`
	srv := typedServer(t, reply, nil)
	defer srv.Close()
	res := ResolveTyped(context.Background(), TypedConfig{BaseURL: srv.URL}, testKey, "proj", "brief",
		typedTestPolicy(), testCards(), typedReadings(), Story{Effort: "low"})
	if res.Status != TypedEscalate || !strings.Contains(res.Reason, "captain") {
		t.Fatalf("an approval rule must escalate: %+v", res)
	}
}

// A non-200 response is `error`, never a match; the intake is never blocked (the caller still exits 0).
func TestResolveTypedHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, "boom")
	}))
	defer srv.Close()
	res := ResolveTyped(context.Background(), TypedConfig{BaseURL: srv.URL}, testKey, "proj", "brief",
		typedTestPolicy(), testCards(), typedReadings(), Story{Effort: "low"})
	if res.Status != TypedError || !strings.Contains(res.Reason, "http 500") {
		t.Fatalf("a 500 must be an error outcome: %+v", res)
	}
	resultHasKey(t, res)
}

// Probabilities that do not cover exactly the offered choices (or do not sum to ~1) are rejected as an error.
func TestResolveTypedProbabilitiesValidation(t *testing.T) {
	// Missing the "default" option key.
	reply := `{"answers":{"rule":{"choice":"rule_1","confidence":0.9,"probabilities":{"rule_1":0.6,"rule_2":0.4}}}}`
	srv := typedServer(t, reply, nil)
	defer srv.Close()
	res := ResolveTyped(context.Background(), TypedConfig{BaseURL: srv.URL}, testKey, "proj", "brief",
		typedTestPolicy(), testCards(), typedReadings(), Story{Effort: "low"})
	if res.Status != TypedError || !strings.Contains(res.Reason, "cover exactly") {
		t.Fatalf("probabilities not covering the choices must error: %+v", res)
	}

	// Sum far from 1.
	reply2 := `{"answers":{"rule":{"choice":"rule_1","confidence":0.9,"probabilities":{"rule_1":0.2,"rule_2":0.2,"default":0.2}}}}`
	srv2 := typedServer(t, reply2, nil)
	defer srv2.Close()
	res2 := ResolveTyped(context.Background(), TypedConfig{BaseURL: srv2.URL}, testKey, "proj", "brief",
		typedTestPolicy(), testCards(), typedReadings(), Story{Effort: "low"})
	if res2.Status != TypedError || !strings.Contains(res2.Reason, "sum to ~1") {
		t.Fatalf("probabilities not summing to ~1 must error: %+v", res2)
	}
}

// With no rules, the typed resolver escalates without any network call (the httptest server is never hit).
func TestResolveTypedNoRules(t *testing.T) {
	hit := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hit = true }))
	defer srv.Close()
	pol := &workspace.Policy{}
	res := ResolveTyped(context.Background(), TypedConfig{BaseURL: srv.URL}, testKey, "proj", "brief",
		pol, testCards(), typedReadings(), Story{Effort: "low"})
	if res.Status != TypedEscalate || res.Reason != "no rules to match" {
		t.Fatalf("no rules must escalate: %+v", res)
	}
	if hit {
		t.Fatal("no rules must make no network call")
	}
}
