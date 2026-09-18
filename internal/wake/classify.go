// Package wake is the generation-stamped wake queue (coxswain.wake.v1) the zero-token watcher uses to hand work to the
// leader without polling. Each classified event is one line in <epic>/.cox/wake.jsonl with an increasing gen. Classify
// ports the classification of v1 bin/watch.sh; Append/Drain/AckThrough manage the queue; Wait blocks (5s poll, no
// fsnotify) until a new wake arrives or the deadline passes.
package wake

import (
	"regexp"
	"strings"

	"github.com/nphattai/coxswain/internal/adapter/backend"
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
	KindRunaway       Kind = "runaway"
	KindStale         Kind = "stale"
	KindUnknownProbe  Kind = "unknown_probe"
	KindStatus        Kind = "status"
	KindIdleNoDone    Kind = "idle_no_done"
	KindQuotaLow      Kind = "quota_low"    // a harness at/near quota exhaustion (M11)
	KindQuotaHealth   Kind = "quota_health" // the automatic quota source stayed unknown for two polls (M11/M13b)

	// Review wakes (M13): a lavish review comment lands as review_feedback (routine, batched); a review decision or a
	// captain answer lands as review_decision (urgent, starts a turn). Per-Kind urgency, not a per-instance tag (the
	// arena round-1 reviewer claim: the closed enum cannot express per-instance urgency without a signature change).
	KindReviewFeedback Kind = "review_feedback"
	KindReviewDecision Kind = "review_decision"

	// KindHeartbeat is not a schema kind: it is a liveness ping the watcher tracks for phase/stale, never a wake.
	KindHeartbeat Kind = "heartbeat"
)

// urgentKinds start a leader turn at once (v1 hook-stop-rewake batching rule). The rest (stale, unknown_probe, status,
// quota_health) batch up to WAKE_BATCH so several routine wakes cost one turn. quota_low is watcher-generated (never from
// mail), and its urgency varies per case (urgent on exhausted_now or below low_percent, routine on a projected shortfall),
// so the quota pass rings the leader doorbell directly for the urgent case rather than relying on this map.
var urgentKinds = map[Kind]bool{
	KindQuestion: true, KindInputRequired: true, KindPRReady: true,
	KindWorkerDone: true, KindStuck: true, KindRunaway: true,
	KindIdleNoDone: true, KindReviewDecision: true,
}

// IsUrgent reports whether a wake of this kind should start a leader turn immediately.
func IsUrgent(k Kind) bool { return urgentKinds[k] }

// prReadyRe / actionableRe port the v1 watch.sh actionable-status regex, split so a PR-ready note becomes pr_ready and
// a decision-point note becomes input_required. Case-insensitive, matched against "<subject> <body>".
var (
	prReadyRe    = regexp.MustCompile(`(?i)ready for review|pr ready|pr #?\d+ ready|ready to merge`)
	actionableRe = regexp.MustCompile(`(?i)ready to compact|review (changes|fixes) done|worker_done|blocked|need(s)? (a )?(decision|ruling|answer)|plan ready|all .*phases done`)
)

// Classify maps a backend mail message to a wake kind, porting v1 bin/watch.sh. Message type is authoritative:
// question, worker_done, and merge_ready map straight through; escalation is a leader-blocking event (input_required);
// a status is actionable (pr_ready or input_required) only when its subject/body matches the v1 regex, else a plain
// status log; a heartbeat is the liveness sentinel. An unrecognized type is treated as a status log.
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
		// Orca allows one worker_done per dispatch, so a re-run reports completion as a status whose subject starts
		// "done:"; the watcher treats that as a worker_done so it advances the story and clears idle_no_done (F9).
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(msg.Subject)), "done:") {
			return KindWorkerDone
		}
		text := msg.Subject + " " + msg.Body
		switch {
		case prReadyRe.MatchString(text):
			return KindPRReady
		case actionableRe.MatchString(text):
			return KindInputRequired
		default:
			return KindStatus
		}
	default:
		return KindStatus
	}
}
