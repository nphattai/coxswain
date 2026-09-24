//go:build port

// Port tests (wave 1, cox-supervision-port-triage): firstmate's classifier suites fm-classify-corr-token and
// fm-classify-decision-key translated case by case against cox's wake.Classify. Firstmate pinned at 1e0e773
// (references/firstmate, read only). Every case is t.Run("FM/<suite>/<case>") with a `// fm: path:line` citation and a
// `// cox:` mechanism tag; a case whose mechanism cox lacks calls notImplemented and fails (DESIGN translation contract).
package wake

import (
	"fmt"
	"strings"
	"testing"

	"github.com/nphattai/coxswain/internal/adapter/backend"
)

// notImplemented fails a case whose firstmate mechanism cox does not have, naming the gap (contract rule 3).
func notImplemented(t *testing.T, mechanism string) {
	t.Helper()
	t.Fatalf("not implemented in cox: %s", mechanism)
}

// statusKind classifies one worker status line the way the watcher sees it: a status message whose subject is the line.
func statusKind(line string) Kind {
	return Classify(backend.Message{Type: "status", Subject: line})
}

// Mechanism names for the report's red list (the `// cox:` tag on each case names its primary one).
const (
	mechFold      = "decision fold: open/close decisions by [key=] across a worker's status history"
	mechDeclWait  = "declared wait: paused / captain-held status verbs"
	mechGrammar   = "status-line grammar: [at=] event time"
	mechOverride  = "captain-relevance override (FM_CAPTAIN_RE)"
	mechClosing   = "per-key closing verb (resolved vs captain-held vs still open)"
	corr1, corr2  = "c44897ee2db4326b", "7ab3e5dd13c9a993"
	fmCorr        = "tests/fm-classify-corr-token.test.sh"
	fmDecisionKey = "tests/fm-classify-decision-key.test.sh"
)

// Name map (firstmate -> cox): a firstmate status line is a worker status message (Type "status", the line as its
// subject); "captain-relevant" / "opens a decision" is an urgent wake kind (input_required, worker_done, ...); a
// non-transition or routine line is a non-urgent kind (status); the terminal verb "done" is worker_done.

// wantUrgent asserts the line reaches the leader at once (firstmate: captain-relevant / opens a decision).
func wantUrgent(t *testing.T, line string) {
	t.Helper()
	if k := statusKind(line); !IsUrgent(k) {
		t.Errorf("want urgent (captain-relevant), got %s for %q", k, line)
	}
}

// wantRoutine asserts the line does not reach the leader as an urgent event (firstmate: not captain-relevant, or not a
// decision transition).
func wantRoutine(t *testing.T, line string) {
	t.Helper()
	if k := statusKind(line); IsUrgent(k) {
		t.Errorf("want routine (non-transition), got urgent %s for %q", k, line)
	}
}

// wantNotOpener asserts the line does not raise a decision (input_required), whatever else it is.
func wantNotOpener(t *testing.T, line string) {
	t.Helper()
	if k := statusKind(line); k == KindInputRequired {
		t.Errorf("prose/impostor line raised a decision (input_required): %q", line)
	}
}

// wantKind asserts the exact kind.
func wantKind(t *testing.T, line string, want Kind) {
	t.Helper()
	if k := statusKind(line); k != want {
		t.Errorf("want %s, got %s for %q", want, k, line)
	}
}

