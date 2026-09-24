package busy

import (
	"regexp"
	"strings"
)

// Dead is the one process-level verdict: the story's endpoint is gone, so whatever the record says, no turn is running
// (fm_busy_classify_live). Only ClassifyLive returns it.
const Dead = "dead"

// The reasons an Unknown (or Dead) verdict carries in its Source slot, verbatim from fm-busy-lib.sh.
const (
	ReasonMissing        = "missing"         // no record file
	ReasonMalformed      = "malformed"       // unparseable, bad tokens, or a record with no armed gen
	ReasonGenMismatch    = "gen-mismatch"    // a record from a superseded incarnation
	ReasonSourceMismatch = "source-mismatch" // a writer the story's harness does not trust
	ReasonLaunchPrompt   = "launch-prompt"   // a launch still at the dispatch seed, parked on a trust/auth dialog
	ReasonNoTarget       = "no-target"       // no recorded endpoint to check
	ReasonEndpointGone   = "endpoint-gone"   // the endpoint no longer exists (Dead)
)

// Verdict is fm_busy_classify's "<state> <source>": State is busy | idle | unknown | dead; Source is the producing
// source for busy/idle (and for an applied unknown, e.g. "recovery"), or the reason for unknown/dead.
type Verdict struct {
	State  string
	Source string
}

func (v Verdict) String() string { return v.State + " " + v.Source }

// Classify is fm_busy_classify: the semantic classification for a story whose endpoint the caller has already
// established as present. It never probes process state. harness is the story's recorded harness ("" = the harness the
// record was armed for); tail is optional pre-captured plain terminal output, consulted ONLY by the launch-prompt
// backstop (a record still pinned at the dispatch seed whose tail shows a harness trust/auth dialog reads unknown
// launch-prompt, never busy). Cox never captures the tail itself and never classifies busy from rendered text.
func Classify(epic, story, harness, tail string) Verdict {
	rec, reason := readRecord(epic, story)
	// The codex gate (fm `case codex*`): codex has no semantic source until one is verified. Cox arms codex only when
	// policy harness.busy_verified vouches for its hook, so a record armed for codex IS the verification; anything else
	// is unverified, checked before the record is trusted.
	if strings.HasPrefix(harness, "codex") && (reason != "" || !strings.HasPrefix(rec.Harness, "codex")) {
		return Verdict{Unknown, "codex-unverified"}
	}
	if reason != "" {
		return Verdict{Unknown, reason}
	}
	if !rec.trusted(rec.Source) || (harness != "" && rec.Harness != harness) {
		return Verdict{Unknown, ReasonSourceMismatch}
	}
	h := harness
	if h == "" {
		h = rec.Harness
	}
	if rec.State == Busy && rec.Source == "dispatch" && tail != "" && LaunchPromptParked(h, tail) {
		return Verdict{Unknown, ReasonLaunchPrompt}
	}
	return Verdict{rec.State, rec.Source}
}

// ClassifyLive is fm_busy_classify_live: Classify behind the one process-level override - a gone endpoint is dead,
// never busy (B-51). target is the story's recorded endpoint (a terminal handle); exists reports whether it is still
// there. An empty target is unknown no-target. The tail is not consulted here (fm passes none either).
func ClassifyLive(epic, story, harness, target string, exists func(target string) bool) Verdict {
	if target == "" {
		return Verdict{Unknown, ReasonNoTarget}
	}
	if !exists(target) {
		return Verdict{Dead, ReasonEndpointGone}
	}
	return Classify(epic, story, harness, "")
}

// Launch-prompt signatures, verbatim from fm-busy-lib.sh:925-1015. Each dialog's question is paired with one of its own
// rendered option/footer lines, both required, so a worker's own prose quoting the question never matches.
var (
	claudeTrustRe    = regexp.MustCompile(`(?i)Quick safety check: Is this a project you created or one you trust\?`)
	claudeTrustOptRe = regexp.MustCompile(`(?i)No, exit|Enter to confirm`)
	claudeImportsRe  = regexp.MustCompile(`(?i)Allow external CLAUDE\.md file imports\?`)
	claudeImportsOpt = regexp.MustCompile(`(?i)No, disable external imports|Yes, allow external imports`)
	piTrustRe        = regexp.MustCompile(`(?i)Trust project folder\?`)
	piTrustOptRe     = regexp.MustCompile(`(?i)Do not trust`)
	geminiTrustRe    = regexp.MustCompile(`(?i)Do you trust the files in this folder\?`)
	geminiTrustOptRe = regexp.MustCompile(`(?i)Trust folder|Don't trust`)
	geminiAuthRe     = regexp.MustCompile(`(?i)How would you like to authenticate for this project\?`)
	geminiAuthOptRe  = regexp.MustCompile(`(?i)Use Gemini API Key|No authentication method selected`)
	geminiAPIKeyRe   = regexp.MustCompile(`(?i)Enter Gemini API Key`)
)

// LaunchPromptParked is fm_busy_launch_prompt_parked: whether tail shows harness's launch trust/auth dialog. Scoped to
// the harnesses that can read a pinned dispatch seed and ship such a dialog (claude*, pi | pi-signed | omp, gemini);
// every other harness has no signature and never matches.
func LaunchPromptParked(harness, tail string) bool {
	both := func(a, b *regexp.Regexp) bool { return a.MatchString(tail) && b.MatchString(tail) }
	switch {
	case strings.HasPrefix(harness, "claude"):
		return both(claudeTrustRe, claudeTrustOptRe) || both(claudeImportsRe, claudeImportsOpt)
	case harness == "pi" || harness == "pi-signed" || harness == "omp":
		return both(piTrustRe, piTrustOptRe)
	case harness == "gemini":
		return both(geminiTrustRe, geminiTrustOptRe) || both(geminiAuthRe, geminiAuthOptRe) || geminiAPIKeyRe.MatchString(tail)
	}
	return false
}
