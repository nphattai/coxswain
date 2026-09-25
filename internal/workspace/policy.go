package workspace

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"

	"github.com/nphattai/coxswain/internal/adapter/harness"
)

// Meta is the mandatory justification every policy section carries: why the rule exists (a measurement or a captain
// ruling, principle P7) and the condition under which it should be reviewed. A section missing either is a validation
// error naming the section, so policy can never harden a value with no recorded reason.
type Meta struct {
	Why        string `json:"why"`
	ReviewWhen string `json:"review_when"`
}

func (m Meta) missing() []string {
	var out []string
	if strings.TrimSpace(m.Why) == "" {
		out = append(out, "why")
	}
	if strings.TrimSpace(m.ReviewWhen) == "" {
		out = append(out, "review_when")
	}
	return out
}

// AllowParallel records the condition under which more than one worker may share a repo (decision 3) and whether the
// scheduler actually enforces it. Enforced is false until a scheduler checks files_owned overlap, so parallel-same-repo
// stays opt-out-of-safety and cox story dispatch only warns.
type AllowParallel struct {
	FilesOwned string `json:"files_owned"` // the disjointness condition, e.g. "disjoint"
	Enforced   bool   `json:"enforced"`    // true only when a scheduler rejects an overlap
}

// WorkersPerRepo is the topology default (decision 3): one worker per repo unless files_owned are disjoint.
type WorkersPerRepo struct {
	Meta
	Value             int           `json:"value"`
	AllowParallelWhen AllowParallel `json:"allow_parallel_when"`
}

// Waves is the wave-ordering default (D24): backend-first, clients depend on the backend story.
type Waves struct {
	Meta
	Value string `json:"value"` // "backend-first" | "single"
}

// Context holds the compaction thresholds in tokens (captain 2026-09-08: 400k plan, 500k now).
type Context struct {
	Meta
	PlanCompact int `json:"plan_compact"`
	CompactNow  int `json:"compact_now"`
}

// Arena is the arena trigger policy (5.2).
type Arena struct {
	Meta
	Trigger []string `json:"trigger"`
}

// Delivery is the story delivery style AND merge mode resolved into each story once at creation (F14, item 8). Style is
// the phase/push rhythm - "default": draft PR at the plan gate, push every phase; "pipo": commits stay local, one push at
// the end, PR opened ready. Mode is the merge posture the brief prints and `cox story done --merge` enforces:
// "no-mistakes" (full gates + PR + wait for merge authority), "direct-PR" (push + PR, no extra pipeline; the default that
// matches today's behaviour), or "local-only" (a clean ready branch, no push, wait). An empty Mode reads as direct-PR.
type Delivery struct {
	Meta
	Style string `json:"style"` // "default" | "pipo"
	Mode  string `json:"mode"`  // "no-mistakes" | "direct-PR" | "local-only"; "" => direct-PR
}

// Delivery modes (item 8). DefaultDeliveryMode is direct-PR so an epic policy that predates the field keeps today's
// behaviour (push + PR).
const (
	ModeNoMistakes      = "no-mistakes"
	ModeDirectPR        = "direct-PR"
	ModeLocalOnly       = "local-only"
	DefaultDeliveryMode = ModeDirectPR
)

// Merge is the epic's merge posture (item 8), a justified section. Yolo defaults false: `cox ship merge` is refused
// unless the captain runs it (`--captain`), so the green-at-live-head rule is enforced rather than remembered. Flipping
// yolo to true lets a non-captain terminal merge, which the captain owns the risk of.
type Merge struct {
	Meta
	Yolo bool `json:"yolo"`
}

// HarnessRole is the option set and default for one role (leader | worker). Models maps a harness name to the default
// model id a worker of that harness runs under when a dispatch does not pin one (claude -> claude-opus-5-5, codex ->
// gpt-5.6-sol): a claude-family default typed at a codex worker made codex reject the launch (M10c). Model is the
// legacy single default, read only as claude's default so a pre-map policy keeps working. Both are optional and only
// read for the worker role, via Policy.WorkerModel.
type HarnessRole struct {
	Options []string          `json:"options"`
	Default string            `json:"default"`
	Model   string            `json:"model"`  // legacy: claude's default, kept readable for pre-map policy
	Models  map[string]string `json:"models"` // harness -> default model id
}

// ArenaHarness is the adversary/reviewer harness rule for arena roles.
type ArenaHarness struct {
	Adversary struct {
		Rule    string `json:"rule"`
		Default string `json:"default"`
	} `json:"adversary"`
	Reviewer struct {
		Rule string `json:"rule"`
	} `json:"reviewer"`
}

