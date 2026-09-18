package quota

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"
)

// quotaAxiSchema is the only quota-axi JSON schemaVersion this adapter parses. Any other version is drift and yields
// unknown readings (DESIGN: schema 5 only, unknown on any drift).
const quotaAxiSchema = 5

// defaultTimeout bounds one quota-axi invocation (DESIGN: 60s). quota-axi is a Node tool with cold-start latency.
const defaultTimeout = 60 * time.Second

// quotaAxiProviders are the providers quota-axi is asked for; each maps to the harness of the same name. cox only routes
// claude and codex, so it never pays for the other provider reads.
var quotaAxiProviders = []string{"claude", "codex"}

// NPXOptIn is the explicit, policy-declared opt-in to run quota-axi via npx when no binary is installed (policy
// quota.npx). It carries an exact version and an integrity value; without it, npx is never used (supply-chain risk on a
// periodic watcher is not worth a convenience default, DESIGN).
type NPXOptIn struct {
	Version   string `json:"version"`
	Integrity string `json:"integrity"`
}

// QuotaAxiConfig is the policy-derived adapter config (policy quota section). Binary overrides PATH lookup; NPX is the
// opt-in fallback; Timeout defaults to 60s.
type QuotaAxiConfig struct {
	Binary  string
	NPX     *NPXOptIn
	Timeout time.Duration
}

// QuotaAxi fills coxswain.quota.v1 from a pinned quota-axi binary. It never persists raw output: Read projects to
// Readings in memory, and only the projection is cached (see cache.go). It never returns an error for a missing or
// degraded source: a missing binary, an auth denial, schema drift, or a stale report all become unknown Readings with a
// reason, so a consumer keeps working (routing skips quota, dispatch still spawns).
type QuotaAxi struct {
	Config QuotaAxiConfig
	Now    func() time.Time
	// run overrides the process invocation in tests; nil => the real exec. It returns quota-axi's stdout bytes.
	run func(ctx context.Context) ([]byte, error)
}

func (q *QuotaAxi) now() time.Time {
	if q.Now != nil {
		return q.Now()
	}
	return time.Now()
}

func (q *QuotaAxi) timeout() time.Duration {
	if q.Config.Timeout > 0 {
		return q.Config.Timeout
	}
	return defaultTimeout
}

// Read runs quota-axi once and projects its schema-5 JSON to Readings. Any failure (binary missing, timeout, non-JSON,
// wrong schema, auth denial, stale) yields unknown Readings, never an error.
func (q *QuotaAxi) Read(ctx context.Context) ([]Reading, error) {
	now := q.now()
	out, err := q.invoke(ctx)
	if err != nil {
		return unknownAll(SourceQuotaAxi, err.Error(), now), nil
	}
	return parseQuotaAxi(out, now), nil
}

// invoke runs the resolved command with a clean environment, a timeout, and no shell, capturing only stdout (stderr is
// discarded so no diagnostic text can ever reach the projection cache).
func (q *QuotaAxi) invoke(ctx context.Context) ([]byte, error) {
	if q.run != nil {
		return q.run(ctx)
	}
	bin, args, err := q.command()
	if err != nil {
		return nil, err
	}
	tctx, cancel := context.WithTimeout(ctx, q.timeout())
	defer cancel()
	cmd := exec.CommandContext(tctx, bin, args...)
	cmd.Env = cleanEnv()
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("quota-axi run failed: %v", err)
	}
	return stdout.Bytes(), nil
}

