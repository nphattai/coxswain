package workspace

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInitWritesDefaultsAndIsIdempotent(t *testing.T) {
	root := t.TempDir()
	created, err := Init(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(created) != 2 {
		t.Fatalf("first init should create 2 files, got %v", created)
	}
	// Both files must load and validate.
	if _, err := Load(root); err != nil {
		t.Fatalf("load workspace: %v", err)
	}
	if _, err := LoadPolicy(root); err != nil {
		t.Fatalf("load policy: %v", err)
	}
	// services/ dir exists.
	if fi, err := os.Stat(filepath.Join(root, "cox", "services")); err != nil || !fi.IsDir() {
		t.Fatalf("cox/services should exist: %v", err)
	}
	// Re-run: nothing new created.
	created, err = Init(root, nil)
	if err != nil || len(created) != 0 {
		t.Fatalf("second init should create nothing, got %v err=%v", created, err)
	}
}

func TestPolicyValidateNamesMissingSections(t *testing.T) {
	p := &Policy{} // every Meta empty
	err := p.Validate()
	if err == nil {
		t.Fatal("empty policy must fail validation")
	}
	for _, want := range []string{"workers_per_repo", "waves", "context", "arena", "delivery", "harness"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("validation error must name %q, got: %v", want, err)
		}
	}
}

func TestPolicyValidatePassesWithMeta(t *testing.T) {
	// The embedded default must be a valid policy.
	root := t.TempDir()
	if _, err := Init(root, nil); err != nil {
		t.Fatal(err)
	}
	p, err := LoadPolicy(root)
	if err != nil {
		t.Fatalf("default policy must validate: %v", err)
	}
	if p.Context.PlanCompact != 400000 || p.Context.CompactNow != 500000 {
		t.Errorf("default context thresholds wrong: %+v", p.Context)
	}
	if p.Delivery.Style != "default" || p.Harness.Leader.Default != "claude" {
		t.Errorf("default delivery/harness wrong: %+v %+v", p.Delivery, p.Harness)
	}
}

// The default policy template carries the quota section; a nil policy and an unset value fall back to the defaults.
func TestQuotaPolicyDefaultsAndTemplate(t *testing.T) {
	root := t.TempDir()
	if _, err := Init(root, nil); err != nil {
		t.Fatal(err)
	}
	pol, err := LoadPolicy(root)
	if err != nil {
		t.Fatalf("load policy: %v", err)
	}
	if pol.QuotaLowPercent() != 10 || pol.QuotaOKPercent() != 25 || pol.QuotaMinRunwayHours() != 24 || pol.QuotaPollMinutes() != 5 || pol.QuotaHealthDebounceMinutes() != 60 {
		t.Fatalf("template quota values: low=%d ok=%d min=%d poll=%d health_debounce=%d", pol.QuotaLowPercent(), pol.QuotaOKPercent(), pol.QuotaMinRunwayHours(), pol.QuotaPollMinutes(), pol.QuotaHealthDebounceMinutes())
	}
	if pol.Quota.NPX != nil {
		t.Fatalf("template npx should be null, got %+v", pol.Quota.NPX)
	}
	// A nil policy still yields the code defaults.
	var nilPol *Policy
	if nilPol.QuotaLowPercent() != DefaultQuotaLowPercent || nilPol.QuotaPollMinutes() != DefaultQuotaPollMinutes || nilPol.QuotaHealthDebounceMinutes() != DefaultQuotaHealthDebounce {
		t.Fatalf("nil policy must return defaults")
	}
}

// The default policy template carries the review section: lavish surface, share off, npx opt-in null (M13).
func TestReviewPolicyTemplate(t *testing.T) {
	root := t.TempDir()
	if _, err := Init(root, nil); err != nil {
		t.Fatal(err)
	}
	pol, err := LoadPolicy(root)
	if err != nil {
		t.Fatalf("load policy: %v", err)
	}
	if pol.Review.Surface != "lavish" || pol.Review.Binary != "lavish-axi" {
		t.Fatalf("template review surface/binary: %+v", pol.Review)
	}
	if pol.Review.Share {
		t.Fatalf("template review.share must default false (ht-ml.app is outward-facing)")
	}
	if pol.Review.NPX != nil {
		t.Fatalf("template review.npx should be null, got %+v", pol.Review.NPX)
	}
}