// Harness is the model-agnostic harness policy (P9, decision 8): options and default per role. Launch maps a harness
// name to the approval/autonomy flags a dispatched worker of that harness launches with (harness.launch.<name>); an
// absent entry falls back to the built-in default (a dispatched worker must run autonomously), and an explicit empty
// list is a captain opt-out that types no flags. See Policy.LaunchFlags.
type Harness struct {
	Meta
	Leader HarnessRole  `json:"leader"`
	Worker HarnessRole  `json:"worker"`
	Arena  ArenaHarness `json:"arena"`
	Launch Launch       `json:"launch"`
	// BusyVerified opts codex into the harness-owned busy record (DESIGN wave-2 item 6). It defaults false: codex is not
	// armed at dispatch and never writes a busy record until a captain flips this, which vouches that a codex-hook writer
	// is wired. claude and pi report their own state from their cards, so this flag only governs codex. It never selects a
	// harness or changes routing.
	BusyVerified bool `json:"busy_verified"`
}

// Launch maps a harness to the flags a launch carries. The flat entries (Worker) are a dispatched worker's autonomy
// flags (harness.launch.<name>); the nested `arena` object (Arena) is an arena role's read-only flags in terminal mode
// (harness.launch.arena.<name>, ADR 0013), kept separate so a role never inherits the worker's bypass flags. A JSON
// value under any key other than "arena" is a worker flag list; the "arena" key holds the per-harness arena map.
type Launch struct {
	Worker map[string][]string
	Arena  map[string][]string
}

// UnmarshalJSON reads the heterogeneous launch object: the "arena" key is a nested per-harness map, every other key is a
// flag list for that harness's dispatched worker.
func (l *Launch) UnmarshalJSON(b []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	l.Worker = map[string][]string{}
	l.Arena = map[string][]string{}
	for k, v := range raw {
		if k == "arena" {
			if err := json.Unmarshal(v, &l.Arena); err != nil {
				return fmt.Errorf("launch.arena: %w", err)
			}
			continue
		}
		var flags []string
		if err := json.Unmarshal(v, &flags); err != nil {
			return fmt.Errorf("launch.%s: %w", k, err)
		}
		l.Worker[k] = flags
	}
	return nil
}

// RoutingProfile is one candidate in a rule's or the default profile array (DESIGN wave-4 item 10). Harness is required;
// Model/Effort/Provider are optional; Floor is a reasoning-class name (one of harness.EffortClasses) the story's effort
// must meet for this candidate to stay eligible (gate 2). The array is quota-ranked by spendPriority after the three
// gates; array order never breaks a tie.
type RoutingProfile struct {
	Harness  string `json:"harness"`
	Model    string `json:"model,omitempty"`
	Effort   string `json:"effort,omitempty"`
	Provider string `json:"provider,omitempty"`
	Floor    string `json:"floor,omitempty"`
}

// RoutingRule is one captain-authored routing rule (DESIGN wave-4 item 10). When is the natural-language match condition
// a model's judgment resolves (the leader at decomposition, or Jev's opt-in typed path); code never matches it. Profiles
// is the non-empty candidate array applied after the match. Approval "captain" makes a matched rule escalate to the
// captain before dispatch instead of routing; "" or "none" dispatches on the ranked candidate. MinConfidence, when
// declared, replaces the typed path's global 0.6 floor for this rule and is checked against the rule's own probability
// (firstmate 795e4b5 `min_confidence`); the model never sees it.
type RoutingRule struct {
	When          string           `json:"when"`
	Profiles      []RoutingProfile `json:"profiles"`
	Approval      string           `json:"approval,omitempty"`       // "" | "none" | "captain"
	MinConfidence *float64         `json:"min_confidence,omitempty"` // nil: the global floor on the answer confidence
}

