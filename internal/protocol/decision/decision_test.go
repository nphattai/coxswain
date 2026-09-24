package decision

import (
	"reflect"
	"testing"
)

func TestGrammar(t *testing.T) {
	const c = "c44897ee2db4326b"
	cases := []struct {
		line, verb, key, note string
		keyOK                 bool
	}{
		{"needs-decision [key=api-shape]: pick REST or RPC", "needs-decision", "api-shape", "pick REST or RPC", true},
		{"needs-decision: [key=api-shape] pick REST or RPC", "needs-decision", "api-shape", "pick REST or RPC", true},
		{"needs-decision: which color", "needs-decision", "default", "which color", true},
		{"needs-decision: pick a [key=red] or [key=blue] theme", "needs-decision", "default", "pick a [key=red] or [key=blue] theme", true},
		{"needs-decision [key=bad key]: x", "needs-decision", "", "x", false},
		{"needs-decision corr=" + c + " [key=k]: note", "needs-decision", "k", "note", true},
		{"needs-decision [key=a] corr=" + c + ": note", "needs-decision", "a", "note", true},
		{"needs-decision [corr=" + c + "] [key=k]: n", "needs-decision", "k", "n", true},
		{"corr=" + c + " blocked [key=k]: prose", "corr=" + c + " blocked", "k", "prose", true},
		{"resolved corr=deadbeef [key=v]: x", "resolved corr=deadbeef", "v", "x", true},
		{"done [at=17:00]: audit complete", "done", "default", "audit complete", true},
		{"done [at=1] [at=2]: audit complete", "done", "default", "audit complete", true},
		{"blocked [key=access]", "blocked", "access", "blocked [key=access]", true},
	}
	for _, tc := range cases {
		if got := Verb(tc.line); got != tc.verb {
			t.Errorf("Verb(%q) = %q, want %q", tc.line, got, tc.verb)
		}
		k, ok := Key(tc.line)
		if ok != tc.keyOK || (ok && k != tc.key) {
			t.Errorf("Key(%q) = %q,%v want %q,%v", tc.line, k, ok, tc.key, tc.keyOK)
		}
		if got := Note(tc.line); got != tc.note {
			t.Errorf("Note(%q) = %q, want %q", tc.line, got, tc.note)
		}
	}
}

func TestAtEpoch(t *testing.T) {
	for _, l := range []string{"x [at=1700000000]: y", "x [key=k] [at=0]: y"} {
		if _, ok := AtEpoch(l); !ok {
			t.Errorf("AtEpoch(%q) unknown, want known", l)
		}
	}
	for _, l := range []string{"x [at=]: y", "x [at=bad]: y", "x [at=17:00]: y", "x [at=1] [at=2]: y",
		"x [at=01700000000]: y", "x [at=99999999999999999999]: y", "x: [at=1700000000] y", "x [at=1700000000]"} {
		if e, ok := AtEpoch(l); ok {
			t.Errorf("AtEpoch(%q) invented %s", l, e)
		}
	}
}

