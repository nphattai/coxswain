// Port tests (wave 1, cox-supervision-port-triage): firstmate's classifier suites fm-classify-corr-token and
// fm-classify-decision-key translated case by case against cox's wake.Classify. Firstmate pinned at 1e0e773
// (references/firstmate, read only). Every case is t.Run("FM/<suite>/<case>") with a `// fm: path:line` citation and a
// `// cox:` mechanism tag. Wave 2 (cox-supervision-port-w2-wake) replaced each notImplemented gap with the firstmate
// case's own assertions against internal/protocol/decision (the fold, closing verb, relevance and time grammar) and
// wake.OpenDecisionLine (the drain's OPEN DECISIONS rendering).
package wake

import (
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/nphattai/coxswain/internal/adapter/backend"
	"github.com/nphattai/coxswain/internal/protocol/decision"
)

// logs is a set of per-task status histories (firstmate: state/<task>.status).
type logs map[string][]string

// openView renders the drain's OPEN DECISIONS lines over every task's fold, in task order (firstmate drain_open).
func openView(l logs, kind decision.Kind) string {
	var tasks []string
	for task := range l {
		tasks = append(tasks, task)
	}
	sort.Strings(tasks)
	var out []string
	for _, task := range tasks {
		for _, d := range decision.Fold(l[task], kind, decision.Verbs{}) {
			out = append(out, OpenDecisionLine(task, d))
		}
	}
	return strings.Join(out, "\n")
}

// foldTSV renders a fold as firstmate's status_open_decisions prints it: "<key>\t<verb>\t<note>" per line.
func foldTSV(lines []string, kind decision.Kind, v decision.Verbs) string {
	var out []string
	for _, d := range decision.Fold(lines, kind, v) {
		out = append(out, d.Key+"\t"+d.Verb+"\t"+d.Note)
	}
	return strings.Join(out, "\n")
}

// wantFold is firstmate's assert_fold (the incremental half is n/a: cox folds the whole history on every read).
func wantFold(t *testing.T, lines []string, kind decision.Kind, want, label string) {
	t.Helper()
	if got := foldTSV(lines, kind, decision.Verbs{}); got != want {
		t.Errorf("%s: fold = %q, want %q", label, got, want)
	}
}

func wantContains(t *testing.T, view, sub, msg string) {
	t.Helper()
	if !strings.Contains(view, sub) {
		t.Errorf("%s: %q not in view:\n%s", msg, sub, view)
	}
}

func wantAbsent(t *testing.T, view, sub, msg string) {
	t.Helper()
	if strings.Contains(view, sub) {
		t.Errorf("%s: %q in view:\n%s", msg, sub, view)
	}
}

// statusKind classifies one worker status line the way the watcher sees it: a status message whose subject is the line.
func statusKind(line string) Kind {
	return Classify(backend.Message{Type: "status", Subject: line})
}

