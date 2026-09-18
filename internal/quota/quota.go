// Package quota is the coxswain.quota.v1 contract: the only quota type routing, the watcher, cox state, and the board
// read (Option C, m11-quota-routing DESIGN.md). cox owns the contract, not the provider read: two adapters fill it, a
// pinned quota-axi binary (schema 5 only, unknown on any drift) and a captain-declared manual file. A Reading is never
// "quota exhausted" or "quota full" unless a fresh source said so; an unreadable signal renders as Known:false, and
// every consumer prints that as unknown with the reason, never as a fact (F11, ADR 0011: the claude status line is not
// scraped; quota-axi reads first-party endpoints).
package quota

import (
	"context"
	"strings"

	"github.com/nphattai/coxswain/internal/adapter/harness"
)

// Schema is the quota contract id every persisted projection carries.
const Schema = "coxswain.quota.v1"

// Runway mirrors quota-axi's per-scope effective runway status (README "Effective usable runway"). It is completion-risk
// evidence, not a score: exhausted_now and projected_exhaustion carry a UsableRunwaySeconds, through_reset and unknown
// do not.
const (
	RunwayExhaustedNow = "exhausted_now"
	RunwayProjected    = "projected_exhaustion"
	RunwayThroughReset = "through_reset"
	RunwayUnknown      = "unknown"
)

// Source records where a Reading came from, so every surface can show provenance (DESIGN: readings are provenance
// tagged). none is the compatibility default for a harness with no adapter reading at all.
const (
	SourceQuotaAxi = "quota-axi"
	SourceManual   = "manual"
	SourceNone     = "none"
)

// NoRunway is the UsableRunwaySeconds sentinel for a scope quota-axi reports no usable-runway value for (through_reset,
// unknown, or a schema that omits the field). The wake horizon treats it as unknown and never as "0 seconds left".
const NoRunway int64 = -1

// Reading is one (harness, model) quota headroom projection. Known is false whenever the value is not machine-readable
// (no adapter, drift, stale, auth denied, or expired manual); callers render that as unknown and never as a quota
// fact. Model is the window family ("fable", "opus", ...) or "" for the account-wide (all_models) scope. PercentRemaining
// and the runway fields are meaningful only when Known. UsableRunwaySeconds is NoRunway (-1) when the source states no
// finite runway for the scope. WindowIDs is the bounding window set the reading was computed from, so a wrong
// model-to-window match is visible (DESIGN risk).
type Reading struct {
	Harness             string   `json:"harness"`
	Model               string   `json:"model,omitempty"`
	Known               bool     `json:"known"`
	PercentRemaining    int      `json:"percent_remaining"`
	ResetsAt            string   `json:"resets_at,omitempty"` // RFC3339, the binding window's own reset
	Runway              string   `json:"runway"`              // one of the Runway* constants
	UsableRunwaySeconds int64    `json:"usable_runway_seconds"`
	Source              string   `json:"source"`                // one of the Source* constants
	ObservedAt          string   `json:"observed_at,omitempty"` // RFC3339 when the projection was read
	Reason              string   `json:"reason,omitempty"`      // why unknown (empty when Known), or a health note
	WindowIDs           []string `json:"window_ids,omitempty"`
}

// Provider fills the contract from one source. Read runs the source (a quota-axi invocation, a manual file scan) and
// returns every Reading it can produce this cycle; a source that is absent or degraded returns unknown Readings, never
// an error, so a missing quota-axi never fails a consumer (an error is reserved for a real I/O fault the caller should
// surface). ctx bounds a slow read.
type Provider interface {
	Read(ctx context.Context) ([]Reading, error)
}

// Merge combines an automatic source with a manual one under the DESIGN precedence: a fresh automatic reading always
// wins, and a manual reading is used only for a (harness, model) the automatic source could not read (missing or
// Known:false). This keeps a captain-declared reading a fallback, never an override of live data. Order is auto first,
// then any manual-only keys, so a stable table renders the same each call.
func Merge(auto, manual []Reading) []Reading {
	key := func(r Reading) string { return r.Harness + "\x00" + r.Model }
	byKey := map[string]Reading{}
	var order []string
	put := func(r Reading) {
		k := key(r)
		if _, seen := byKey[k]; !seen {
			order = append(order, k)
		}
		byKey[k] = r
	}
	for _, r := range auto {
		put(r)
	}
	for _, m := range manual {
		if cur, ok := byKey[key(m)]; ok && cur.Known {
			continue // automatic fresh wins over any manual entry for the same key
		}
		put(m)
	}
	out := make([]Reading, 0, len(order))
	for _, k := range order {
		out = append(out, byKey[k])
	}
	return out
}

// Pick selects the Reading for a (harness, model) from a merged list: an exact (harness, model) match wins, else a
// model-family match (the reading's Model token appears in the requested model, e.g. "fable" in "claude-fable-5-1"),
// else the account-wide (harness, "") reading, else an unknown reading naming that no source covered it. Callers use it
// for the dispatch gate, cox state, and the quota table, so the harness/model a story runs under maps to one reading.
func Pick(readings []Reading, harnessName, model string) Reading {
	var accountWide *Reading
	var family *Reading
	lower := strings.ToLower(model)
	for i := range readings {
		r := readings[i]
		if r.Harness != harnessName {
			continue
		}
		switch {
		case r.Model == model && model != "":
			return r
		case r.Model == "":
			accountWide = &readings[i]
		case model != "" && strings.Contains(lower, strings.ToLower(r.Model)):
			family = &readings[i]
		}
	}
	if family != nil {
		return *family
	}
	if accountWide != nil {
		return *accountWide
	}
	return Reading{Harness: harnessName, Model: model, Known: false, Runway: RunwayUnknown, UsableRunwaySeconds: NoRunway, Source: SourceNone, Reason: "no quota source for this harness"}
}

// For returns an unknown Reading for a harness with no adapter data. It is the compatibility default routing reads when
// no live source was fetched, so the routing ladder still records "quota unknown, skipped" rather than treating a
// missing source as full (routing is observe-only: it never changes the Choice, ADR 0011).
func For(card harness.Capability) Reading {
	return Reading{
		Harness:             card.Name,
		Known:               false,
		Runway:              RunwayUnknown,
		UsableRunwaySeconds: NoRunway,
		Source:              SourceNone,
		Reason:              "no quota source fetched for routing (observe-only, ADR 0011)",
	}
}

// Readings maps each card's harness name to an unknown reading (the routing default). The live table comes from the
// adapters via cox quota / cox route, not this helper.
func Readings(cards []harness.Capability) map[string]Reading {
	out := make(map[string]Reading, len(cards))
	for _, c := range cards {
		out[c.Name] = For(c)
	}
	return out
}