func TestOrcaPlaneDefaultAndOverride(t *testing.T) {
	// Absent backend section => code default (orchestration until M10's final flip).
	if got := (&Policy{}).OrcaPlane(); got != DefaultOrcaPlane {
		t.Errorf("empty policy OrcaPlane = %q, want default %q", got, DefaultOrcaPlane)
	}
	// A nil policy is the default too (a caller that could not load policy still gets a plane).
	if got := (*Policy)(nil).OrcaPlane(); got != DefaultOrcaPlane {
		t.Errorf("nil policy OrcaPlane = %q, want %q", got, DefaultOrcaPlane)
	}
	// The embedded default template must parse the backend section.
	root := t.TempDir()
	if _, err := Init(root, nil); err != nil {
		t.Fatal(err)
	}
	p, err := LoadPolicy(root)
	if err != nil {
		t.Fatal(err)
	}
	// M10 flipped the template (and code default) to terminal (ADR 0012 decision 4).
	if p.OrcaPlane() != PlaneTerminal {
		t.Errorf("template plane = %q, want terminal", p.OrcaPlane())
	}
	// A project override can pin the plane back and does not need a why (backend is not a justified section).
	projDir := filepath.Join(root, "orch")
	if err := os.MkdirAll(filepath.Join(projDir, "cox"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projDir, "cox", "policy.json"),
		[]byte(`{"backend":{"orca":{"plane":"orchestration"}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	merged, err := Resolve(root, projDir)
	if err != nil {
		t.Fatalf("resolve with backend override: %v", err)
	}
	if merged.OrcaPlane() != PlaneOrchestration {
		t.Errorf("override plane = %q, want orchestration", merged.OrcaPlane())
	}
}

// WorkerModel resolves per harness: an explicit model wins, else the per-harness models map, else (claude only) the
// legacy model key or the captain ruling; a harness with no default resolves to ("", false) so the launch omits --model.
func TestWorkerModelPerHarness(t *testing.T) {
	// Explicit model wins over any policy, for any harness.
	p := &Policy{}
	p.Harness.Worker.Models = map[string]string{"claude": "claude-opus-4-8", "codex": "gpt-5.6-sol", "pi": "openai-codex/gpt-5.6-sol"}
	if got, ok := p.WorkerModel("claude", "pinned-x"); !ok || got != "pinned-x" {
		t.Errorf("explicit model = %q,%v want pinned-x,true", got, ok)
	}
	// pi resolves its provider/model default from the map; an explicit model still wins (item 6, B-47).
	if got, ok := p.WorkerModel("pi", ""); !ok || got != "openai-codex/gpt-5.6-sol" {
		t.Errorf("pi default = %q,%v want openai-codex/gpt-5.6-sol,true", got, ok)
	}
	if got, ok := p.WorkerModel("pi", "anthropic/claude-opus-4-8"); !ok || got != "anthropic/claude-opus-4-8" {
		t.Errorf("pi explicit = %q,%v want anthropic/claude-opus-4-8,true", got, ok)
	}
	// No explicit model: the per-harness default from the map.
	if got, ok := p.WorkerModel("codex", ""); !ok || got != "gpt-5.6-sol" {
		t.Errorf("codex default = %q,%v want gpt-5.6-sol,true", got, ok)
	}
	if got, ok := p.WorkerModel("claude", ""); !ok || got != "claude-opus-4-8" {
		t.Errorf("claude default = %q,%v want claude-opus-4-8,true", got, ok)
	}
	// A harness with no map entry: no default, launch omits --model.
	if got, ok := p.WorkerModel("omp", ""); ok || got != "" {
		t.Errorf("omp default = %q,%v want \"\",false", got, ok)
	}
	// Legacy key: harness.worker.model is claude's default when the map has no claude entry.
	legacy := &Policy{}
	legacy.Harness.Worker.Model = "claude-legacy"
	if got, ok := legacy.WorkerModel("claude", ""); !ok || got != "claude-legacy" {
		t.Errorf("legacy claude default = %q,%v want claude-legacy,true", got, ok)
	}
	// The legacy key never leaks to a non-claude harness.
	if got, ok := legacy.WorkerModel("codex", ""); ok || got != "" {
		t.Errorf("legacy must not apply to codex = %q,%v want \"\",false", got, ok)
	}
	// No policy at all: claude gets the captain ruling, other harnesses get nothing.
	if got, ok := (&Policy{}).WorkerModel("claude", "  "); !ok || got != DefaultWorkerModel {
		t.Errorf("bare claude default = %q,%v want %q,true", got, ok, DefaultWorkerModel)
	}
	if got, ok := (*Policy)(nil).WorkerModel("claude", ""); !ok || got != DefaultWorkerModel {
		t.Errorf("nil policy claude = %q,%v want %q,true", got, ok, DefaultWorkerModel)
	}
	if got, ok := (*Policy)(nil).WorkerModel("codex", ""); ok || got != "" {
		t.Errorf("nil policy codex = %q,%v want \"\",false", got, ok)
	}
}

// LaunchFlags returns the policy entry when present (including an explicit empty list, a captain opt-out), else the
// built-in autonomy default so a dispatched worker never deadlocks on an approval prompt it cannot answer.
func TestLaunchFlagsPolicyThenDefault(t *testing.T) {
	// No policy entry: built-in defaults per harness.
	empty := &Policy{}
	if got := empty.LaunchFlags("claude"); strings.Join(got, " ") != "--permission-mode bypassPermissions" {
		t.Errorf("claude default = %v", got)
	}
	if got := empty.LaunchFlags("codex"); strings.Join(got, " ") != "-a never -s workspace-write" {
		t.Errorf("codex default = %v", got)
	}
	if got := empty.LaunchFlags("omp"); got != nil {
		t.Errorf("unknown harness default = %v, want nil", got)
	}
	if got := (*Policy)(nil).LaunchFlags("claude"); strings.Join(got, " ") != "--permission-mode bypassPermissions" {
		t.Errorf("nil policy claude = %v", got)
	}
	// An explicit policy entry wins, and an explicit empty list is a captain opt-out (types no flags).
	p := &Policy{Harness: Harness{Launch: Launch{Worker: map[string][]string{"claude": {"--permission-mode", "acceptEdits"}, "codex": {}}}}}
	if got := p.LaunchFlags("claude"); strings.Join(got, " ") != "--permission-mode acceptEdits" {
		t.Errorf("claude override = %v", got)
	}
	if got := p.LaunchFlags("codex"); len(got) != 0 {
		t.Errorf("codex opt-out = %v, want empty (not the default)", got)
	}
}

// ArenaLaunchFlags returns the nested harness.launch.arena.<name> entry when present, else the read-only default, so an
// arena role in terminal mode never inherits the worker's bypass flags.
func TestArenaLaunchFlags(t *testing.T) {
	empty := &Policy{}
	// Terminal-mode claude uses acceptEdits so a role can write its report; plan mode cannot write even with --add-dir and
	// is the headless path (ADR 0013, M12b).
	if got := empty.ArenaLaunchFlags("claude"); strings.Join(got, " ") != "--permission-mode acceptEdits" {
		t.Errorf("claude arena default = %v", got)
	}
	// Terminal-mode codex uses workspace-write so a role can write its report in its own worktree; read-only would let it
	// read but never write (ADR 0013, M12b). Headless is the read-only path.
	if got := empty.ArenaLaunchFlags("codex"); strings.Join(got, " ") != "-a never -s workspace-write" {
		t.Errorf("codex arena default = %v", got)
	}
	if got := (*Policy)(nil).ArenaLaunchFlags("codex"); strings.Join(got, " ") != "-a never -s workspace-write" {
		t.Errorf("nil policy codex arena = %v", got)
	}
	p := &Policy{Harness: Harness{Launch: Launch{Arena: map[string][]string{"claude": {"--permission-mode", "dontAsk"}}}}}
	if got := p.ArenaLaunchFlags("claude"); strings.Join(got, " ") != "--permission-mode dontAsk" {
		t.Errorf("claude arena override = %v", got)
	}
	// A harness with no arena override still gets the default.
	if got := p.ArenaLaunchFlags("codex"); strings.Join(got, " ") != "-a never -s workspace-write" {
		t.Errorf("codex arena fallback = %v", got)
	}
}

// LaunchConfirmS returns the policy window when set, else the 60s default (also for an absent section or nil policy).
func TestLaunchConfirmSDefaultAndOverride(t *testing.T) {
	if got := (&Policy{}).LaunchConfirmS(); got != DefaultLaunchConfirmS {
		t.Errorf("empty policy LaunchConfirmS = %d, want %d", got, DefaultLaunchConfirmS)
	}
	if got := (*Policy)(nil).LaunchConfirmS(); got != DefaultLaunchConfirmS {
		t.Errorf("nil policy LaunchConfirmS = %d, want %d", got, DefaultLaunchConfirmS)
	}
	p := &Policy{}
	p.Backend.Orca.LaunchConfirmS = 90
	if got := p.LaunchConfirmS(); got != 90 {
		t.Errorf("override LaunchConfirmS = %d, want 90", got)
	}
}

func TestResolveProjectOverrideIsSectionGranular(t *testing.T) {
	root := t.TempDir()
	if _, err := Init(root, nil); err != nil {
		t.Fatal(err)
	}
	// A project that overrides only delivery keeps every other section from the workspace policy.
	projDir := filepath.Join(root, "pipo")
	if err := os.MkdirAll(filepath.Join(projDir, "cox"), 0o755); err != nil {
		t.Fatal(err)
	}
	override := `{"delivery":{"style":"pipo","why":"pipo commits stay local","review_when":"when the repo moves to draft-PR flow"}}`
	if err := os.WriteFile(filepath.Join(projDir, "cox", "policy.json"), []byte(override), 0o644); err != nil {
		t.Fatal(err)
	}
	p, err := Resolve(root, projDir)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if p.Delivery.Style != "pipo" {
		t.Errorf("delivery override not applied: %+v", p.Delivery)
	}
	if p.Context.PlanCompact != 400000 || p.Waves.Value != "backend-first" {
		t.Errorf("non-overridden sections must survive: %+v %+v", p.Context, p.Waves)
	}
}

func TestResolveRejectsOverrideThatDropsWhy(t *testing.T) {
	root := t.TempDir()
	if _, err := Init(root, nil); err != nil {
		t.Fatal(err)
	}
	projDir := filepath.Join(root, "bad")
	if err := os.MkdirAll(filepath.Join(projDir, "cox"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Override delivery but omit why -> merged policy must fail validation naming delivery.
	override := `{"delivery":{"style":"pipo","review_when":"x"}}`
	if err := os.WriteFile(filepath.Join(projDir, "cox", "policy.json"), []byte(override), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Resolve(root, projDir)
	if err == nil || !strings.Contains(err.Error(), "delivery") {
		t.Fatalf("override dropping why must fail naming delivery, got %v", err)
	}
}

func TestFromReposMD(t *testing.T) {
	md := `# Repos

| Alias | Repo | Role | Production | Staging | Delivery |
| --- | --- | --- | --- | --- | --- |
| admin | ExampleOrg/acme-partner-admin | web | master | release | direct-PR |
| services | ExampleOrg/acme-services | backend | master | release, release-no-pin | direct-PR |
| apps | acme-apps (ExampleOrg) | mobile | master | none | direct-PR |

Some prose after.
`
	dir := t.TempDir()
	path := filepath.Join(dir, "repos.md")
	if err := os.WriteFile(path, []byte(md), 0o644); err != nil {
		t.Fatal(err)
	}
	ws, err := FromReposMD(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(ws.Repos) != 3 {
		t.Fatalf("want 3 repos, got %d: %+v", len(ws.Repos), ws.Repos)
	}
	admin, ok := ws.Repo("admin")
	if !ok || admin.Name != "ExampleOrg/acme-partner-admin" || admin.Production != "master" || admin.Staging != "release" {
		t.Errorf("admin parsed wrong: %+v", admin)
	}
	apps, _ := ws.Repo("apps")
	if apps.Name != "acme-apps" || apps.Staging != "" {
		t.Errorf("apps: owner note should be stripped and none staging empty: %+v", apps)
	}
	svc, _ := ws.Repo("services")
	if svc.Staging != "release" {
		t.Errorf("services staging should keep the first token: %+v", svc)
	}
}

func TestFromReposMDErrorsWithoutRows(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "repos.md")
	if err := os.WriteFile(path, []byte("# no tables here\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := FromReposMD(path); err == nil {
		t.Fatal("expected error when no repo rows")
	}
}

func TestRepoRefPrefersPath(t *testing.T) {
	r := Repo{Name: "org/x", Path: "/abs/x"}
	if r.Ref() != "/abs/x" {
		t.Errorf("Ref should prefer path, got %q", r.Ref())
	}
	r2 := Repo{Name: "org/x"}
	if r2.Ref() != "org/x" {
		t.Errorf("Ref should fall back to name, got %q", r2.Ref())
	}
}