const corr1, corr2 = "c44897ee2db4326b", "7ab3e5dd13c9a993"

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
		// fm drain_open over the two task logs, both directions.
		l := logs{"task-open": {"needs-decision corr=" + corr1 + " [key=texte-du-mur]: propose the wall text"}}
		wantContains(t, openView(l, decision.KindUnknown), "task-open [key=texte-du-mur] needs-decision: propose the wall text", "a needs-decision carrying a correlation token did not open its key")
		l["task-open"] = append(l["task-open"], "resolved corr="+corr1+" [key=texte-du-mur]: captain chose the third wording")
		wantAbsent(t, openView(l, decision.KindUnknown), "texte-du-mur", "a resolved carrying a correlation token did not close its key")
		l["task-blocked"] = []string{"blocked corr=" + corr2 + " [key=sortie-plan]: the only lever exceeds the ruling"}
		wantContains(t, openView(l, decision.KindUnknown), "task-blocked [key=sortie-plan]", "a blocked carrying a correlation token did not open its key")
		l["task-blocked"] = append(l["task-blocked"], "captain-held corr="+corr2+" [key=sortie-plan]: tracked as a captain hold")
		wantAbsent(t, openView(l, decision.KindUnknown), "sortie-plan", "a captain-held carrying a correlation token did not close its key")
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
		l := logs{
			"t1": {"needs-decision corr=" + corr1 + " [key=before]: token ahead of the key"},
			"t2": {"needs-decision [key=after] corr=" + corr1 + ": token behind the key"},
			"t3": {"blocked corr=" + corr1 + ": token and no key at all"},
			"t4": {"needs-decision corr=" + corr1 + " corr=" + corr2 + " [key=twice]: two tokens on one line"},
			"t5": {"needs-decision [corr=" + corr1 + "]: the helper bracket shape"},
		}
		view := openView(l, decision.KindUnknown)
		wantContains(t, view, "t1 [key=before]", "token before the key did not open")
		wantContains(t, view, "t2 [key=after]", "token after the key did not open")
		wantContains(t, view, "t3 blocked:", "token with no key did not open the default key")
		wantContains(t, view, "t4 [key=twice]", "two tokens on one line did not open")
		wantContains(t, view, "t5 needs-decision:", "the bracketed helper token did not open")
		l["t1"] = append(l["t1"], "resolved corr="+corr1+" [key=before]: closed")
		l["t2"] = append(l["t2"], "resolved [key=after] corr="+corr1+": closed")
		l["t3"] = append(l["t3"], "resolved corr="+corr1+": closed")
		l["t4"] = append(l["t4"], "resolved corr="+corr1+" corr="+corr2+" [key=twice]: closed")
		l["t5"] = append(l["t5"], "resolved [corr="+corr1+"]: closed")
		if view := openView(l, decision.KindUnknown); view != "" {
			t.Errorf("a correlated closer failed to close from some position: %s", view)
		}
	})

	t.Run(s+"untokened_pair_is_unchanged", func(t *testing.T) {
		// fm: tests/fm-classify-corr-token.test.sh:127
		// cox: decision fold
		wantUrgent(t, "needs-decision [key=api-shape]: pick REST or RPC")
		wantUrgent(t, "blocked: no key at all")
		wantRoutine(t, "resolved [key=api-shape]: went with REST")
		wantRoutine(t, "resolved: cleared on its own")
		l := logs{"keyed": {"needs-decision [key=api-shape]: pick REST or RPC"}, "bare": {"blocked: no key at all"}}
		view := openView(l, decision.KindUnknown)
		wantContains(t, view, "keyed [key=api-shape]", "an untokened keyed opener regressed")
		wantContains(t, view, "bare blocked:", "an untokened bare opener regressed")
		l["keyed"] = append(l["keyed"], "resolved [key=api-shape]: went with REST")
		l["bare"] = append(l["bare"], "resolved: cleared on its own")
		if view := openView(l, decision.KindUnknown); view != "" {
			t.Errorf("an untokened closer regressed: %s", view)
		}
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
		l := logs{}
		for i, imp := range impostors {
			l[fmt.Sprintf("close-%d", i)] = []string{"needs-decision [key=victim]: a real captain decision", imp + ": free text that must not close it"}
			l[fmt.Sprintf("open-%d", i)] = []string{strings.Replace(imp, "resolved", "needs-decision", 1) + ": free text that must not open anything"}
		}
		view := openView(l, decision.KindUnknown)
		for i, imp := range impostors {
			wantContains(t, view, fmt.Sprintf("close-%d [key=victim] needs-decision: a real captain decision", i), "an impostor closed a real decision: "+imp)
			wantAbsent(t, view, fmt.Sprintf("open-%d ", i), "an impostor opened a decision nobody raised: "+imp)
		}
		noted := logs{"noted": {"needs-decision [key=noted]: see corr=c44897ee2db4326b in the thread",
			"working: chasing corr=c44897ee2db4326b through the log", "done: resolved corr=c44897ee2db4326b in passing"}}
		wantContains(t, openView(noted, decision.KindUnknown), "noted [key=noted]", "a token quoted inside notes disturbed the fold")
	})

	t.Run(s+"token_first_word_never_impersonates_a_transition", func(t *testing.T) {
		// fm: tests/fm-classify-corr-token.test.sh:221
		// cox: wake.Classify + decision fold
		wantNotOpener(t, "corr="+corr1+" needs-decision [key=token-first-needs]: prose")
		wantNotOpener(t, "corr="+corr1+" blocked [key=token-first-blocked]: prose")
		wantUrgent(t, "needs-decision [key=stays-open-resolved]: a real captain decision")
		wantUrgent(t, "blocked [key=stays-open-held]: a real captain blocker")
		l := logs{
			"token-first-needs":    {"corr=" + corr1 + " needs-decision [key=token-first-needs]: prose"},
			"token-first-blocked":  {"corr=" + corr1 + " blocked [key=token-first-blocked]: prose"},
			"token-first-resolved": {"needs-decision [key=stays-open-resolved]: a real captain decision", "corr=" + corr1 + " resolved [key=stays-open-resolved]: prose"},
			"token-first-held":     {"blocked [key=stays-open-held]: a real captain blocker", "corr=" + corr1 + " captain-held [key=stays-open-held]: prose"},
		}
		view := openView(l, decision.KindUnknown)
		wantAbsent(t, view, "token-first-needs ", "a token-first needs-decision opened a decision")
		wantAbsent(t, view, "token-first-blocked ", "a token-first blocked opened a decision")
		wantContains(t, view, "token-first-resolved [key=stays-open-resolved] needs-decision: a real captain decision", "a token-first resolved closed a real decision")
		wantContains(t, view, "token-first-held [key=stays-open-held] blocked: a real captain blocker", "a token-first captain-held closed a real blocker")
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
		if !decision.IsPaused("paused: waiting on the upstream release") || !decision.IsPaused("  paused:   waiting on a reset") {
			t.Errorf("paused: regressed")
		}
		for _, l := range []string{"blocked: the build is paused upstream", "working: paused the animation loop", ""} {
			if decision.IsPaused(l) {
				t.Errorf("paused-in-prose regressed: %q", l)
			}
		}
		if !decision.IsTerminalVerb("done: shipped") || decision.IsTerminalVerb("working: rebased onto merged #76") {
			t.Errorf("terminal verb regressed")
		}
		if !decision.IsCaptainHeld("captain-held [key=r]: tracked") || decision.IsCaptainHeld("resolved [key=r]: answered") {
			t.Errorf("captain-held regressed")
		}
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
		if !decision.IsTerminalVerb("done corr="+corr1+": shipped") || decision.IsTerminalVerb("working corr="+corr1+": still going") {
			t.Errorf("a correlated terminal verb regressed")
		}
		if !decision.IsPaused("paused corr=" + corr1 + ": waiting on the upstream release") {
			t.Errorf("a correlated pause was not recognised as a declared external wait")
		}
		if !decision.IsCaptainHeld("captain-held corr=" + corr1 + " [key=r]: tracked as a hold") {
			t.Errorf("a correlated captain-held was not recognised")
		}
		if decision.IsPaused("blocked corr=" + corr1 + ": the build is paused upstream") {
			t.Errorf("a correlated blocked mentioning paused false-matched")
		}
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
		// The incremental half is n/a (cox folds the whole history on every read); pin the answer itself per append.
		var lines []string
		for i := 0; i < 6; i++ {
			lines = append(lines, fmt.Sprintf("needs-decision corr=%s [key=k%d]: decision %d", corr1, i, i),
				fmt.Sprintf("working corr=%s: routine progress %d", corr2, i))
			open := decision.Fold(lines, decision.KindUnknown, decision.Verbs{})
			if _, ok := decision.Open(open, fmt.Sprintf("k%d", i)); len(open) != i+1 || !ok {
				t.Errorf("after opening k%d the fold holds %+v", i, open)
			}
		}
		for i := 0; i < 6; i++ {
			lines = append(lines, fmt.Sprintf("resolved corr=%s [key=k%d]: answered %d", corr1, i, i))
			open := decision.Fold(lines, decision.KindUnknown, decision.Verbs{})
			if _, ok := decision.Open(open, fmt.Sprintf("k%d", i)); len(open) != 5-i || ok {
				t.Errorf("after closing k%d the fold holds %+v", i, open)
			}
		}
	})

	t.Run(s+"a_cursor_written_before_this_change_is_rebuilt", func(t *testing.T) {
		// fm: tests/fm-classify-corr-token.test.sh:455
		// cox: decision fold
		wantUrgent(t, "needs-decision corr="+corr1+" [key=owed]: a decision the captain is owed")
		// cox keeps no fold cursor to go stale: every drain refolds the whole history, so the decision surfaces.
		l := logs{"task-stale": {"needs-decision corr=" + corr1 + " [key=owed]: a decision the captain is owed"}}
		wantContains(t, openView(l, decision.KindUnknown), "task-stale [key=owed]", "a decision the captain is owed was hidden")
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
		// n/a halves: status_stamp_line and fm_parent_channel_append_once are firstmate writers cox does not have.
		if e, ok := decision.AtEpoch(stamped); !ok || e != "1700000000" {
			t.Errorf("stamped event has no emission time: %q %v", e, ok)
		}
		if decision.Verb(stamped) != "needs-decision" || decision.Note(stamped) != "choose: A or B" {
			t.Errorf("time changed verb or note: %q %q", decision.Verb(stamped), decision.Note(stamped))
		}
		if k, _ := decision.Key(stamped); k != "timed" {
			t.Errorf("time changed key: %q", k)
		}
		for _, l := range []string{"done: legacy", "done: [at=1700000000] prose", "done [at=]: empty", "done [at=$(date +%s)]: literal substitution",
			"done [at=<epoch>]: unsubstituted placeholder", "done [at=bad]: malformed", "done [at=17:00]: malformed colon", "done [at=-1]: negative",
			"done [at=01700000000]: noncanonical", "done [at=99999999999999999999]: overflow", "done [at=1] [at=2]: ambiguous"} {
			if e, ok := decision.AtEpoch(l); ok {
				t.Errorf("invented time %s for %q", e, l)
			}
		}
		for _, l := range []string{"needs-decision [key=api-shape] [at=10:30]: choose: A or B", "needs-decision [at=10:30] [key=api-shape]: choose: A or B",
			"needs-decision [key=api-shape] [at=2026-09-20T14:03:00Z]: choose: A or B"} {
			if k, _ := decision.Key(l); k != "api-shape" || decision.Note(l) != "choose: A or B" {
				t.Errorf("a colon-bearing time moved the separator: key %q note %q from %q", k, decision.Note(l), l)
			}
		}
		for _, l := range []string{"done [at=1700000000] [corr=" + corr1 + "]: finished", "done [corr=" + corr1 + "] [at=1700000000]: finished",
			"done[at=1700000000] [corr=" + corr1 + "]: finished"} {
			if e, _ := decision.AtEpoch(l); e != "1700000000" {
				t.Errorf("metadata order changed time: %q", l)
			}
		}
		lines := []string{stamped, "working [at=1700000000]: unrelated progress"}
		if len(decision.Fold(lines, decision.KindUnknown, decision.Verbs{})) == 0 {
			t.Errorf("time cleared an open decision")
		}
		lines = append(lines, "resolved [at=1700000001] [key=timed]: answered")
		if open := decision.Fold(lines, decision.KindUnknown, decision.Verbs{}); len(open) != 0 {
			t.Errorf("timed resolution did not close decision: %+v", open)
		}
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
		override := "done:|needs-decision:|blocked:|failed:"
		for _, v := range []string{"done", "needs-decision", "blocked", "failed"} {
			for _, l := range []string{v + ": audit complete", v + " [at=1700000000]: audit complete", v + "[at=1700000000]: audit complete"} {
				if !decision.CaptainRelevant(l, override) {
					t.Errorf("override missed actionable event: %q", l)
				}
				if ev, _ := decision.Actionable([]string{l}, decision.KindUnknown, override); len(ev) != 1 || ev[0] != l {
					t.Errorf("override hid actionable status span or changed its bytes: %q -> %v", l, ev)
				}
			}
		}
		for _, l := range []string{"blocked: waiting", "blocked [at=1700000000]: waiting"} {
			if decision.CaptainRelevant(l, "done:") {
				t.Errorf("override admitted excluded event: %q", l)
			}
			if ev, _ := decision.Actionable([]string{l}, decision.KindUnknown, "done:"); len(ev) != 0 {
				t.Errorf("override surfaced excluded event: %q", l)
			}
		}
		for _, v := range []string{"working", "paused", "resolved", "captain-held"} {
			for _, l := range []string{v + ": done: mentioned", v + " [at=1700000000]: done: mentioned"} {
				if decision.CaptainRelevant(l, override) {
					t.Errorf("override bypassed nonterminal suppression: %q", l)
				}
			}
		}
		custom := "^custom-verb: audit complete$"
		for _, l := range []string{"custom-verb: audit complete", "custom-verb [at=1700000000]: audit complete", "custom-verb [at=<epoch>]: audit complete"} {
			if !decision.CaptainRelevant(l, custom) {
				t.Errorf("timestamp broke custom verb override: %q", l)
			}
			if got := decision.Latest([]string{"working: started", l}, custom); got != l {
				t.Errorf("event scan skipped the stamped custom-verb event: %q", got)
			}
		}
		literal := `^done \[corr=` + corr1 + `\]: literal \[at=1700000000\]$`
		for _, l := range []string{"done [corr=" + corr1 + "]: literal [at=1700000000]", "done [at=1700000000] [corr=" + corr1 + "]: literal [at=1700000000]",
			"done [corr=" + corr1 + "] [at=1700000000]: literal [at=1700000000]"} {
			if !decision.CaptainRelevant(l, literal) {
				t.Errorf("normalization changed correlation metadata or note: %q", l)
			}
		}
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
		for _, v := range []string{"done", "needs-decision", "blocked", "failed"} {
			for _, tag := range tags {
				l := v + " " + tag + ": audit complete"
				if e, ok := decision.AtEpoch(l); ok {
					t.Errorf("invented time %s for %q", e, l)
				}
				if decision.Verb(l) != v || decision.Note(l) != "audit complete" {
					t.Errorf("malformed time changed verb/note: %q %q from %q", decision.Verb(l), decision.Note(l), l)
				}
				if k, _ := decision.Key(l); k != decision.DefaultKey {
					t.Errorf("malformed time invented a decision key: %q from %q", k, l)
				}
				if !decision.CaptainRelevant(l, "") || !decision.CaptainRelevant(l, "done:|needs-decision:|blocked:|failed:") {
					t.Errorf("a malformed tag lost an actionable event: %q", l)
				}
				if ev, _ := decision.Actionable([]string{l}, decision.KindUnknown, ""); len(ev) != 1 || ev[0] != l {
					t.Errorf("default vocabulary hid actionable status span: %q -> %v", l, ev)
				}
				if got := decision.Latest([]string{l}, "done:|needs-decision:|blocked:|failed:"); got != l {
					t.Errorf("event scan lost a terminal event to a malformed tag: %q", l)
				}
			}
		}
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
		for _, tag := range []string{"[at=17:00]", "[at=10:30]", "[at=2026-09-20T14:03:00Z]", "[at=<epoch>]", "[at=bad]"} {
			wantFold(t, []string{"needs-decision [key=api-shape] [at=1700000000]: REST or gRPC?", "done " + tag + " finished the audit"},
				decision.KindShip, "api-shape\tneeds-decision\tREST or gRPC?", "malformed tag "+tag+" closed an open decision")
			wantFold(t, []string{"needs-decision " + tag + " which base branch"}, decision.KindShip, "", "malformed tag "+tag+" opened a phantom decision")
		}
		wantFold(t, []string{"needs-decision [key=api-shape] [at=1700000000]: REST or gRPC?", "done [at=1700000001]: finished the audit"},
			decision.KindShip, "", "a well-formed terminal event stopped closing the decision")
	})
}

