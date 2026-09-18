package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/nphattai/coxswain/internal/adapter/harness/registry"
	"github.com/nphattai/coxswain/internal/doctor"
	"github.com/nphattai/coxswain/internal/quota"
	"github.com/nphattai/coxswain/internal/workspace"
)

// harnessCard is the doctor view of one harness capability card plus whether an adapter is implemented for it.
type harnessCard struct {
	Name       string   `json:"name"`
	Roles      []string `json:"roles"`
	Wake       string   `json:"wake"`
	Checkpoint string   `json:"checkpoint"`
	Doorbell   bool     `json:"doorbell"`
	Interrupt  bool     `json:"interrupt"`
	Telemetry  bool     `json:"telemetry"`
	Adapter    bool     `json:"adapter"`
}

// policyOption is one harness name declared in a policy's options, with whether an adapter exists for it.
type policyOption struct {
	Name    string `json:"name"`
	Adapter bool   `json:"adapter"`
}

// doctorOutput wraps the installation report with the harness card table (and, when --epic resolves a policy, the
// per-option adapter column), so `cox doctor --json` carries both.
type doctorOutput struct {
	doctor.Report
	Harnesses     []harnessCard  `json:"harnesses"`
	PolicyOptions []policyOption `json:"policy_options,omitempty"`
	Quota         quotaDoctor    `json:"quota"`
}

// quotaDoctor is the doctor view of the quota-axi adapter and any manual readings in effect: whether the binary is
// found, its version, whether the automatic source is Keychain-granted (derived from a live claude reading when an epic
// is given), and the active captain-declared readings.
type quotaDoctor struct {
	Found    bool     `json:"found"`
	Path     string   `json:"path,omitempty"`
	Version  string   `json:"version,omitempty"`
	Keychain string   `json:"keychain"` // ok | needs-grant | unknown
	Manual   []string `json:"manual_active,omitempty"`
}

// cmdDoctor implements `cox doctor [--epic <dir>] [--json]`. It prints every installation and each epic's control
// directory, the capability card of every implemented harness, and (with --epic) an adapter yes|no column for each
// harness declared in that epic's resolved policy options, then exits 1 if any installation issue was found (version
// divergence, or a live v1 .run beside a v2 .cox), 0 otherwise.
func cmdDoctor(args []string) int {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	asJSON := fs.Bool("json", false, "emit JSON")
	epicDir := fs.String("epic", "", "epic directory (optional; adds an adapter column for its policy harness options)")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	now := time.Now().UTC()
	rep := doctor.Run(doctor.DefaultRoots())
	out := doctorOutput{Report: rep, Harnesses: harnessCards(codexCoxHooksInstalled(".")), PolicyOptions: policyOptions(*epicDir), Quota: quotaReport(*epicDir)}
	var watcherIssues []string

	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(out); err != nil {
			fmt.Fprintln(os.Stderr, "cox doctor:", err)
			return 1
		}
	} else {
		if len(rep.Installations) == 0 {
			fmt.Println("no coxswain installations found")
		}
		for _, in := range rep.Installations {
			line := fmt.Sprintf("%s  [%s]  %s", in.Path, in.Type, in.Version)
			if in.KitPath != in.Path {
				line += "  (kit: " + in.KitPath + ")"
			}
			if len(in.Epics) == 0 {
				line += "  (dev checkout)"
			}
			fmt.Println(line)
			for _, ep := range in.Epics {
				ctrl := ".run"
				if ep.HasCox {
					ctrl = ".cox/events.jsonl"
				}
				if ep.HasRun && ep.HasCox {
					ctrl = ".run + .cox/events.jsonl"
				}
				fmt.Printf("    epic %s  (%s)\n", ep.Path, ctrl)
				wi := watcherInfo(ep.Path)
				fmt.Printf("      %s\n", watcherLine(wi, now))
				if iss := watcherIssue(ep.Path, wi); iss != "" {
					watcherIssues = append(watcherIssues, iss)
				}
				if drift := policyDriftFor(ep.Path); len(drift) > 0 {
					fmt.Printf("      policy drift vs template (missing keys): %s\n", strings.Join(drift, ", "))
				}
			}
		}
		fmt.Println("harness cards:")
		for _, c := range out.Harnesses {
			fmt.Printf("  %-8s roles=%-13s wake=%-4s checkpoint=%-6s doorbell=%-5t interrupt=%-5t telemetry=%-5t adapter=%s\n",
				c.Name, strings.Join(c.Roles, ","), c.Wake, c.Checkpoint, c.Doorbell, c.Interrupt, c.Telemetry, yesNo(c.Adapter))
		}
		if len(out.PolicyOptions) > 0 {
			fmt.Printf("policy harness options (%s):\n", *epicDir)
			for _, o := range out.PolicyOptions {
				fmt.Printf("  %-10s adapter=%s\n", o.Name, yesNo(o.Adapter))
			}
		}
		q := out.Quota
		if q.Found {
			fmt.Printf("quota-axi: found=yes path=%s version=%s keychain=%s\n", q.Path, orNone(q.Version), q.Keychain)
		} else {
			fmt.Println("quota-axi: found=no (install it or set policy quota.binary; npx is opt-in via policy quota.npx)")
		}
		for _, m := range q.Manual {
			fmt.Printf("  manual reading: %s\n", m)
		}
		for _, iss := range rep.Issues {
			fmt.Fprintln(os.Stderr, "ISSUE:", iss)
		}
		for _, iss := range watcherIssues {
			fmt.Fprintln(os.Stderr, "ISSUE:", iss)
		}
	}
	if len(rep.Issues) > 0 || len(watcherIssues) > 0 {
		return 1
	}
	return 0
}