// Routing is the worker-routing posture (ADR 0011 baseline default + DESIGN wave-4 item 10 rules). It stays a review_when
// default (no `why`, not a justified section): below the baseline bar and with no matching rule, routing filters the
// harness options by card fit and keeps the harness default. Rules and DefaultProfiles add the captain-authored posture a
// model's judgment matches and code applies through the three gates and the spendPriority ranking. Effort maps a story
// kind to its default reasoning-effort class; a story's own `effort:` frontmatter overrides it. MinRunwaySeconds is the
// runway-feasibility floor (gate 3, default DefaultMinRunwaySeconds); TieEpsilon is the spendPriority tie band (default
// DefaultTieEpsilon). Adding these fields never invalidates an existing policy (all optional, code defaults apply).
type Routing struct {
	Default          string            `json:"default"`     // "policy": fall back to the harness default until the bar is met
	ReviewWhen       string            `json:"review_when"` // the baseline-row bar that unlocks a non-default choice
	Rules            []RoutingRule     `json:"rules,omitempty"`
	DefaultProfiles  []RoutingProfile  `json:"default_profiles,omitempty"`
	Effort           map[string]string `json:"effort,omitempty"`             // story kind -> default reasoning-effort class
	MinRunwaySeconds int64             `json:"min_runway_seconds,omitempty"` // gate-3 runway floor; <=0 => DefaultMinRunwaySeconds
	TieEpsilon       float64           `json:"tie_epsilon,omitempty"`        // spendPriority tie band; <=0 => DefaultTieEpsilon
}

// Routing defaults (DESIGN wave-4 item 10), applied when policy declares none.
const (
	DefaultMinRunwaySeconds int64   = 4 * 60 * 60 // 4h runway-feasibility floor (gate 3)
	DefaultTieEpsilon       float64 = 0.01        // spendPriority values within this band are a tie -> escalate
)

// Effort-by-kind code defaults (DESIGN wave-4 item 10): a scout needs the strongest reasoning (ambiguous investigation),
// a ship story the least (well-understood work), an arena role sits between. Any other kind falls back to the ship
// default. A story's `effort:` frontmatter overrides all of this.
var defaultEffortByKind = map[string]string{"scout": "xhigh", "ship": "low", "arena": "high"}

// EffortForKind resolves the default reasoning-effort class for a story kind: the policy routing.effort override for that
// kind, else the code default, else the ship default. A story's own frontmatter effort wins over this (resolved by the
// caller). A nil policy still yields the code default.
func (p *Policy) EffortForKind(kind string) string {
	if kind == "" {
		kind = "ship"
	}
	if p != nil {
		if e, ok := p.Routing.Effort[kind]; ok && strings.TrimSpace(e) != "" {
			return strings.TrimSpace(e)
		}
	}
	if e, ok := defaultEffortByKind[kind]; ok {
		return e
	}
	return defaultEffortByKind["ship"]
}

// RoutingMinRunwaySeconds returns the gate-3 runway-feasibility floor in seconds, or DefaultMinRunwaySeconds when policy
// is nil or the value is unset. A caller that could not load policy still gets the 4h floor.
func (p *Policy) RoutingMinRunwaySeconds() int64 {
	if p == nil || p.Routing.MinRunwaySeconds <= 0 {
		return DefaultMinRunwaySeconds
	}
	return p.Routing.MinRunwaySeconds
}

// RoutingTieEpsilon returns the spendPriority tie band, or DefaultTieEpsilon when policy is nil or the value is unset.
func (p *Policy) RoutingTieEpsilon() float64 {
	if p == nil || p.Routing.TieEpsilon <= 0 {
		return DefaultTieEpsilon
	}
	return p.Routing.TieEpsilon
}

// Alerts is the optional out-of-band notification policy (item 3, adapts firstmate's wedge alarm). channel is
// off|osascript|command:<cmd>; the default (unset) is off, so cox never posts a notification unless the captain opts in.
// The watcher fires it, rate-limited, when the leader terminal has been unreachable for three consecutive doorbell
// nudges (see docs/reference/policy-json.md).
type Alerts struct {
	Channel string `json:"channel"`
}

// QuotaNPX is the explicit npx opt-in for the quota-axi adapter (policy quota.npx): an exact version and integrity value.
// null (the default) means npx is never used - the installed binary is the trustworthy default (M11).
type QuotaNPX struct {
	Version   string `json:"version"`
	Integrity string `json:"integrity"`
}

// Quota is the observe-only quota policy (M11). Like routing and backend it carries why/review_when for the record but is
// not a mandatory justified section (an epic policy without it keeps working on the code defaults), so adding this field
// never invalidates an existing policy. Binary overrides the PATH lookup for quota-axi; NPX is the opt-in fallback;
// low_percent/ok_percent/min_runway_hours/poll_minutes are the wake and gate thresholds.
type Quota struct {
	Meta
	Binary         string    `json:"binary"`
	NPX            *QuotaNPX `json:"npx"`
	LowPercent     int       `json:"low_percent"`
	OKPercent      int       `json:"ok_percent"`
	MinRunwayHours int       `json:"min_runway_hours"`
	PollMinutes    int       `json:"poll_minutes"`
}