func TestPortClassifyDecisionKey(t *testing.T) {
	const s = "FM/fm-classify-decision-key/"

	t.Run(s+"stated_key_is_honored_in_both_positions", func(t *testing.T) {
		// fm: tests/fm-classify-decision-key.test.sh:51
		// cox: decision fold
		wantUrgent(t, "needs-decision [key=api-shape]: pick REST or RPC")
		wantUrgent(t, "needs-decision: [key=api-shape] pick REST or RPC")
		want := "api-shape\tneeds-decision\tpick REST or RPC"
		wantFold(t, []string{"needs-decision [key=api-shape]: pick REST or RPC"}, decision.KindUnknown, want, "documented before-colon form")
		wantFold(t, []string{"needs-decision: [key=api-shape] pick REST or RPC"}, decision.KindUnknown, want, "colon-first form")
	})

	t.Run(s+"bare_keyless_line_still_folds_to_default", func(t *testing.T) {
		// fm: tests/fm-classify-decision-key.test.sh:70
		// cox: decision fold
		wantUrgent(t, "needs-decision: which color")
		wantRoutine(t, "resolved: went with blue")
		lines := []string{"needs-decision: which color"}
		wantFold(t, lines, decision.KindUnknown, "default\tneeds-decision\twhich color", "bare keyless line")
		wantFold(t, append(lines, "resolved: went with blue"), decision.KindUnknown, "", "bare keyless resolution")
	})

	t.Run(s+"resolution_closes_across_positions", func(t *testing.T) {
		// fm: tests/fm-classify-decision-key.test.sh:84
		// cox: decision fold
		wantUrgent(t, "needs-decision: [key=seam-max-bound] pick the bound")
		wantRoutine(t, "resolved [key=seam-max-bound]: answered: use 4")
		wantUrgent(t, "needs-decision [key=seam-max-bound]: pick the bound")
		wantRoutine(t, "resolved: [key=seam-max-bound] answered: use 4")
		wantFold(t, []string{"needs-decision: [key=seam-max-bound] pick the bound", "resolved [key=seam-max-bound]: answered: use 4"},
			decision.KindUnknown, "", "documented resolution closing a colon-first open")
		wantFold(t, []string{"needs-decision [key=seam-max-bound]: pick the bound", "resolved: [key=seam-max-bound] answered: use 4"},
			decision.KindUnknown, "", "colon-first resolution closing a documented open")
	})

	t.Run(s+"blocked_is_position_tolerant_like_needs_decision", func(t *testing.T) {
		// fm: tests/fm-classify-decision-key.test.sh:100
		// cox: decision fold
		wantUrgent(t, "blocked [key=creds]: waiting on the deploy token")
		wantUrgent(t, "blocked: [key=creds] waiting on the deploy token")
		want := "creds\tblocked\twaiting on the deploy token"
		wantFold(t, []string{"blocked [key=creds]: waiting on the deploy token"}, decision.KindUnknown, want, "documented blocked form")
		wantFold(t, []string{"blocked: [key=creds] waiting on the deploy token"}, decision.KindUnknown, want, "colon-first blocked form")
	})

	t.Run(s+"two_colon_form_decisions_stay_distinct", func(t *testing.T) {
		// fm: tests/fm-classify-decision-key.test.sh:111
		// cox: decision fold
		wantUrgent(t, "needs-decision: [key=alpha] first question")
		wantUrgent(t, "needs-decision: [key=beta] second question")
		wantRoutine(t, "resolved [key=alpha]: answered: yes")
		lines := []string{"needs-decision: [key=alpha] first question", "needs-decision: [key=beta] second question"}
		wantFold(t, lines, decision.KindUnknown, "alpha\tneeds-decision\tfirst question\nbeta\tneeds-decision\tsecond question", "two colon-form decisions")
		wantFold(t, append(lines, "resolved [key=alpha]: answered: yes"), decision.KindUnknown, "beta\tneeds-decision\tsecond question", "closing one of two colon-form decisions")
	})

	t.Run(s+"mid_note_prose_mention_is_not_a_stated_key", func(t *testing.T) {
		// fm: tests/fm-classify-decision-key.test.sh:128
		// cox: decision fold
		wantUrgent(t, "needs-decision: pick a [key=red] or [key=blue] theme")
		wantUrgent(t, "needs-decision [key=red]: which shade")
		wantRoutine(t, "working: still thinking about [key=red] here")
		lines := []string{"needs-decision: pick a [key=red] or [key=blue] theme"}
		wantFold(t, lines, decision.KindUnknown, "default\tneeds-decision\tpick a [key=red] or [key=blue] theme", "mid-note prose mention")
		lines = append(lines, "needs-decision [key=red]: which shade", "working: still thinking about [key=red] here")
		wantFold(t, lines, decision.KindUnknown, "default\tneeds-decision\tpick a [key=red] or [key=blue] theme\nred\tneeds-decision\twhich shade", "prose mention leaves the open set untouched")
	})

	t.Run(s+"malformed_stated_key_never_collapses_to_default", func(t *testing.T) {
		// fm: tests/fm-classify-decision-key.test.sh:146
		// cox: decision fold
		wantFold(t, []string{"needs-decision [key=bad key]: before-colon malformed"}, decision.KindUnknown, "", "malformed before-colon key")
		wantFold(t, []string{"needs-decision: [key=bad key] colon-first malformed"}, decision.KindUnknown, "", "malformed colon-first key")
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
		lines := []string{"needs-decision [corr=d448ea86afa4bf67] [key=loan-installment-cadence-amount]: pick the cadence"}
		wantFold(t, lines, decision.KindUnknown, "loan-installment-cadence-amount\tneeds-decision\tpick the cadence", "corr-then-key opens under the stated key")
		wantFold(t, append(lines, "resolved [corr=d448ea86afa4bf67] [key=loan-installment-cadence-amount]: answered: monthly"),
			decision.KindUnknown, "", "corr-then-key resolution closes the same stated key")
	})

	t.Run(s+"corr_only_tag_opens_as_default_like_a_bare_line", func(t *testing.T) {
		// fm: tests/fm-classify-decision-key.test.sh:201
		// cox: decision fold
		if a, b := statusKind("needs-decision: which vendor"), statusKind("needs-decision [corr=d448ea86afa4bf67]: which vendor"); a != b {
			t.Errorf("a corr-only tag classified differently than the bare line: %s vs %s", b, a)
		}
		wantUrgent(t, "needs-decision [corr=d448ea86afa4bf67]: which vendor")
		bare := foldTSV([]string{"needs-decision: which vendor"}, decision.KindUnknown, decision.Verbs{})
		if corred := foldTSV([]string{"needs-decision [corr=d448ea86afa4bf67]: which vendor"}, decision.KindUnknown, decision.Verbs{}); corred != bare {
			t.Errorf("a corr-only tag folded differently than the bare line: %q vs %q", corred, bare)
		}
		wantFold(t, []string{"needs-decision [corr=d448ea86afa4bf67]: which vendor"}, decision.KindUnknown, "default\tneeds-decision\twhich vendor", "corr-only tag")
	})

	t.Run(s+"key_only_before_colon_still_opens_no_regression", func(t *testing.T) {
		// fm: tests/fm-classify-decision-key.test.sh:215
		// cox: decision fold
		wantUrgent(t, "needs-decision [key=loan-installment-cadence-amount]: pick the cadence")
		wantFold(t, []string{"needs-decision [key=loan-installment-cadence-amount]: pick the cadence"}, decision.KindUnknown,
			"loan-installment-cadence-amount\tneeds-decision\tpick the cadence", "key-only before colon, no corr tag")
	})

	t.Run(s+"blocked_and_resolved_are_tag_order_independent", func(t *testing.T) {
		// fm: tests/fm-classify-decision-key.test.sh:225
		// cox: decision fold
		wantUrgent(t, "blocked [corr=aaaa1111bbbb2222] [key=creds]: waiting on the deploy token")
		wantUrgent(t, "blocked [key=creds] [corr=aaaa1111bbbb2222]: waiting on the deploy token")
		wantRoutine(t, "resolved [corr=aaaa1111bbbb2222] [key=creds]: answered: rotated")
		want := "creds\tblocked\twaiting on the deploy token"
		wantFold(t, []string{"blocked [corr=aaaa1111bbbb2222] [key=creds]: waiting on the deploy token"}, decision.KindUnknown, want, "blocked corr-then-key")
		wantFold(t, []string{"blocked [key=creds] [corr=aaaa1111bbbb2222]: waiting on the deploy token"}, decision.KindUnknown, want, "blocked key-then-corr")
		wantFold(t, []string{"blocked [corr=aaaa1111bbbb2222] [key=creds]: waiting on the deploy token", "resolved [corr=aaaa1111bbbb2222] [key=creds]: answered: rotated"},
			decision.KindUnknown, "", "blocked/resolved corr+key close together regardless of tag order")
	})

	t.Run(s+"incremental_agrees_with_full_fold_across_appends", func(t *testing.T) {
		// fm: tests/fm-classify-decision-key.test.sh:242
		// cox: decision fold
		wantUrgent(t, "needs-decision: [key=seam-max-bound] pick the bound")
		wantRoutine(t, "working: routine progress note")
		wantUrgent(t, "needs-decision: [key=other] a second colon-form question")
		lines := []string{"needs-decision: [key=seam-max-bound] pick the bound"}
		wantFold(t, lines, decision.KindUnknown, "seam-max-bound\tneeds-decision\tpick the bound", "colon-first open, first read")
		lines = append(lines, "working: routine progress note", "needs-decision: [key=other] a second colon-form question")
		wantFold(t, lines, decision.KindUnknown, "seam-max-bound\tneeds-decision\tpick the bound\nother\tneeds-decision\ta second colon-form question", "colon-first opens buried under later appends")
		lines = append(lines, "resolved [key=seam-max-bound]: answered: use 4", "resolved: [key=other] cleared on its own")
		wantFold(t, lines, decision.KindUnknown, "", "cross-position resolutions close both")
	})

	t.Run(s+"closing_verb_separates_resolution_from_durable_transfer", func(t *testing.T) {
		// fm: tests/fm-classify-decision-key.test.sh:285
		// cox: per-key closing verb
		lines := []string{"working: started", "needs-decision [key=route]: north or south", "resolved [key=route]: answered: north",
			"needs-decision [key=access]: open or restricted", "captain-held [key=access]: tracked by sample-access-call",
			"blocked [key=creds]: need the deploy token", "done: everything else shipped"}
		for key, want := range map[string]string{"route": "resolved", "access": "captain-held", "creds": "blocked", "never-mentioned": ""} {
			if got := decision.ClosingVerb(lines, key, decision.KindUnknown, decision.Verbs{}); got != want {
				t.Errorf("closing verb of %s = %q, want %q", key, got, want)
			}
		}
		if got := decision.ClosingVerb(nil, "route", decision.KindUnknown, decision.Verbs{}); got != "" {
			t.Errorf("an absent status history reported a verb: %q", got)
		}
	})

	t.Run(s+"closing_verb_tracks_the_last_transition_in_both_positions", func(t *testing.T) {
		// fm: tests/fm-classify-decision-key.test.sh:314
		// cox: per-key closing verb
		lines := []string{"needs-decision: [key=route] colon-first open", "resolved: [key=route] colon-first close"}
		steps := []struct{ add, want string }{
			{"", "resolved"},
			{"needs-decision [key=route]: re-opened after a bad answer", "needs-decision"},
			{"resolved [key=route]: answered: south after all", "resolved"},
			{"working: a later append that only mentions [key=route] as prose", "resolved"},
		}
		for _, s := range steps {
			if s.add != "" {
				lines = append(lines, s.add)
			}
			if got := decision.ClosingVerb(lines, "route", decision.KindUnknown, decision.Verbs{}); got != s.want {
				t.Errorf("after %q the closing verb = %q, want %q", s.add, got, s.want)
			}
		}
	})

	t.Run(s+"closing_verb_honors_overridden_transition_verbs", func(t *testing.T) {
		// fm: tests/fm-classify-decision-key.test.sh:345
		// cox: per-key closing verb
		lines := []string{"blocked [key=route]: waiting"}
		for i := 0; i < 200; i++ {
			lines = append(lines, "note: routine reply", "working: still going", "Continuation prose here.")
		}
		lines = append(lines, "answered [key=route]: settled")
		if got := decision.ClosingVerb(lines, "route", decision.KindShip, decision.Verbs{Resolve: "answered"}); got != "answered" {
			t.Errorf("an overridden resolve verb stopped closing its key: %q", got)
		}
		if got := decision.ClosingVerb(lines, "route", decision.KindShip, decision.Verbs{}); got != "blocked" {
			t.Errorf("without the override the same line must leave the key open: %q", got)
		}
		lines = append(lines, "blocked [key=access]: waiting", "awaiting-captain [key=access]: handed off")
		if got := decision.ClosingVerb(lines, "access", decision.KindShip, decision.Verbs{Held: "awaiting-captain"}); got != "awaiting-captain" {
			t.Errorf("an overridden durable-transfer verb stopped closing its key: %q", got)
		}
	})

	t.Run(s+"closing_verb_filters_unrelated_history_without_subshell_growth", func(t *testing.T) {
		// fm: tests/fm-classify-decision-key.test.sh:365
		// cox: per-key closing verb (the bash subshell-growth bound has no Go analog; the retained-resolution half does)
		wantRoutine(t, "working: mentions [key=route] in prose")
		for _, want := range []string{"route", "default"} {
			tag := "[key=" + want + "]"
			if want == decision.DefaultKey {
				tag = ""
			}
			for _, size := range []int{1, 1000} {
				lines := []string{"blocked corr=0123456789abcdef " + tag + ": waiting"}
				for i := 0; i < size; i++ {
					lines = append(lines, "note: routine reply", "working: mentions [key="+want+"] in prose", "done: another task finished",
						"failed: unrelated work", "PR ready https://example.com/pull/1", "")
					if want != decision.DefaultKey {
						lines = append(lines, "blocked [key=other]: another question", "resolved [key=other]: answered")
					}
				}
				lines = append(lines, "resolved corr=0123456789abcdef: "+tag+" answered", "note: cleanup complete")
				if got := decision.ClosingVerb(lines, want, decision.KindSecondmate, decision.Verbs{}); got != "resolved" {
					t.Errorf("%s (size %d) lost its resolution behind unrelated history: %q", want, size, got)
				}
			}
		}
	})

	t.Run(s+"closing_verb_filter_preserves_terminal_chronology", func(t *testing.T) {
		// fm: tests/fm-classify-decision-key.test.sh:398
		// cox: per-key closing verb
		wantKind(t, "done: report saved", KindWorkerDone)
		for _, kind := range []decision.Kind{decision.KindShip, decision.KindScout, decision.KindSecondmate} {
			for _, want := range []string{"access", "default"} {
				tag := "[key=" + want + "]"
				if want == decision.DefaultKey {
					tag = ""
				}
				for _, terminal := range []string{"done", "failed"} {
					lines := []string{"blocked " + tag + ": waiting"}
					if terminal == "done" {
						lines = append(lines, "done: report saved")
					} else {
						lines = append(lines, "failed corr=0123456789abcdef [key=other]: task failed")
					}
					lines = append(lines, "note: cleanup complete")
					expected := terminal
					if kind == decision.KindSecondmate {
						expected = "blocked"
					}
					if got := decision.ClosingVerb(lines, want, kind, decision.Verbs{}); got != expected {
						t.Errorf("%s/%s lost %s chronology: %q", kind, want, terminal, got)
					}
					lines = append(lines, "needs-decision: [key="+want+"] reopened", "note: more cleanup")
					if got := decision.ClosingVerb(lines, want, kind, decision.Verbs{}); got != "needs-decision" {
						t.Errorf("%s/%s lost a post-terminal reopening: %q", kind, want, got)
					}
				}
			}
		}
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
		for _, kind := range []decision.Kind{decision.KindShip, decision.KindScout} {
			for _, word := range []string{"done", "failed"} {
				prose := []string{"needs-decision [key=route]: A or B?", "paused: waiting on the vendor", "Steps remaining:", " " + word}
				wantFold(t, prose, kind, "route\tneeds-decision\tA or B?", string(kind)+": bare '"+word+"' prose")
				if got := decision.ClosingVerb(prose, "route", kind, decision.Verbs{}); got != "needs-decision" {
					t.Errorf("%s: bare %q prose closed a still-open key: %q", kind, word, got)
				}
				real := []string{"needs-decision [key=route]: A or B?", word + ": real outcome"}
				wantFold(t, real, kind, "", string(kind)+": genuine "+word+" supersedes")
				if got := decision.ClosingVerb(real, "route", kind, decision.Verbs{}); got != word {
					t.Errorf("%s: genuine %s no longer supersedes the open key: %q", kind, word, got)
				}
			}
		}
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
		for _, word := range []string{"blocked", "needs-decision", "resolved"} {
			wantFold(t, []string{"working: investigating the deploy", "Options considered:", " " + word}, decision.KindShip, "", "bare '"+word+"' prose opened a decision")
			wantFold(t, []string{"blocked: need release access", "Steps remaining:", " " + word}, decision.KindShip, "default\tblocked\tneed release access", "bare '"+word+"' prose moved an open decision")
		}
		keyed := []string{"blocked [key=access]"}
		wantFold(t, keyed, decision.KindShip, "access\tblocked\tblocked [key=access]", "a keyed colonless line stopped opening its key")
		wantFold(t, append(keyed, "resolved [key=access]"), decision.KindShip, "", "a keyed colonless line stopped closing its key")
		wantFold(t, []string{"blocked: need release access", "resolved: access granted"}, decision.KindShip, "", "a genuine resolution stopped closing its decision")
	})
}