// command resolves the executable and args: the policy binary override, else quota-axi on PATH, else the explicit npx
// opt-in, else an error (which Read turns into unknown readings). It never uses a shell.
func (q *QuotaAxi) command() (string, []string, error) {
	args := []string{"--provider", strings.Join(quotaAxiProviders, ","), "--json"}
	if q.Config.Binary != "" {
		return q.Config.Binary, args, nil
	}
	if bin, err := exec.LookPath("quota-axi"); err == nil {
		return bin, args, nil
	}
	if q.Config.NPX != nil && q.Config.NPX.Version != "" {
		npx, err := exec.LookPath("npx")
		if err != nil {
			return "", nil, fmt.Errorf("quota-axi not found and npx missing for the policy opt-in")
		}
		// ponytail: npm has no per-run SRI check, so quota.npx.integrity is recorded and surfaced by doctor but not
		// enforced here; the installed-binary path is the trustworthy default. Enforce integrity via a vendored install
		// if the npx path ever becomes the norm.
		return npx, append([]string{"-y", "quota-axi@" + q.Config.NPX.Version}, args...), nil
	}
	return "", nil, fmt.Errorf("quota-axi not found on PATH (install it or set policy quota.binary; npx is opt-in via policy quota.npx)")
}

// cleanEnv is the minimal environment quota-axi runs under: PATH (to resolve the Node shebang) and HOME (to find the
// codex auth file and the macOS Keychain context). No other inherited variable is passed, so nothing else leaks into the
// subprocess.
func cleanEnv() []string {
	env := []string{}
	if p := os.Getenv("PATH"); p != "" {
		env = append(env, "PATH="+p)
	}
	if h := os.Getenv("HOME"); h != "" {
		env = append(env, "HOME="+h)
	}
	return env
}

// --- parsing ---

type quotaAxiDoc struct {
	GeneratedAt   string             `json:"generatedAt"`
	SchemaVersion int                `json:"schemaVersion"`
	Providers     []quotaAxiProvider `json:"providers"`
}

type quotaAxiProvider struct {
	Provider       string            `json:"provider"`
	Windows        []quotaAxiWindow  `json:"windows"`
	State          quotaAxiState     `json:"state"`
	QuotaSemantics quotaAxiSemantics `json:"quotaSemantics"`
}

type quotaAxiWindow struct {
	ID       string `json:"id"`
	ResetsAt string `json:"resetsAt"`
}

type quotaAxiState struct {
	Status        string `json:"status"`
	Stale         bool   `json:"stale"`
	Error         string `json:"error"`
	Reason        string `json:"reason"`
	RemedyCommand string `json:"remedyCommand"`
}

type quotaAxiSemantics struct {
	Status                string          `json:"status"`
	EffectiveAvailability []quotaAxiScope `json:"effectiveAvailability"`
}

type quotaAxiScope struct {
	Scope                     string         `json:"scope"`
	Status                    string         `json:"status"`
	EffectivePercentRemaining int            `json:"effectivePercentRemaining"`
	BoundedBy                 []string       `json:"boundedBy"`
	LimitingWindowIDs         []string       `json:"limitingWindowIds"`
	Runway                    quotaAxiRunway `json:"runway"`
}

type quotaAxiRunway struct {
	Status              string `json:"status"`
	UsableRunwaySeconds *int64 `json:"usableRunwaySeconds"`
}

// parseQuotaAxi projects quota-axi JSON to Readings. Non-JSON, wrong schema, or a provider quota-axi did not return all
// become unknown Readings. It reads only the fields the contract needs, so identity fields the provider may carry are
// never copied.
func parseQuotaAxi(data []byte, now time.Time) []Reading {
	var doc quotaAxiDoc
	if err := json.Unmarshal(data, &doc); err != nil {
		return unknownAll(SourceQuotaAxi, "quota-axi output is not JSON", now)
	}
	if doc.SchemaVersion != quotaAxiSchema {
		return unknownAll(SourceQuotaAxi, fmt.Sprintf("quota-axi schema %d unsupported (need %d)", doc.SchemaVersion, quotaAxiSchema), now)
	}
	observedAt := doc.GeneratedAt
	if observedAt == "" {
		observedAt = now.UTC().Format(time.RFC3339)
	}
	seen := map[string]bool{}
	var out []Reading
	for _, p := range doc.Providers {
		if !isQuotaAxiProvider(p.Provider) {
			continue // a provider cox does not route (quota-axi can report others)
		}
		seen[p.Provider] = true
		out = append(out, providerReadings(p.Provider, p, observedAt)...)
	}
	for _, h := range quotaAxiProviders {
		if !seen[h] {
			out = append(out, unknownReading(h, "", SourceQuotaAxi, "quota-axi returned no data for "+h, observedAt))
		}
	}
	return out
}