// Quota defaults, applied when policy declares none (or is nil).
const (
	DefaultQuotaLowPercent     = 10
	DefaultQuotaOKPercent      = 25
	DefaultQuotaMinRunwayHours = 24
	DefaultQuotaPollMinutes    = 5
)

// Orca plane values (ADR 0012, decision 4).
const (
	PlaneOrchestration = "orchestration" // drive Orca via task-create/worker-start + orchestration mailbox
	PlaneTerminal      = "terminal"      // drive Orca via worktree + terminal only; cox owns handoff
	// DefaultOrcaPlane is the plane used when policy declares no backend.orca.plane. M10 flips it to terminal (ADR 0012,
	// decision 4) after preparing the live E2E; the tech lead reverts this one commit if the live E2E or a live story
	// fails on claude or codex, which drops the default back to orchestration without touching the rest of M10.
	DefaultOrcaPlane = PlaneTerminal
)

// OrcaBackend selects the Orca driving plane (ADR 0012). It is a compatibility switch, not a hardened behavioral value,
// so like Routing it carries no why/review_when and is not one of the justified sections(): an absent section means the
// code default (DefaultOrcaPlane), so no existing epic policy is invalidated by adding this field.
type OrcaBackend struct {
	Plane          string `json:"plane"`            // "orchestration" | "terminal"; "" => DefaultOrcaPlane
	LaunchConfirmS int    `json:"launch_confirm_s"` // terminal-plane spawn confirm window in seconds; <=0 => DefaultLaunchConfirmS
}

// Backend groups per-backend policy. Only Orca has a plane switch today.
type Backend struct {
	Orca OrcaBackend `json:"orca"`
}

// Watch groups the watcher-window overrides. Like backend and quota it is additive and not a justified section, so an
// epic policy without it uses the code defaults (an absent section never invalidates a policy). BusyTurnMaxMin overrides
// the busy-turn-max window (DESIGN wave-2 item 6d); <=0 means the watcher default (DefaultBusyTurnMax).
type Watch struct {
	BusyTurnMaxMin int `json:"busy_turn_max_min"`
}

// ReviewNPX is the explicit npx opt-in for the lavish review adapter (policy review.npx): an exact version and integrity
// value. null (the default) means npx is never used; the installed binary is the trustworthy default (same rule as
// quota.npx, M13).
type ReviewNPX struct {
	Version   string `json:"version"`
	Integrity string `json:"integrity"`
}

// Review is the visual-review policy (M13). Like quota and backend it carries why/review_when for the record but is not a
// mandatory justified section, so adding it never invalidates an existing policy (an epic without it uses the code
// defaults and lavish stays off). Surface selects the review surface ("lavish" | "none"); Binary overrides the PATH
// lookup for lavish-axi; NPX is the opt-in fallback; Share defaults false and ht-ml.app publishing is refused unless
// cox review share is given --share (outward-facing, DESIGN).
type Review struct {
	Meta
	Surface string     `json:"surface"`
	Binary  string     `json:"binary"`
	NPX     *ReviewNPX `json:"npx"`
	Share   bool       `json:"share"`
}

// Policy is the parsed cox/policy.json. Every top-level section embeds Meta and must carry why + review_when, except
// routing, which is a review_when default and not a behavioral rule on its own.
type Policy struct {
	WorkersPerRepo WorkersPerRepo `json:"workers_per_repo"`
	Waves          Waves          `json:"waves"`
	Context        Context        `json:"context"`
	Arena          Arena          `json:"arena"`
	Delivery       Delivery       `json:"delivery"`
	Harness        Harness        `json:"harness"`
	Routing        Routing        `json:"routing"`
	Backend        Backend        `json:"backend"`
	Quota          Quota          `json:"quota"`
	Review         Review         `json:"review"`
	Alerts         Alerts         `json:"alerts"`
	Watch          Watch          `json:"watch"`
	MergePosture   Merge          `json:"merge"`
}

// DeliveryMode returns the resolved delivery mode, or DefaultDeliveryMode (direct-PR) when policy is nil or the mode is
// unset, so a caller that could not load policy still resolves a mode.
func (p *Policy) DeliveryMode() string {
	if p == nil || strings.TrimSpace(p.Delivery.Mode) == "" {
		return DefaultDeliveryMode
	}
	return strings.TrimSpace(p.Delivery.Mode)
}

// MergeYolo reports whether policy opts a non-captain terminal into `cox ship merge` (default false). A nil policy is
// false, so a caller that could not load policy never lets a worker or leader merge.
func (p *Policy) MergeYolo() bool {
	return p != nil && p.MergePosture.Yolo
}