func TestPortClassifyCorrToken(t *testing.T) {
	const s = "FM/fm-classify-corr-token/"

	t.Run(s+"tokened_opener_opens_and_tokened_closer_closes", func(t *testing.T) {
		// fm: tests/fm-classify-corr-token.test.sh:48
		// cox: decision fold
		wantUrgent(t, "needs-decision corr="+corr1+" [key=texte-du-mur]: propose the wall text")
		wantRoutine(t, "resolved corr="+corr1+" [key=texte-du-mur]: captain chose the third wording")
		wantUrgent(t, "blocked corr="+corr2+" [key=sortie-plan]: the only lever exceeds the ruling")
		wantRoutine(t, "captain-held corr="+corr2+" [key=sortie-plan]: tracked as a captain hold")
		notImplemented(t, mechFold)
	})

	t.Run(s+"token_is_read_through_in_every_position_it_is_written_in", func(t *testing.T) {
		// fm: tests/fm-classify-corr-token.test.sh:90
		// cox: decision fold
		for _, l := range []string{
			"needs-decision corr=" + corr1 + " [key=before]: token ahead of the key",
			"needs-decision [key=after] corr=" + corr1 + ": token behind the key",
			"blocked corr=" + corr1 + ": token and no key at all",
			"needs-decision corr=" + corr1 + " corr=" + corr2 + " [key=twice]: two tokens on one line",
			"needs-decision [corr=" + corr1 + "]: the helper bracket shape",
		} {
			wantUrgent(t, l)
		}
		for _, l := range []string{
			"resolved corr=" + corr1 + " [key=before]: closed",
			"resolved [key=after] corr=" + corr1 + ": closed",
			"resolved corr=" + corr1 + ": closed",
			"resolved corr=" + corr1 + " corr=" + corr2 + " [key=twice]: closed",
			"resolved [corr=" + corr1 + "]: closed",
		} {
			wantRoutine(t, l)
		}
		notImplemented(t, mechFold)
	})

	t.Run(s+"untokened_pair_is_unchanged", func(t *testing.T) {
		// fm: tests/fm-classify-corr-token.test.sh:127
		// cox: decision fold
		wantUrgent(t, "needs-decision [key=api-shape]: pick REST or RPC")
		wantUrgent(t, "blocked: no key at all")
		wantRoutine(t, "resolved [key=api-shape]: went with REST")
		wantRoutine(t, "resolved: cleared on its own")
		notImplemented(t, mechFold)
	})

	t.Run(s+"prose_and_malformed_tokens_never_become_transitions", func(t *testing.T) {
		// fm: tests/fm-classify-corr-token.test.sh:151
		// cox: decision fold
		impostors := []string{
			"resolved the corr= issue yesterday [key=victim]",
			"resolved corr= [key=victim]",
			"resolved corr=deadbeef [key=victim]",
			"resolved corr=abcdef0123456789ab [key=victim]",
			"resolved corr=ZZZZbeefdeadbeef [key=victim]",
			"resolved xcorr=c44897ee2db4326b [key=victim]",
			"resolved corr=c44897ee2db4326 [key=victim]",
			"resolved anything=whatever [key=victim]",
			"resolved and then corr=c44897ee2db4326b happened [key=victim]",
		}
		for _, l := range impostors {
			wantNotOpener(t, strings.Replace(l, "resolved", "needs-decision", 1)+": free text that must not open anything")
		}
		wantUrgent(t, "needs-decision [key=noted]: see corr=c44897ee2db4326b in the thread")
		wantRoutine(t, "working: chasing corr=c44897ee2db4326b through the log")
		// The closing half (an impostor must not close the real decision) needs the fold.
		notImplemented(t, mechFold)
	})

	t.Run(s+"token_first_word_never_impersonates_a_transition", func(t *testing.T) {
		// fm: tests/fm-classify-corr-token.test.sh:221
		// cox: wake.Classify + decision fold
		wantNotOpener(t, "corr="+corr1+" needs-decision [key=token-first-needs]: prose")
		wantNotOpener(t, "corr="+corr1+" blocked [key=token-first-blocked]: prose")
		wantUrgent(t, "needs-decision [key=stays-open-resolved]: a real captain decision")
		wantUrgent(t, "blocked [key=stays-open-held]: a real captain blocker")
		notImplemented(t, mechFold)
	})

	t.Run(s+"captain_relevance_and_pause_are_unchanged_without_a_token", func(t *testing.T) {
		// fm: tests/fm-classify-corr-token.test.sh:263
		// cox: wake.Classify + declared wait
		wantUrgent(t, "done: shipped")
		wantUrgent(t, "needs-decision [key=q1]: pick one")
		wantUrgent(t, "blocked: stuck")
		wantUrgent(t, "failed: gave up")
		wantRoutine(t, "working: still going")
		wantRoutine(t, "working: rebased onto merged #76")
		wantUrgent(t, "merged")
		wantRoutine(t, "resolved [key=q1]: answered")
		wantKind(t, "done: shipped", KindWorkerDone)
		if statusKind("working: rebased onto merged #76") == KindWorkerDone {
			t.Errorf("a nonterminal line read as terminal")
		}
		notImplemented(t, mechDeclWait)
	})

	t.Run(s+"consumer_verdicts_read_through_the_token", func(t *testing.T) {
		// fm: tests/fm-classify-corr-token.test.sh:291
		// cox: wake.Classify + declared wait
		wantUrgent(t, "done corr="+corr1+": shipped")
		wantUrgent(t, "needs-decision corr="+corr1+" [key=q]: pick one")
		wantUrgent(t, "blocked corr="+corr1+": stuck")
		wantUrgent(t, "done [corr="+corr1+"]: shipped via the helper")
		wantKind(t, "done corr="+corr1+": shipped", KindWorkerDone)
		if statusKind("working corr="+corr1+": still going") == KindWorkerDone {
			t.Errorf("a correlated working became terminal")
		}
		wantRoutine(t, "working corr="+corr1+": rebased onto merged #76")
		wantRoutine(t, "resolved corr="+corr1+" [key=q]: answered")
		notImplemented(t, mechDeclWait)
	})

	t.Run(s+"daemon_and_crew_state_case_arms_read_through_the_token", func(t *testing.T) {
		// fm: tests/fm-classify-corr-token.test.sh:328
		// cox: wake.Classify
		for _, v := range []string{"working", "resolved", "captain-held"} {
			wantRoutine(t, v+" corr="+corr1+" [key=k]: note")
			wantRoutine(t, v+" [key=k]: note")
		}
		for _, v := range []string{"working", "needs-decision", "blocked", "done", "failed"} {
			if a, b := statusKind(v+" corr="+corr1+": note"), statusKind(v+": note"); a != b {
				t.Errorf("verb %q: tokened line reads %s, untokened %s", v, a, b)
			}
		}
	})

	// n/a test_pending_reply_escalation_matching_is_unaffected (fm-classify-corr-token.test.sh:363): the reserved
	// pending-reply key namespace belongs to firstmate's secondmate reply channel (fm-pending-reply-lib); cox answers
	// questions by qNNN through `cox reply`, with no status-line keys.

	t.Run(s+"incremental_and_whole_file_folds_agree_over_correlated_lines", func(t *testing.T) {
		// fm: tests/fm-classify-corr-token.test.sh:402
		// cox: decision fold
		for i := 0; i < 6; i++ {
			wantUrgent(t, fmt.Sprintf("needs-decision corr=%s [key=k%d]: decision %d", corr1, i, i))
			wantRoutine(t, fmt.Sprintf("working corr=%s: routine progress %d", corr2, i))
		}
		notImplemented(t, mechFold)
	})

	t.Run(s+"a_cursor_written_before_this_change_is_rebuilt", func(t *testing.T) {
		// fm: tests/fm-classify-corr-token.test.sh:455
		// cox: decision fold
		wantUrgent(t, "needs-decision corr="+corr1+" [key=owed]: a decision the captain is owed")
		notImplemented(t, mechFold)
	})

	// n/a test_the_real_writers_produce_tokens_this_library_reads (fm-classify-corr-token.test.sh:488): pins firstmate's
	// own token writers (fm-pending-reply-lib, fm-secondmate-report.sh); cox has no correlation-token writer.

	t.Run(s+"optional_event_time", func(t *testing.T) {
		// fm: tests/fm-classify-corr-token.test.sh:545
		// cox: status-line grammar
		plain := "needs-decision corr=" + corr1 + " [key=timed]: choose: A or B"
		stamped := "needs-decision corr=" + corr1 + " [key=timed] [at=1700000000]: choose: A or B"
		if a, b := statusKind(plain), statusKind(stamped); a != b {
			t.Errorf("event time changed the kind: %s vs %s", a, b)
		}
		wantUrgent(t, stamped)
		wantRoutine(t, "working [at=1700000000]: unrelated progress")
		wantRoutine(t, "resolved [at=1700000001] [key=timed]: answered")
		notImplemented(t, mechGrammar)
	})

	t.Run(s+"captain_override_ignores_event_time", func(t *testing.T) {
		// fm: tests/fm-classify-corr-token.test.sh:639
		// cox: captain-relevance override
		for _, v := range []string{"done", "needs-decision", "blocked", "failed"} {
			for _, l := range []string{v + ": audit complete", v + " [at=1700000000]: audit complete", v + "[at=1700000000]: audit complete"} {
				wantUrgent(t, l)
			}
		}
		for _, v := range []string{"working", "paused", "resolved", "captain-held"} {
			wantRoutine(t, v+": done: mentioned")
			wantRoutine(t, v+" [at=1700000000]: done: mentioned")
		}
		notImplemented(t, mechOverride)
	})

	t.Run(s+"malformed_event_time_is_ordinary_bytes", func(t *testing.T) {
		// fm: tests/fm-classify-corr-token.test.sh:687
		// cox: wake.Classify + decision fold
		tags := []string{"[at=]", "[at=bad]", "[at=17:00]", "[at=bad] [at=17:00]", "[at=2026-09-20T14:03:00Z]",
			"[at=10:30]", "[at=$(date +%s)]", "[at=<epoch>]", "[at=1] [at=2]", "[at=01700000000]", "[at=99999999999999999999]"}
		for _, v := range []string{"done", "needs-decision", "blocked", "failed"} {
			for _, tag := range tags {
				wantUrgent(t, v+" "+tag+": audit complete")
			}
		}
		notImplemented(t, mechFold) // a malformed tag must fold under the default key
	})

	t.Run(s+"malformed_event_time_never_moves_the_decision_fold", func(t *testing.T) {
		// fm: tests/fm-classify-corr-token.test.sh:736
		// cox: decision fold
		for _, tag := range []string{"[at=17:00]", "[at=10:30]", "[at=2026-09-20T14:03:00Z]", "[at=<epoch>]", "[at=bad]"} {
			if statusKind("done "+tag+" finished the audit") == KindWorkerDone {
				t.Errorf("a colonless done %s read as a terminal declaration", tag)
			}
			wantNotOpener(t, "needs-decision "+tag+" which base branch")
		}
		wantKind(t, "done [at=1700000001]: finished the audit", KindWorkerDone)
		notImplemented(t, mechFold)
	})
}