func TestFold(t *testing.T) {
	lines := []string{
		"needs-decision: [key=seam] pick the bound",
		"working: routine progress note",
		"needs-decision: [key=other] a second question",
		"blocked: need access",
	}
	want := []Decision{
		{"seam", "needs-decision", "pick the bound", 1},
		{"other", "needs-decision", "a second question", 3},
		{"default", "blocked", "need access", 4},
	}
	if got := Fold(lines, KindUnknown, Verbs{}); !reflect.DeepEqual(got, want) {
		t.Fatalf("Fold = %+v\nwant %+v", got, want)
	}
	lines = append(lines, "resolved [key=seam]: use 4", "resolved: [key=other] cleared", "resolved: done")
	if got := Fold(lines, KindUnknown, Verbs{}); len(got) != 0 {
		t.Fatalf("resolutions left %+v open", got)
	}
	// Terminal supersession applies to ship and scout only.
	term := []string{"blocked [key=access]: waiting", "done: report saved", "note: cleanup"}
	for kind, open := range map[Kind]int{KindShip: 0, KindScout: 0, KindSecondmate: 1, KindUnknown: 1} {
		if got := Fold(term, kind, Verbs{}); len(got) != open {
			t.Errorf("kind %s: %d open after done, want %d", kind, len(got), open)
		}
	}
	// A malformed time tag never supplies the separator the line lacks.
	if got := Fold([]string{"needs-decision [key=a]: q", "done [at=17:00] finished"}, KindShip, Verbs{}); len(got) != 1 {
		t.Errorf("colonless done closed the fold: %+v", got)
	}
	// Reserved keys move only through their own vocabulary.
	if got := Fold([]string{"needs-decision [key=pending-reply-x]: hijack"}, KindUnknown, Verbs{}); len(got) != 0 {
		t.Errorf("foreign writer opened a reserved key: %+v", got)
	}
}

func TestClosingVerb(t *testing.T) {
	lines := []string{
		"working: started",
		"needs-decision [key=route]: north or south",
		"resolved [key=route]: answered: north",
		"needs-decision [key=access]: open or restricted",
		"captain-held [key=access]: tracked",
		"blocked [key=creds]: need the deploy token",
		"done: everything else shipped",
	}
	for key, want := range map[string]string{"route": "resolved", "access": "captain-held", "creds": "blocked", "never": ""} {
		if got := ClosingVerb(lines, key, KindUnknown, Verbs{}); got != want {
			t.Errorf("ClosingVerb(%s) = %q, want %q", key, got, want)
		}
	}
	over := []string{"blocked [key=route]: waiting", "answered [key=route]: settled"}
	if got := ClosingVerb(over, "route", KindShip, Verbs{Resolve: "answered"}); got != "answered" {
		t.Errorf("overridden resolve verb: %q", got)
	}
	if got := ClosingVerb(over, "route", KindShip, Verbs{}); got != "blocked" {
		t.Errorf("without the override: %q", got)
	}
}

func TestCaptainRelevant(t *testing.T) {
	for _, l := range []string{"done: x", "needs-decision [key=q]: x", "failed: x", "merged", "PR ready for review", "done [at=bad]: x"} {
		if !CaptainRelevant(l, "") {
			t.Errorf("%q not captain-relevant", l)
		}
	}
	for _, l := range []string{"working: rebased onto merged #76", "resolved [key=q]: done: x", "paused: done: x", "note: hi"} {
		if CaptainRelevant(l, "") {
			t.Errorf("%q captain-relevant", l)
		}
	}
	if CaptainRelevant("blocked: waiting", "done:") {
		t.Error("override admitted an excluded verb")
	}
	if !CaptainRelevant("custom-verb [at=1700000000]: audit complete", "^custom-verb: audit complete$") {
		t.Error("stamp broke a custom override")
	}
}

func TestActionable(t *testing.T) {
	lines := []string{"needs-decision [key=api]: pick A or B", "resolved [key=api]: A", "working: more", "failed: boom"}
	ev, nd := Actionable(lines, KindUnknown, "")
	if !reflect.DeepEqual(ev, []string{"failed: boom"}) || nd {
		t.Errorf("Actionable = %v %v", ev, nd)
	}
	ev, nd = Actionable([]string{"needs-decision: open", "working: x"}, KindUnknown, "")
	if len(ev) != 1 || !nd {
		t.Errorf("open decision not actionable: %v %v", ev, nd)
	}
}

func TestLatest(t *testing.T) {
	if got := Latest([]string{"working: a", "Steps remaining:", " done", ""}, ""); got != "working: a" {
		t.Errorf("Latest = %q", got)
	}
	if got := Latest([]string{"prose only"}, ""); got != "prose only" {
		t.Errorf("fallback Latest = %q", got)
	}
}