// ValidDeliveryMode reports whether a delivery mode string is one cox understands. An empty string is valid (it reads as
// DefaultDeliveryMode); any other unrecognised value is a load error, so a typo never silently disables the gates.
func ValidDeliveryMode(mode string) bool {
	switch mode {
	case "", ModeNoMistakes, ModeDirectPR, ModeLocalOnly:
		return true
	default:
		return false
	}
}

// BusyVerified reports whether policy opts codex into the harness-owned busy record (default false, DESIGN wave-2 item
// 6). A nil policy is false, so a caller that could not load policy never arms codex.
func (p *Policy) BusyVerified() bool {
	return p != nil && p.Harness.BusyVerified
}

// BusyTurnMaxMinutes returns the busy-turn-max override in minutes, or 0 when policy is nil or the value is unset (the
// caller then falls back to the watcher default). See DESIGN wave-2 item 6d.
func (p *Policy) BusyTurnMaxMinutes() int {
	if p == nil || p.Watch.BusyTurnMaxMin <= 0 {
		return 0
	}
	return p.Watch.BusyTurnMaxMin
}

// AlertsChannel returns the configured out-of-band alarm channel directives (off|auto|osascript|command:<cmd>, one per
// line), or "auto" when policy is nil or the channel is unset: an absent config is default-on (firstmate
// docs/wedge-alarm.md:21; supersedes the item 3 "unset means off" reading).
func (p *Policy) AlertsChannel() string {
	if p == nil || strings.TrimSpace(p.Alerts.Channel) == "" {
		return "auto"
	}
	return strings.TrimSpace(p.Alerts.Channel)
}

// QuotaLowPercent/QuotaOKPercent/QuotaMinRunwayHours/QuotaPollMinutes return the quota thresholds, falling back to the
// defaults when policy is nil or the value is unset. A caller that could not load policy still gets working defaults.
func (p *Policy) QuotaLowPercent() int {
	if p != nil && p.Quota.LowPercent > 0 {
		return p.Quota.LowPercent
	}
	return DefaultQuotaLowPercent
}

func (p *Policy) QuotaOKPercent() int {
	if p != nil && p.Quota.OKPercent > 0 {
		return p.Quota.OKPercent
	}
	return DefaultQuotaOKPercent
}

func (p *Policy) QuotaMinRunwayHours() int {
	if p != nil && p.Quota.MinRunwayHours > 0 {
		return p.Quota.MinRunwayHours
	}
	return DefaultQuotaMinRunwayHours
}

func (p *Policy) QuotaPollMinutes() int {
	if p != nil && p.Quota.PollMinutes > 0 {
		return p.Quota.PollMinutes
	}
	return DefaultQuotaPollMinutes
}

// DefaultWorkerModel is the captain ruling for a dispatched claude worker with no pinned model: Opus 5.5 (captain
// 2026-09-23, B-52; was Opus 4.8 from the 2026-09-03 ruling). It is the
// final fallback under WorkerModel for the claude harness so a claude launch line always carries a --model, even with
// no policy loaded (ADR 0012 / M10b). Other harnesses have no such ruling: an unmapped harness resolves to no model.
const DefaultWorkerModel = "claude-opus-5-5"

// WorkerModel resolves the model a worker of harness `h` runs under and whether one was found: an explicit dispatch
// model wins, else the per-harness policy default (harness.worker.models[h]), else the legacy harness.worker.model as
// claude's default, else DefaultWorkerModel for claude only. A harness with no mapped default returns ("", false) so
// the caller omits --model and lets the harness pick its own default (typing claude's model at codex made codex reject
// the launch, M10c).
func (p *Policy) WorkerModel(h, explicit string) (string, bool) {
	if strings.TrimSpace(explicit) != "" {
		return explicit, true
	}
	if p != nil {
		if m, ok := p.Harness.Worker.Models[h]; ok && strings.TrimSpace(m) != "" {
			return m, true
		}
		if h == "claude" && strings.TrimSpace(p.Harness.Worker.Model) != "" {
			return p.Harness.Worker.Model, true
		}
	}
	if h == "claude" {
		return DefaultWorkerModel, true
	}
	return "", false
}

// LaunchFlags returns the approval/autonomy flags a dispatched worker of the named harness launches with. An explicit
// policy entry (including an empty list, a captain opt-out) wins; when the harness has no entry, or policy is nil, the
// built-in default applies so a dispatched worker never deadlocks on a local approval prompt it cannot answer. The
// captain owns the risk of these flags; docs/adapters/{claude,codex}.md record how to tighten them.
func (p *Policy) LaunchFlags(harness string) []string {
	if p != nil {
		if f, ok := p.Harness.Launch.Worker[harness]; ok {
			return f
		}
	}
	return defaultLaunchFlags(harness)
}