func TestPortClassifyDecisionKey(t *testing.T) {
	const s = "FM/fm-classify-decision-key/"

	t.Run(s+"stated_key_is_honored_in_both_positions", func(t *testing.T) {
		// fm: tests/fm-classify-decision-key.test.sh:51
		// cox: decision fold
		wantUrgent(t, "needs-decision [key=api-shape]: pick REST or RPC")
		wantUrgent(t, "needs-decision: [key=api-shape] pick REST or RPC")
		notImplemented(t, mechFold)
	})

	t.Run(s+"bare_keyless_line_still_folds_to_default", func(t *testing.T) {
		// fm: tests/fm-classify-decision-key.test.sh:70
		// cox: decision fold
		wantUrgent(t, "needs-decision: which color")
		wantRoutine(t, "resolved: went with blue")
		notImplemented(t, mechFold)
	})

	t.Run(s+"resolution_closes_across_positions", func(t *testing.T) {
		// fm: tests/fm-classify-decision-key.test.sh:84
		// cox: decision fold
		wantUrgent(t, "needs-decision: [key=seam-max-bound] pick the bound")
		wantRoutine(t, "resolved [key=seam-max-bound]: answered: use 4")
		wantUrgent(t, "needs-decision [key=seam-max-bound]: pick the bound")
		wantRoutine(t, "resolved: [key=seam-max-bound] answered: use 4")
		notImplemented(t, mechFold)
	})

	t.Run(s+"blocked_is_position_tolerant_like_needs_decision", func(t *testing.T) {
		// fm: tests/fm-classify-decision-key.test.sh:100
		// cox: decision fold
		wantUrgent(t, "blocked [key=creds]: waiting on the deploy token")
		wantUrgent(t, "blocked: [key=creds] waiting on the deploy token")
		notImplemented(t, mechFold)
	})

	t.Run(s+"two_colon_form_decisions_stay_distinct", func(t *testing.T) {
		// fm: tests/fm-classify-decision-key.test.sh:111
		// cox: decision fold
		wantUrgent(t, "needs-decision: [key=alpha] first question")
		wantUrgent(t, "needs-decision: [key=beta] second question")
		wantRoutine(t, "resolved [key=alpha]: answered: yes")
		notImplemented(t, mechFold)
	})

	t.Run(s+"mid_note_prose_mention_is_not_a_stated_key", func(t *testing.T) {
		// fm: tests/fm-classify-decision-key.test.sh:128
		// cox: decision fold
		wantUrgent(t, "needs-decision: pick a [key=red] or [key=blue] theme")
		wantUrgent(t, "needs-decision [key=red]: which shade")
		wantRoutine(t, "working: still thinking about [key=red] here")
		notImplemented(t, mechFold)
	})

	t.Run(s+"malformed_stated_key_never_collapses_to_default", func(t *testing.T) {
		// fm: tests/fm-classify-decision-key.test.sh:146
		// cox: decision fold
		notImplemented(t, mechFold) // a malformed [key=bad key] must open nothing and never fold as default
	})

	t.Run(s+"status_line_verb_strips_every_bracket_tag_before_colon", func(t *testing.T) {
		// fm: tests/fm-classify-decision-key.test.sh:166
		// cox: wake.Classify
		wantKind(t, "needs-decision [corr=d448ea86afa4bf67] [key=loan-installment-cadence-amount]: fill in the terms", KindInputRequired)
		wantKind(t, "needs-decision [key=loan-installment-cadence-amount] [corr=d448ea86afa4bf67]: fill in the terms", KindInputRequired)
		wantKind(t, "needs-decision [corr=d448ea86afa4bf67]: fill in the terms", KindInputRequired)
		wantKind(t, "blocked [corr=aaaa1111bbbb2222] [key=creds]: waiting on the deploy token", KindInputRequired)
		wantRoutine(t, "resolved [corr=aaaa1111bbbb2222] [key=creds]: answered: rotated")
	})

	t.Run(s+"corr_and_key_tags_open_and_close_under_the_stated_key", func(t *testing.T) {
		// fm: tests/fm-classify-decision-key.test.sh:187
		// cox: decision fold
		wantUrgent(t, "needs-decision [corr=d448ea86afa4bf67] [key=loan-installment-cadence-amount]: pick the cadence")
		wantRoutine(t, "resolved [corr=d448ea86afa4bf67] [key=loan-installment-cadence-amount]: answered: monthly")
		notImplemented(t, mechFold)
	})

	t.Run(s+"corr_only_tag_opens_as_default_like_a_bare_line", func(t *testing.T) {
		// fm: tests/fm-classify-decision-key.test.sh:201
		// cox: decision fold
		if a, b := statusKind("needs-decision: which vendor"), statusKind("needs-decision [corr=d448ea86afa4bf67]: which vendor"); a != b {
			t.Errorf("a corr-only tag classified differently than the bare line: %s vs %s", b, a)
		}
		wantUrgent(t, "needs-decision [corr=d448ea86afa4bf67]: which vendor")
		notImplemented(t, mechFold)
	})

	t.Run(s+"key_only_before_colon_still_opens_no_regression", func(t *testing.T) {
		// fm: tests/fm-classify-decision-key.test.sh:215
		// cox: decision fold
		wantUrgent(t, "needs-decision [key=loan-installment-cadence-amount]: pick the cadence")
		notImplemented(t, mechFold)
	})

	t.Run(s+"blocked_and_resolved_are_tag_order_independent", func(t *testing.T) {
		// fm: tests/fm-classify-decision-key.test.sh:225
		// cox: decision fold
		wantUrgent(t, "blocked [corr=aaaa1111bbbb2222] [key=creds]: waiting on the deploy token")
		wantUrgent(t, "blocked [key=creds] [corr=aaaa1111bbbb2222]: waiting on the deploy token")
		wantRoutine(t, "resolved [corr=aaaa1111bbbb2222] [key=creds]: answered: rotated")
		notImplemented(t, mechFold)
	})

	t.Run(s+"incremental_agrees_with_full_fold_across_appends", func(t *testing.T) {
		// fm: tests/fm-classify-decision-key.test.sh:242
		// cox: decision fold
		wantUrgent(t, "needs-decision: [key=seam-max-bound] pick the bound")
		wantRoutine(t, "working: routine progress note")
		wantUrgent(t, "needs-decision: [key=other] a second colon-form question")
		notImplemented(t, mechFold)
	})

	t.Run(s+"closing_verb_separates_resolution_from_durable_transfer", func(t *testing.T) {
		// fm: tests/fm-classify-decision-key.test.sh:285
		// cox: per-key closing verb
		notImplemented(t, mechClosing)
	})

	t.Run(s+"closing_verb_tracks_the_last_transition_in_both_positions", func(t *testing.T) {
		// fm: tests/fm-classify-decision-key.test.sh:314
		// cox: per-key closing verb
		notImplemented(t, mechClosing)
	})

	t.Run(s+"closing_verb_honors_overridden_transition_verbs", func(t *testing.T) {
		// fm: tests/fm-classify-decision-key.test.sh:345
		// cox: per-key closing verb
		notImplemented(t, mechClosing)
	})

	t.Run(s+"closing_verb_filters_unrelated_history_without_subshell_growth", func(t *testing.T) {
		// fm: tests/fm-classify-decision-key.test.sh:365
		// cox: per-key closing verb (the bash subshell-growth bound has no Go analog; the retained-resolution half does)
		wantRoutine(t, "working: mentions [key=route] in prose")
		notImplemented(t, mechClosing)
	})

	t.Run(s+"closing_verb_filter_preserves_terminal_chronology", func(t *testing.T) {
		// fm: tests/fm-classify-decision-key.test.sh:398
		// cox: per-key closing verb
		wantKind(t, "done: report saved", KindWorkerDone)
		notImplemented(t, mechClosing)
	})

	t.Run(s+"bare_prose_cannot_impersonate_a_terminal_declaration", func(t *testing.T) {
		// fm: tests/fm-classify-decision-key.test.sh:429
		// cox: wake.Classify + decision fold
		for _, word := range []string{"done", "failed"} {
			m := backend.Message{Type: "status", Subject: "paused: waiting on the vendor", Body: "Steps remaining:\n " + word}
			if k := Classify(m); IsUrgent(k) {
				t.Errorf("bare %q prose continuation read as a terminal declaration (%s)", word, k)
			}
			wantUrgent(t, word+": real outcome")
		}
		notImplemented(t, mechFold)
	})

	t.Run(s+"bare_prose_cannot_open_or_close_a_decision", func(t *testing.T) {
		// fm: tests/fm-classify-decision-key.test.sh:455
		// cox: wake.Classify + decision fold
		for _, word := range []string{"blocked", "needs-decision", "resolved"} {
			m := backend.Message{Type: "status", Subject: "working: investigating the deploy", Body: "Options considered:\n " + word}
			if k := Classify(m); k == KindInputRequired {
				t.Errorf("bare %q prose continuation raised a decision", word)
			}
		}
		wantUrgent(t, "blocked [key=access]")
		wantRoutine(t, "resolved [key=access]")
		notImplemented(t, mechFold)
	})
}
