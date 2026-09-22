package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const briefTestKey = "sk-cmd-secret-never-leak-42"

// captureStdErrOut runs f with both os.Stdout and os.Stderr redirected and returns (stdout, stderr).
func captureStdErrOut(t *testing.T, f func()) (string, string) {
	t.Helper()
	oldOut, oldErr := os.Stdout, os.Stderr
	ro, wo, _ := os.Pipe()
	re, we, _ := os.Pipe()
	os.Stdout, os.Stderr = wo, we
	defer func() { os.Stdout, os.Stderr = oldOut, oldErr }()
	f()
	_ = wo.Close()
	_ = we.Close()
	out, _ := io.ReadAll(ro)
	errb, _ := io.ReadAll(re)
	return string(out), string(errb)
}

// stubQuotaAxi is a fake quota-axi (schema 5): claude spendPriority 0.2, codex 0.7, both fresh/through_reset, so a rule
// array of [claude, codex] resolves to codex. It is hermetic - the test never reads this machine's live quota.
const stubQuotaAxiJSON = `{"generatedAt":"2026-09-22T00:00:00Z","schemaVersion":5,"providers":[` +
	`{"provider":"claude","windows":[],"state":{"status":"fresh","stale":false},"quotaSemantics":{"status":"known","effectiveAvailability":[` +
	`{"scope":"all_models","status":"known","effectivePercentRemaining":80,"boundedBy":["w"],"limitingWindowIds":["w"],"runway":{"status":"through_reset"},"selection":{"status":"known","spendPriority":0.2}}]}},` +
	`{"provider":"codex","windows":[],"state":{"status":"fresh","stale":false},"quotaSemantics":{"status":"known","effectiveAvailability":[` +
	`{"scope":"all_models","status":"known","effectivePercentRemaining":60,"boundedBy":["w"],"limitingWindowIds":["w"],"runway":{"status":"through_reset"},"selection":{"status":"known","spendPriority":0.7}}]}}` +
	`]}`

// typedWorkspace writes a minimal workspace whose policy carries one routing rule and a stub quota-axi binary, plus a
// brief file, and returns the epic dir and the brief path.
func typedWorkspace(t *testing.T) (epic, brief string) {
	t.Helper()
	ws := t.TempDir()
	mustWrite(t, filepath.Join(ws, "cox", "workspace.json"), `{"schema":"coxswain.workspace.v1"}`)
	stub := filepath.Join(ws, "quota-axi-stub")
	mustWrite(t, stub, "#!/bin/sh\ncat <<'JSON'\n"+stubQuotaAxiJSON+"\nJSON\n")
	if err := os.Chmod(stub, 0o755); err != nil {
		t.Fatal(err)
	}
	tpl, err := os.ReadFile(filepath.Join("..", "..", "templates", "policy.json"))
	if err != nil {
		t.Fatal(err)
	}
	withRule := strings.Replace(string(tpl), `"default": "policy",`,
		`"default": "policy", "rules": [{"when":"backend API work","profiles":[{"harness":"claude"},{"harness":"codex"}]}],`, 1)
	withRule = strings.Replace(withRule, `"binary": "",`, `"binary": "`+stub+`",`, 1)
	if withRule == string(tpl) {
		t.Fatal("template routing.default / quota.binary lines not found to inject")
	}
	mustWrite(t, filepath.Join(ws, "cox", "policy.json"), withRule)
	epic = filepath.Join(ws, "proj", "epics", "e1")
	brief = filepath.Join(epic, "stories", "s.md")
	mustWrite(t, brief, "---\nid: s\nharness: auto\nkind: ship\n---\nImplement the backend API.\n")
	return epic, brief
}

// With no TYPESAFE_API_KEY (environment or workspace .env), cox route --brief prints one off line to stderr, exits 0, and
// makes no network call (the server is never hit).
func TestRouteBriefOffNoRequest(t *testing.T) {
	t.Setenv("TYPESAFE_API_KEY", "")
	epic, brief := typedWorkspace(t)
	hit := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hit = true }))
	defer srv.Close()
	t.Setenv("COX_TYPESAFE_BASE_URL", srv.URL)

	var rc int
	out, errOut := captureStdErrOut(t, func() { rc = cmdRoute([]string{"--brief", brief, "--epic", epic}) })
	if rc != 0 {
		t.Fatalf("off must exit 0, got %d", rc)
	}
	if !strings.Contains(errOut, "typed resolution off (TYPESAFE_API_KEY absent)") {
		t.Fatalf("off must print the off line to stderr, got: %q", errOut)
	}
	if hit {
		t.Fatal("off must make no network call")
	}
	if strings.TrimSpace(out) != "" {
		t.Fatalf("off must print nothing to stdout, got: %q", out)
	}
}

// With a key present and a confident match, cox route --brief prints the TOON block and a ready profile line, and the key
// never appears in stdout or stderr.
func TestRouteBriefClearKeyNeverInOutput(t *testing.T) {
	t.Setenv("TYPESAFE_API_KEY", briefTestKey)
	epic, brief := typedWorkspace(t)
	reply := `{"model":"jev-latest","answers":{"rule":{"choice":"rule_1","confidence":0.92,"probabilities":{"rule_1":0.92,"default":0.08}}},"usage":{"input_tokens":5,"output_tokens":1}}`
	var sawAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, reply)
	}))
	defer srv.Close()
	t.Setenv("COX_TYPESAFE_BASE_URL", srv.URL)

	var rc int
	out, errOut := captureStdErrOut(t, func() { rc = cmdRoute([]string{"--brief", brief, "--epic", epic}) })
	if rc != 0 {
		t.Fatalf("a resolution must exit 0, got %d", rc)
	}
	if !strings.Contains(out, "status: clear") || !strings.Contains(out, "profile: --harness ") {
		t.Fatalf("clear must print a status and a profile line, got: %q", out)
	}
	if sawAuth != "Bearer "+briefTestKey {
		t.Fatalf("the key must reach the server as a Bearer header, saw %q", sawAuth)
	}
	if strings.Contains(out, briefTestKey) || strings.Contains(errOut, briefTestKey) {
		t.Fatalf("the API key must never appear in output; stdout=%q stderr=%q", out, errOut)
	}
}
