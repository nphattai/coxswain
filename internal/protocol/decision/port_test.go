package decision

import (
	"fmt"
	"reflect"
	"testing"
)

// TestPortClassifyDecisionKey translates the c6e816f cases of tests/fm-classify-decision-key.test.sh (the keyed
// activity fold and the declared-wait read); the earlier cases of that suite are in internal/wake/port_classify_test.go.
func TestPortClassifyDecisionKey(t *testing.T) {
	const s = "FM/fm-classify-decision-key/"
	acts := func(lines ...string) []Decision { return OpenActivities(lines, Verbs{}) }
	paused := func(key, note string, line int) []Decision { return []Decision{{key, "paused", note, line}} }

	t.Run(s+"keyless_wait_survives_stated_default_retraction", func(t *testing.T) {
		// fm: tests/fm-classify-decision-key.test.sh:491@a8572f6
		f := []string{"needs-decision: which color", "paused: waiting on the vendor release"}
		if got := Fold(f, KindUnknown, Verbs{}); len(got) != 1 || got[0].Key != DefaultKey || got[0].Verb != "needs-decision" {
			t.Errorf("keyless decision did not stay open beside the wait: %+v", got)
		}
		if got := acts(f...); !reflect.DeepEqual(got, paused(DefaultKey, "waiting on the vendor release", 2)) {
			t.Errorf("keyless pause did not open as its own default phase: %+v", got)
		}
		f = append(f, "resolved [key=default]: answered: blue")
		if got := Fold(f, KindUnknown, Verbs{}); len(got) != 0 {
			t.Errorf("stated default retraction did not close the keyless decision: %+v", got)
		}
		if got := acts(f...); !reflect.DeepEqual(got, paused(DefaultKey, "waiting on the vendor release", 2)) {
			t.Errorf("stated default retraction cancelled the unrelated keyless wait: %+v", got)
		}
		if got := acts("paused: waiting on the vendor release", "resolved: [key=default] answered: blue"); !reflect.DeepEqual(got, paused(DefaultKey, "waiting on the vendor release", 1)) {
			t.Errorf("a colon-first stated default retraction cancelled the keyless wait: %+v", got)
		}
		if got := acts("paused: waiting on the vendor release", "resolved: the vendor shipped"); len(got) != 0 {
			t.Errorf("a keyless self-retraction left the keyless wait open: %+v", got)
		}
		if got := acts("paused [key=legal]: awaiting counsel", "resolved [key=default]: answered: blue", "resolved: unrelated keyless close"); !reflect.DeepEqual(got, paused("legal", "awaiting counsel", 1)) {
			t.Errorf("a default or keyless retraction closed a keyed wait: %+v", got)
		}
		if got := acts("paused [key=default]: named default wait", "resolved [key=default]: that wait cleared"); len(got) != 0 {
			t.Errorf("a stated default retraction did not close the stated default wait: %+v", got)
		}
		if got := acts("working: legacy start", "done: legacy completion"); len(got) != 0 {
			t.Errorf("a keyless terminal stopped superseding the keyless working phase: %+v", got)
		}
		if got := acts("paused: waiting on the vendor release", "needs-decision [key=default]: which color"); !reflect.DeepEqual(got, paused(DefaultKey, "waiting on the vendor release", 1)) {
			t.Errorf("a stated default decision cancelled the keyless wait: %+v", got)
		}
	})

	t.Run(s+"declared_wait_survives_answers_past_the_event_window", func(t *testing.T) {
		// fm: tests/fm-classify-decision-key.test.sh:542@a8572f6 (FM_CLASSIFY_EVENT_WINDOW_LINES defaults to 200)
		f := []string{"needs-decision: which color", "paused: waiting on the vendor release"}
		for i := 0; i <= 200; i++ {
			f = append(f, fmt.Sprintf("resolved [key=q%d]: answered", i))
		}
		f = append(f, "resolved [key=default]: answered: blue")
		if got := DeclaredWait(f, Verbs{}, ""); got != "paused: waiting on the vendor release" {
			t.Errorf("answers past the event window cancelled the wait: %q", got)
		}
		f = append(f, "resolved: the vendor shipped")
		if got := DeclaredWait(f, Verbs{}, ""); got != "" {
			t.Errorf("the worker's own keyless resolved line did not retract the wait past the window: %q", got)
		}
	})
}

// DeclaredWait's remaining firstmate rules: a standing pause or hold is the latest event itself; any non-resolve
// event after a pause ends it; a captain-held line counts only while it is the latest event.
func TestDeclaredWait(t *testing.T) {
	for _, tc := range []struct {
		lines []string
		want  string
	}{
		{[]string{"working: x", "paused: vendor"}, "paused: vendor"},
		{[]string{"captain-held: which window"}, "captain-held: which window"},
		{[]string{"captain-held: which window", "resolved [key=q1]: answered"}, ""},
		{[]string{"paused: vendor", "working: back on it", "resolved [key=q1]: answered"}, ""},
		{[]string{"paused [key=legal]: counsel", "resolved [key=legal]: back"}, ""},
		{[]string{"paused [key=legal]: counsel", "resolved: keyless answer"}, "paused [key=legal]: counsel"},
		{[]string{"working: x"}, ""},
		{nil, ""},
	} {
		if got := DeclaredWait(tc.lines, Verbs{}, ""); got != tc.want {
			t.Errorf("DeclaredWait(%q) = %q, want %q", tc.lines, got, tc.want)
		}
	}
}