// ArenaLaunchFlags returns the read-only flags an arena role of the named harness launches with in terminal mode
// (harness.launch.arena.<name>, ADR 0013). An explicit policy entry (including an empty list) wins; otherwise the
// built-in arena default applies (see defaultArenaLaunchFlags), so a role never inherits the worker's bypass flags even
// when policy is silent.
func (p *Policy) ArenaLaunchFlags(harness string) []string {
	if p != nil {
		if f, ok := p.Harness.Launch.Arena[harness]; ok {
			return f
		}
	}
	return defaultArenaLaunchFlags(harness)
}

// defaultArenaLaunchFlags is the arena baseline for a terminal-mode role when policy declares none. These are the
// terminal flags, not the headless ones: headless is always read-only and hard-coded in arena.headlessArgv. In terminal
// mode a role runs in its own disposable worktree and must be able to write its report there, so codex uses
// -a never -s workspace-write (read-only would let it read but never write, so the report never lands; a read-only codex
// with --add-dir also exits, M12b). claude uses --permission-mode acceptEdits (verified against claude --help): plan mode
// cannot write even with --add-dir, so a terminal claude role in plan mode can only produce a plan to observe
// (docs/adapters/claude.md); plan mode is the headless path (arena.headlessArgv). Both are still tighter than the worker
// bypass flags.
func defaultArenaLaunchFlags(harness string) []string {
	switch harness {
	case "claude":
		return []string{"--permission-mode", "acceptEdits"}
	case "codex":
		return []string{"-a", "never", "-s", "workspace-write"}
	default:
		return nil
	}
}

// defaultLaunchFlags is the autonomy baseline for a dispatched worker when policy declares none. Verified against the
// live CLIs on 2026-09-15 (claude 2.1.272, codex-cli 0.154.0): claude uses --permission-mode bypassPermissions; codex
// has no --full-auto at the top level, so it uses -a never -s workspace-write. An unknown harness gets no flags.
func defaultLaunchFlags(harness string) []string {
	switch harness {
	case "claude":
		return []string{"--permission-mode", "bypassPermissions"}
	case "codex":
		return []string{"-a", "never", "-s", "workspace-write"}
	default:
		return nil
	}
}

// DefaultLaunchConfirmS is the terminal-plane spawn confirm window when policy declares none: 60 seconds, up from the
// original 20s that warned on cold harness starts that were in fact running (M10b, docs/adapters/orca.md live fact).
const DefaultLaunchConfirmS = 60

// LaunchConfirmS returns the terminal-plane spawn confirm window in seconds, or DefaultLaunchConfirmS when policy
// declares none (or is nil). A caller that could not load policy still gets the 60s window.
func (p *Policy) LaunchConfirmS() int {
	if p == nil || p.Backend.Orca.LaunchConfirmS <= 0 {
		return DefaultLaunchConfirmS
	}
	return p.Backend.Orca.LaunchConfirmS
}

// OrcaPlane returns the configured Orca plane, or DefaultOrcaPlane when policy declares none. A nil policy is the
// default too, so a caller that could not load policy still gets a plane.
func (p *Policy) OrcaPlane() string {
	if p == nil || strings.TrimSpace(p.Backend.Orca.Plane) == "" {
		return DefaultOrcaPlane
	}
	return p.Backend.Orca.Plane
}

// section returns each named policy section's Meta for validation, in stable order.
func (p *Policy) sections() []struct {
	name string
	meta Meta
} {
	return []struct {
		name string
		meta Meta
	}{
		{"workers_per_repo", p.WorkersPerRepo.Meta},
		{"waves", p.Waves.Meta},
		{"context", p.Context.Meta},
		{"arena", p.Arena.Meta},
		{"delivery", p.Delivery.Meta},
		{"harness", p.Harness.Meta},
		{"merge", p.MergePosture.Meta},
	}
}

// Validate reports every section missing why or review_when, naming each. It returns nil only when all sections are
// justified. The error lists sections sorted for a deterministic message.
func (p *Policy) Validate() error {
	var problems []string
	for _, s := range p.sections() {
		if m := s.meta.missing(); len(m) > 0 {
			problems = append(problems, fmt.Sprintf("%s (missing %s)", s.name, strings.Join(m, "+")))
		}
	}
	if !ValidDeliveryMode(strings.TrimSpace(p.Delivery.Mode)) {
		problems = append(problems, fmt.Sprintf("delivery (invalid mode %q; want no-mistakes|direct-PR|local-only)", p.Delivery.Mode))
	}
	problems = append(problems, p.routingProblems()...)
	if len(problems) == 0 {
		return nil
	}
	sort.Strings(problems)
	return fmt.Errorf("policy validation failed: %s", strings.Join(problems, "; "))
}