// providerReadings projects one provider to Readings: a non-fresh or stale provider is one unknown reading carrying the
// error and remedy; a fresh provider yields one reading per understood scope (all_models and each model:* scope), with
// percent, runway, usable-runway-seconds, and the binding window's reset from that scope's effectiveAvailability entry.
func providerReadings(h string, p quotaAxiProvider, observedAt string) []Reading {
	if p.State.Status != "fresh" || p.State.Stale {
		return []Reading{unknownReading(h, "", SourceQuotaAxi, providerUnknownReason(p.State), observedAt)}
	}
	resets := map[string]string{}
	for _, w := range p.Windows {
		resets[w.ID] = w.ResetsAt
	}
	var out []Reading
	for _, ea := range p.QuotaSemantics.EffectiveAvailability {
		if ea.Scope != "all_models" && !strings.HasPrefix(ea.Scope, "model:") {
			continue // claude/codex expose only all_models and model:*; ignore any other scope shape
		}
		model := strings.TrimPrefix(ea.Scope, "model:")
		if ea.Scope == "all_models" {
			model = ""
		}
		if ea.Status != "known" {
			// quota-axi's own rule: a scope it reports unknown makes the whole reading unknown (never our inference).
			out = append(out, unknownReading(h, model, SourceQuotaAxi, "quota-axi reports scope "+ea.Scope+" unknown", observedAt))
			continue
		}
		r := Reading{
			Harness:             h,
			Model:               model,
			Known:               true,
			PercentRemaining:    ea.EffectivePercentRemaining,
			Runway:              orUnknownRunway(ea.Runway.Status),
			UsableRunwaySeconds: NoRunway,
			Source:              SourceQuotaAxi,
			ObservedAt:          observedAt,
			WindowIDs:           ea.BoundedBy,
		}
		if ea.Runway.UsableRunwaySeconds != nil {
			r.UsableRunwaySeconds = *ea.Runway.UsableRunwaySeconds
		}
		if len(ea.LimitingWindowIDs) > 0 {
			r.ResetsAt = resets[ea.LimitingWindowIDs[0]]
		}
		out = append(out, r)
	}
	if len(out) == 0 {
		out = append(out, unknownReading(h, "", SourceQuotaAxi, "quota-axi returned no effective availability for "+h, observedAt))
	}
	return out
}

// providerUnknownReason builds the reason for a non-fresh provider from its state, appending the remedy command so the
// captain sees how to fix it (e.g. keychain_prompt_required -> run quota-axi --allow-keychain-prompt).
func providerUnknownReason(s quotaAxiState) string {
	var parts []string
	switch {
	case s.Error != "":
		parts = append(parts, s.Error)
	case s.Reason != "":
		parts = append(parts, s.Reason)
	case s.Stale:
		parts = append(parts, "stale")
	default:
		parts = append(parts, "state "+s.Status)
	}
	if s.RemedyCommand != "" {
		parts = append(parts, "remedy: "+s.RemedyCommand)
	}
	return strings.Join(parts, "; ")
}

func orUnknownRunway(status string) string {
	if status == "" {
		return RunwayUnknown
	}
	return status
}

func isQuotaAxiProvider(name string) bool {
	for _, h := range quotaAxiProviders {
		if h == name {
			return true
		}
	}
	return false
}

func unknownReading(h, model, source, reason, observedAt string) Reading {
	return Reading{
		Harness:             h,
		Model:               model,
		Known:               false,
		Runway:              RunwayUnknown,
		UsableRunwaySeconds: NoRunway,
		Source:              source,
		ObservedAt:          observedAt,
		Reason:              reason,
	}
}

func unknownAll(source, reason string, now time.Time) []Reading {
	at := now.UTC().Format(time.RFC3339)
	out := make([]Reading, 0, len(quotaAxiProviders))
	for _, h := range quotaAxiProviders {
		out = append(out, unknownReading(h, "", source, reason, at))
	}
	return out
}