// policyDriftFor lists the template policy keys the epic's workspace policy is missing (drift), or nil when the
// workspace root or its policy.json cannot be resolved. It compares <wsRoot>/cox/policy.json against the embedded
// template so a policy that predates newer keys (e.g. harness.worker.models) is surfaced (M14).
func policyDriftFor(epicDir string) []string {
	wsRoot, err := findWorkspaceRoot(epicDir)
	if err != nil {
		return nil
	}
	drift, err := workspace.PolicyDrift(filepath.Join(wsRoot, workspace.ControlDir, "policy.json"))
	if err != nil {
		return nil
	}
	return drift
}

// harnessCards renders the capability card of every implemented harness, tagged adapter=true (they are, by definition
// of the registry). The rows are the source doctor prints and the shape --json emits.
func harnessCards(codexHooksPresent bool) []harnessCard {
	var out []harnessCard
	for _, name := range registry.Names() {
		h, _ := registry.Adapter(name)
		c := h.Card()
		roles := make([]string, 0, len(c.Roles))
		for _, r := range c.Roles {
			roles = append(roles, string(r))
		}
		wake := string(c.Wake)
		// Codex's card default is pull (no hooks), but once the cox codex leader hooks are installed the Stop hook
		// delivers wakes on the push path, so reflect the effective mode.
		if c.Name == "codex" && codexHooksPresent {
			wake = "push"
		}
		out = append(out, harnessCard{
			Name: c.Name, Roles: roles, Wake: wake, Checkpoint: string(c.Checkpoint),
			Doorbell: c.Doorbell, Interrupt: c.Interrupt, Telemetry: c.Telemetry, Adapter: true,
		})
	}
	return out
}

// codexCoxHooksInstalled reports whether root/.codex/hooks.json carries the cox codex leader hooks (detected by the
// stop-rewake command). The workspace-level file is where `cox workspace hooks --harness codex` writes them, and the
// leader runs `cox doctor` from that root, so doctor checks the current directory.
func codexCoxHooksInstalled(root string) bool {
	b, err := os.ReadFile(filepath.Join(root, ".codex", "hooks.json"))
	if err != nil {
		return false
	}
	return bytes.Contains(b, []byte("cox hook stop-rewake"))
}

// policyOptions resolves the epic's policy (when epicDir is given and a policy loads) and returns each distinct harness
// name declared in its leader/worker options, tagged with whether an adapter is implemented. It returns nil when no
// epic is given or the policy cannot be resolved, so doctor still prints the card table without an epic.
func policyOptions(epicDir string) []policyOption {
	if epicDir == "" {
		return nil
	}
	pol := loadPolicyQuiet(epicDir)
	if pol == nil {
		return nil
	}
	return policyOptionsFrom(pol)
}

// policyOptionsFrom is the pure core of policyOptions: the distinct harness names across the policy's leader and worker
// options, sorted, each tagged with whether an adapter is implemented.
func policyOptionsFrom(pol *workspace.Policy) []policyOption {
	seen := map[string]bool{}
	var names []string
	for _, n := range append(append([]string{}, pol.Harness.Leader.Options...), pol.Harness.Worker.Options...) {
		if n != "" && !seen[n] {
			seen[n] = true
			names = append(names, n)
		}
	}
	sort.Strings(names)
	out := make([]policyOption, 0, len(names))
	for _, n := range names {
		_, ok := registry.Adapter(n)
		out = append(out, policyOption{Name: n, Adapter: ok})
	}
	return out
}

// quotaReport builds the quota doctor view: it locates the quota-axi binary (policy override then PATH), reads its
// version, and - when an epic is given - derives the Keychain status from a live claude reading and lists any active
// manual readings. It never fails: a missing binary is found=false, and a slow read leaves keychain unknown.
func quotaReport(epicDir string) quotaDoctor {
	var q quotaDoctor
	q.Keychain = "unknown"
	bin := ""
	if epicDir != "" {
		if b := quotaAxiConfig(epicDir).Binary; b != "" {
			bin = b
		}
	}
	if bin == "" {
		if p, err := exec.LookPath("quota-axi"); err == nil {
			bin = p
		}
	}
	if bin == "" {
		return q
	}
	q.Found = true
	q.Path = bin
	q.Version = quotaAxiVersion(bin)
	if epicDir == "" {
		return q
	}
	readings := mergedQuotaReadings(epicDir)
	q.Keychain = keychainStatus(readings)
	q.Manual = manualReadingLines(epicDir)
	return q
}

// quotaAxiVersion runs `<bin> --version` with a short timeout and returns the trimmed first line, or "" on any error.
func quotaAxiVersion(bin string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, "--version").Output()
	if err != nil {
		return ""
	}
	line := strings.TrimSpace(string(out))
	if i := strings.IndexByte(line, '\n'); i >= 0 {
		line = line[:i]
	}
	return line
}

// keychainStatus derives whether the automatic source is granted from the claude reading: known -> ok, an unknown whose
// reason mentions keychain/auth -> needs-grant, else unknown.
func keychainStatus(readings []quota.Reading) string {
	c := quota.Pick(readings, "claude", "")
	if c.Known {
		return "ok"
	}
	low := strings.ToLower(c.Reason)
	if strings.Contains(low, "keychain") || strings.Contains(low, "auth") {
		return "needs-grant"
	}
	return "unknown"
}

// manualReadingLines lists the active captain-declared readings for the doctor (source manual only).
func manualReadingLines(epicDir string) []string {
	man, _ := (&quota.Manual{EpicDir: epicDir}).Read(context.Background())
	var out []string
	for _, r := range man {
		out = append(out, quotaReadingLine(r))
	}
	return out
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}