// providerIDRe is the provider-id shape a routing profile's/rule's `provider` must match when present (lower-case,
// hyphen-separated), the same shape firstmate enforces so a fabricated provider name never selects a quota row.
var providerIDRe = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// routingProblems reports every malformed routing rule or default profile, each naming its field, so a typo refuses
// dispatch rather than being silently selected around (DESIGN wave-4 item 10). These are the card-free structural
// checks; the card-fit checks (unknown harness, effort a card does not support) are ValidateRoutingCards, run at dispatch
// where the capability cards are available. Order-stable for a deterministic message.
func (p *Policy) routingProblems() []string {
	var problems []string
	for i, r := range p.Routing.Rules {
		where := fmt.Sprintf("routing.rules[%d]", i)
		if strings.TrimSpace(r.When) == "" {
			problems = append(problems, where+" (missing when)")
		}
		switch r.Approval {
		case "", "none", "captain":
		default:
			problems = append(problems, fmt.Sprintf("%s (invalid approval %q; want none|captain)", where, r.Approval))
		}
		if mc := r.MinConfidence; mc != nil && (*mc < 0 || *mc > 1) {
			problems = append(problems, where+" (min_confidence must be a number from 0 through 1 when present)")
		}
		problems = append(problems, profileArrayProblems(where+".profiles", r.Profiles, true)...)
	}
	if p.Routing.DefaultProfiles != nil {
		problems = append(problems, profileArrayProblems("routing.default_profiles", p.Routing.DefaultProfiles, true)...)
	}
	for kind, e := range p.Routing.Effort {
		if _, ok := harness.EffortRank(e); !ok {
			problems = append(problems, fmt.Sprintf("routing.effort[%s] (unknown effort class %q; want %s)", kind, e, strings.Join(harness.EffortClasses, "|")))
		}
	}
	return problems
}

// profileArrayProblems validates one profile array: non-empty (when requireNonEmpty), no duplicate (harness, model,
// effort) triple, and each profile's fields well formed (harness required; effort/floor a known reasoning class; provider
// matching providerIDRe). Each problem names its field.
func profileArrayProblems(where string, profiles []RoutingProfile, requireNonEmpty bool) []string {
	var problems []string
	if requireNonEmpty && len(profiles) == 0 {
		return []string{where + " (empty; a rule and the default need at least one profile)"}
	}
	seen := map[string]bool{}
	for i, pr := range profiles {
		at := fmt.Sprintf("%s[%d]", where, i)
		if strings.TrimSpace(pr.Harness) == "" {
			problems = append(problems, at+" (missing harness)")
		}
		if pr.Effort != "" {
			if _, ok := harness.EffortRank(pr.Effort); !ok {
				problems = append(problems, fmt.Sprintf("%s (unknown effort class %q; want %s)", at, pr.Effort, strings.Join(harness.EffortClasses, "|")))
			}
		}
		if pr.Floor != "" {
			if _, ok := harness.EffortRank(pr.Floor); !ok {
				problems = append(problems, fmt.Sprintf("%s (unknown floor class %q; want %s)", at, pr.Floor, strings.Join(harness.EffortClasses, "|")))
			}
		}
		if pr.Provider != "" && !providerIDRe.MatchString(pr.Provider) {
			problems = append(problems, fmt.Sprintf("%s (invalid provider %q; want %s)", at, pr.Provider, providerIDRe.String()))
		}
		key := pr.Harness + "\x00" + pr.Model + "\x00" + pr.Effort
		if seen[key] {
			problems = append(problems, fmt.Sprintf("%s (duplicate profile harness=%s model=%s effort=%s)", at, pr.Harness, orDash(pr.Model), orDash(pr.Effort)))
		}
		seen[key] = true
	}
	return problems
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// ValidateRoutingCards is the card-fit half of routing validation, run at dispatch where the capability cards are known:
// every routing profile's harness must have a card (else it is an "unknown harness"), and the card must accept the
// profile's effort (else "effort a card does not support"). It refuses dispatch naming the field, never selecting around
// a bad profile (DESIGN wave-4 item 10). A nil policy is clean (nothing to validate).
func (p *Policy) ValidateRoutingCards(cards map[string]harness.Capability) error {
	if p == nil {
		return nil
	}
	var problems []string
	check := func(where string, profiles []RoutingProfile) {
		for i, pr := range profiles {
			at := fmt.Sprintf("%s[%d]", where, i)
			card, ok := cards[pr.Harness]
			if !ok {
				problems = append(problems, fmt.Sprintf("%s (unknown harness %q: no capability card)", at, pr.Harness))
				continue
			}
			if !card.CardAcceptsEffort(pr.Effort) {
				problems = append(problems, fmt.Sprintf("%s (effort %q not supported by harness %q; card accepts %s)", at, pr.Effort, pr.Harness, strings.Join(card.Efforts, "|")))
			}
		}
	}
	for i, r := range p.Routing.Rules {
		check(fmt.Sprintf("routing.rules[%d].profiles", i), r.Profiles)
	}
	check("routing.default_profiles", p.Routing.DefaultProfiles)
	if len(problems) == 0 {
		return nil
	}
	sort.Strings(problems)
	return fmt.Errorf("routing profile validation failed: %s", strings.Join(problems, "; "))
}

// LoadPolicy reads and parses <ws>/cox/policy.json and validates it. An invalid policy (a section without why or
// review_when) is a hard error naming the section.
func LoadPolicy(wsRoot string) (*Policy, error) {
	return LoadPolicyFile(filepath.Join(wsRoot, ControlDir, "policy.json"))
}

// LoadPolicyFile parses and validates a policy.json at an explicit path.
func LoadPolicyFile(path string) (*Policy, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read policy: %w", err)
	}
	var p Policy
	if err := json.Unmarshal(b, &p); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if err := p.Validate(); err != nil {
		return nil, err
	}
	return &p, nil
}

