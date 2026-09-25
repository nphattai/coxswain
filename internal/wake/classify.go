// Package wake is the generation-stamped wake queue (coxswain.wake.v1) the zero-token watcher uses to hand work to the
// leader without polling. Each classified event is one line in <epic>/.cox/wake.jsonl with an increasing gen. Classify
// reads worker status lines with firstmate's grammar (internal/protocol/decision); Append/Drain/AckThrough manage the
// queue; Wait blocks (5s poll, no fsnotify) until a new wake arrives or the deadline passes.
package wake

import (
	"regexp"
	"strings"

	"github.com/nphattai/coxswain/internal/adapter/backend"
	"github.com/nphattai/coxswain/internal/protocol/decision"
)

// Schema is the wake record schema id.
const Schema = "coxswain.wake.v1"

// Kind classifies a wake. The schema kinds are the queue-worthy ones; Heartbeat is an internal sentinel the watcher
// uses for liveness/phase tracking and never writes to the queue.
type Kind string

const (
	KindQuestion      Kind = "question"
	KindInputRequired Kind = "input_required"
	KindPRReady       Kind = "pr_ready"
	KindWorkerDone    Kind = "worker_done"
	KindStuck         Kind = "stuck"
	KindStale         Kind = "stale"
	KindUnknownProbe  Kind = "unknown_probe"
	KindStatus        Kind = "status"
	KindIdleNoDone    Kind = "idle_no_done"
	KindQuotaLow      Kind = "quota_low" // a harness at/near quota exhaustion (M11)
	// KindCheck is a registered custom check's output, or a rejected unauthenticated check (firstmate's check row,
	// fm-watch.sh:2544,2583): always actionable, so urgent.
	KindCheck Kind = "check"

	// Review wakes (M13): a lavish review comment lands as review_feedback (routine, batched); a review decision or a
	// captain answer lands as review_decision (urgent, starts a turn). Per-Kind urgency, not a per-instance tag (the
	// arena round-1 reviewer claim: the closed enum cannot express per-instance urgency without a signature change).
	KindReviewFeedback Kind = "review_feedback"
	KindReviewDecision Kind = "review_decision"

	// KindHeartbeat is not a schema kind: it is a liveness ping the watcher tracks for phase/stale, never a wake.
	KindHeartbeat Kind = "heartbeat"
)

// urgentKinds start a leader turn at once (v1 hook-stop-rewake batching rule). The rest (status, quota_low when routine) batch up
// to WAKE_BATCH so several routine wakes cost one turn. stale and unknown_probe are urgent: firstmate's stale: surfaces
// (wedge escalation, stale surface, gone endpoint, pause recheck) wake the primary (leader ruling 2026-09-24,
// cox-supervision-port wave 2). quota_low is watcher-generated (never from
// mail), and its urgency varies per case (urgent on exhausted_now or below low_percent, routine on a projected shortfall),
// so the quota pass rings the leader doorbell directly for the urgent case rather than relying on this map.
var urgentKinds = map[Kind]bool{
	KindQuestion: true, KindInputRequired: true, KindPRReady: true,
	KindWorkerDone: true, KindStuck: true,
	KindIdleNoDone: true, KindReviewDecision: true,
	KindStale: true, KindUnknownProbe: true,
	KindCheck: true,
}

// IsUrgent reports whether a wake of this kind should start a leader turn immediately.
func IsUrgent(k Kind) bool { return urgentKinds[k] }

// Cox-own verbless vocabulary from the story working rules (compaction, plan approval, review rounds): each is an
// input_required or pr_ready only when it LEADS a line, so prose that merely mentions one never raises a wake.
var (
	coxInputRe = regexp.MustCompile(`(?i)^[[:space:]]*(ready to compact|plan ready|review (changes|fixes) done|all .*phases done)`)
	coxPRRe    = regexp.MustCompile(`(?i)^[[:space:]]*(ready for review|pr #?\d+ ready|ready to merge)`)
	legacyPRRe = regexp.MustCompile(`(?i)PR ready|checks green|ready in branch|merged`)
)

// kindRank orders the kinds a multi-line status can yield; the most urgent line decides the message.
var kindRank = map[Kind]int{KindStatus: 0, KindPRReady: 1, KindStuck: 2, KindInputRequired: 3, KindWorkerDone: 4}

// Classify maps a backend mail message to a wake kind. Message type is authoritative: question, worker_done, and
// merge_ready map straight through; escalation is a leader-blocking event (input_required); a heartbeat is the
// liveness sentinel; an unrecognized type is a status log. A status message is read as firstmate reads a status log
// (internal/protocol/decision, fm-classify-lib.sh): the subject is a status line, each body line a continuation that
// counts only when it is itself a recognised event, and the most urgent line decides (completion only from the subject).
func Classify(msg backend.Message) Kind {
	switch msg.Type {
	case "heartbeat":
		return KindHeartbeat
	case "question":
		return KindQuestion
	case "worker_done":
		return KindWorkerDone
	case "merge_ready":
		return KindPRReady
	case "escalation":
		return KindInputRequired
	case "status":
		best := StatusLineKind(msg.Subject)
		for _, line := range strings.Split(msg.Body, "\n") {
			if !decision.IsEvent(line, "") && !coxInputRe.MatchString(line) && !coxPRRe.MatchString(line) {
				continue // continuation prose never impersonates a declaration
			}
			// Completion is the subject's declaration only (cox F9: a re-run reports "done:" as the subject); a body
			// line may raise attention but never advances the story.
			if k := StatusLineKind(line); k != KindWorkerDone && kindRank[k] > kindRank[best] {
				best = k
			}
		}
		return best
	default:
		return KindStatus
	}
}

// StatusLineKind classifies one worker status line (name map: firstmate captain-relevant -> an urgent kind).
// needs-decision/blocked -> input_required, done -> worker_done, failed -> stuck, only when the line is a real
// declaration (a head/note colon, or a colonless [key=] for a decision, as the fold's declaration guard reads it on the
// unstamped copy); a legacy verbless PR ready/checks green/ready in branch/merged -> pr_ready; working, resolved,
// captain-held, paused and note never escalate from prose. Verbs match case-insensitively (cox F9: "DONE:").
func StatusLineKind(line string) Kind {
	u := decision.Unstamped(line)
	colon := strings.Contains(u, ":")
	verb := strings.ToLower(decision.Verb(line))
	switch verb {
	case "working", decision.ResolveVerb, decision.CaptainHeldVerb, decision.PausedVerb, "note":
		return KindStatus
	case "done":
		if colon {
			return KindWorkerDone
		}
		return KindStatus
	case "failed":
		if colon {
			return KindStuck
		}
		return KindStatus
	case "needs-decision", "blocked":
		// A malformed key has no fold record, but a colon-bearing line is still the decision the leader must see.
		if _, ok := decision.Key(line); colon || (ok && strings.Contains(u, "[key=")) {
			return KindInputRequired
		}
		return KindStatus
	}
	switch {
	case coxInputRe.MatchString(u):
		return KindInputRequired
	case coxPRRe.MatchString(u):
		return KindPRReady
	case !decision.CaptainRelevant(line, ""):
		return KindStatus
	case legacyPRRe.MatchString(u):
		return KindPRReady
	default:
		return KindInputRequired // a captain token in a verbless legacy line: the leader must look
	}
}