// Resolve loads the workspace policy and, when <project>/cox/policy.json exists, overlays its present sections on top of
// it (project overrides win). The overlay is section-granular: a project file that sets only `delivery` keeps every
// other section from the workspace policy. The merged policy is validated as a whole, so an override that drops a
// section's why is caught. projectDir may be "" to skip the overlay.
func Resolve(wsRoot, projectDir string) (*Policy, error) {
	base, err := LoadPolicy(wsRoot)
	if err != nil {
		return nil, err
	}
	if projectDir == "" {
		return base, nil
	}
	overridePath := filepath.Join(projectDir, ControlDir, "policy.json")
	b, err := os.ReadFile(overridePath)
	if err != nil {
		if os.IsNotExist(err) {
			return base, nil // no project override
		}
		return nil, fmt.Errorf("read project policy: %w", err)
	}
	// Overlay only the sections the project file actually declares. A declared section REPLACES the base section whole
	// (the destination is zeroed first), so a project that overrides a section must re-supply its why and review_when;
	// an override that omits them is caught by the final Validate rather than silently inheriting a now-wrong reason.
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, fmt.Errorf("parse %s: %w", overridePath, err)
	}
	replace := func(key string, dst, zero any) error {
		r, ok := raw[key]
		if !ok {
			return nil
		}
		// Zero the section, then decode the override onto it.
		reflect.ValueOf(dst).Elem().Set(reflect.ValueOf(zero).Elem())
		return json.Unmarshal(r, dst)
	}
	if err := replace("workers_per_repo", &base.WorkersPerRepo, &WorkersPerRepo{}); err != nil {
		return nil, err
	}
	if err := replace("waves", &base.Waves, &Waves{}); err != nil {
		return nil, err
	}
	if err := replace("context", &base.Context, &Context{}); err != nil {
		return nil, err
	}
	if err := replace("arena", &base.Arena, &Arena{}); err != nil {
		return nil, err
	}
	if err := replace("delivery", &base.Delivery, &Delivery{}); err != nil {
		return nil, err
	}
	if err := replace("harness", &base.Harness, &Harness{}); err != nil {
		return nil, err
	}
	if err := replace("routing", &base.Routing, &Routing{}); err != nil {
		return nil, err
	}
	if err := replace("backend", &base.Backend, &Backend{}); err != nil {
		return nil, err
	}
	if err := replace("quota", &base.Quota, &Quota{}); err != nil {
		return nil, err
	}
	if err := replace("review", &base.Review, &Review{}); err != nil {
		return nil, err
	}
	if err := replace("merge", &base.MergePosture, &Merge{}); err != nil {
		return nil, err
	}
	if err := base.Validate(); err != nil {
		return nil, fmt.Errorf("merged policy (%s over workspace): %w", overridePath, err)
	}
	return base, nil
}
