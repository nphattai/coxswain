package lavish

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nphattai/coxswain/internal/protocol/inbox"
	"github.com/nphattai/coxswain/internal/wake"
)

// fakeLavish writes an executable stand-in for lavish-axi that ignores its args and prints toon on stdout, and returns
// its path (used as Config.Binary). A heredoc with a quoted delimiter keeps the TOON verbatim.
func fakeLavish(t *testing.T, toon string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "lavish-axi")
	script := "#!/bin/sh\ncat <<'LAVISH_EOF'\n" + toon + "\nLAVISH_EOF\n"
	if err := os.WriteFile(p, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func poll(t *testing.T, epic, toon string) (PollResult, error) {
	t.Helper()
	cfg := Config{Binary: fakeLavish(t, toon)}
	var out, errb bytes.Buffer
	return Poll(cfg, epic, filepath.Join(epic, "reports", "visual", "arena.html"), 500*time.Millisecond, nil, &out, &errb)
}

func leaderRecords(t *testing.T, epic string) []inbox.Record {
	t.Helper()
	recs, err := inbox.All(epic, LeaderStory)
	if err != nil {
		t.Fatal(err)
	}
	return recs
}

func unacked(t *testing.T, epic string) []wake.Wake {
	t.Helper()
	w, err := wake.Drain(epic, true)
	if err != nil {
		t.Fatal(err)
	}
	return w
}

const feedbackTOON = `session:
  file: /x/arena.html
  status: feedback
prompts[2]:
  - tag: comment
    selector: #s1
    text: this line
    prompt: make it clearer
  - tag: answer
    selector: #claim-reviewer-1-1
    prompt: answer reviewer-1-1 yes
    claim_id: reviewer-1-1`

func TestPollFeedbackRecordsAndWakes(t *testing.T) {
	epic := t.TempDir()
	res, err := poll(t, epic, feedbackTOON)
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != ExitFeedback || len(res.Records) != 2 {
		t.Fatalf("result: %+v", res)
	}
	recs := leaderRecords(t, epic)
	if len(recs) != 2 {
		t.Fatalf("want 2 inbox records, got %d", len(recs))
	}
	for _, r := range recs {
		if r.Urgency != inbox.FYI {
			t.Errorf("review feedback must be fyi, got %q", r.Urgency)
		}
	}
	if !strings.Contains(recs[0].Body, "anchor=#s1") || !strings.Contains(recs[0].Body, "quoted=this line") || !strings.Contains(recs[0].Body, "message=make it clearer") {
		t.Errorf("comment body missing fields:\n%s", recs[0].Body)
	}
	// Two wakes: a routine comment and an urgent answer.
	ws := unacked(t, epic)
	if len(ws) != 2 {
		t.Fatalf("want 2 wakes, got %d", len(ws))
	}
	kinds := map[wake.Kind]bool{ws[0].Kind: true, ws[1].Kind: true}
	if !kinds[wake.KindReviewFeedback] || !kinds[wake.KindReviewDecision] {
		t.Fatalf("want a review_feedback and a review_decision wake, got %v", kinds)
	}
}

func TestPollEnded(t *testing.T) {
	epic := t.TempDir()
	res, err := poll(t, epic, "session:\n  file: /x/arena.html\n  status: ended\n  ended_by: user")
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != ExitEnded {
		t.Fatalf("exit = %d, want %d", res.ExitCode, ExitEnded)
	}
	if len(leaderRecords(t, epic)) != 1 || len(unacked(t, epic)) != 1 {
		t.Fatalf("ended should write one terminal record and one routine wake")
	}
	if unacked(t, epic)[0].Kind != wake.KindReviewFeedback {
		t.Errorf("ended wake should be routine review_feedback")
	}
}

func TestPollBrowserDisconnected(t *testing.T) {
	epic := t.TempDir()
	res, err := poll(t, epic, "session:\n  file: /x/arena.html\n  status: browser_disconnected")
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != ExitDisconnected {
		t.Fatalf("exit = %d, want %d", res.ExitCode, ExitDisconnected)
	}
}

func TestPollTimeout(t *testing.T) {
	epic := t.TempDir()
	res, err := poll(t, epic, "session:\n  file: /x/arena.html\n  status: waiting")
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != ExitTimeout || res.Status != "timeout" {
		t.Fatalf("result: %+v", res)
	}
}

func TestPollSendAndEnd(t *testing.T) {
	epic := t.TempDir()
	toon := "session:\n  file: /x/arena.html\n  status: feedback\n  session_ended: true\n  ended_by: user\n" +
		"prompts[1]:\n  - tag: comment\n    selector: #s1\n    prompt: last note"
	res, err := poll(t, epic, toon)
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != ExitEnded {
		t.Fatalf("Send & End must stop callers from re-arming, exit = %d", res.ExitCode)
	}
	// One feedback record plus one end record.
	if got := len(leaderRecords(t, epic)); got != 2 {
		t.Fatalf("want feedback + end records, got %d", got)
	}
}

func TestPollFeedbackAndBrowserDisconnectReturnsEnded(t *testing.T) {
	epic := t.TempDir()
	toon := "session:\n  file: /x/arena.html\n  status: feedback\n  session_ended: true\n  ended_by: browser_disconnected\n" +
		"prompts[1]:\n  - tag: comment\n    selector: #s1\n    prompt: last note"
	res, err := poll(t, epic, toon)
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != ExitEnded {
		t.Fatalf("final feedback after disconnect must return ended, exit = %d", res.ExitCode)
	}
	if got := len(leaderRecords(t, epic)); got != 2 {
		t.Fatalf("want feedback + disconnect records, got %d", got)
	}
}

// TestPollFaultInjection is the arena verification gate: lavish delivered feedback (and cleared its queue) but the
// record write fails. Poll must return an error and surface the raw feedback so the loss is reported, never silent.
func TestPollFaultInjection(t *testing.T) {
	epic := t.TempDir()
	// Make the _leader inbox unwritable by planting a file where the inbox directory must be created.
	if err := os.MkdirAll(filepath.Join(epic, "inbox"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(inbox.Dir(epic, LeaderStory), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := Config{Binary: fakeLavish(t, feedbackTOON)}
	var out, errb bytes.Buffer
	_, err := Poll(cfg, epic, "/x/arena.html", 500*time.Millisecond, nil, &out, &errb)
	if err == nil {
		t.Fatal("expected a hard error when the record cannot be written (feedback is lost)")
	}
	if !strings.Contains(out.String(), "LOST") || !strings.Contains(out.String(), "make it clearer") {
		t.Fatalf("loss must be reported with the raw feedback, got:\n%s", out.String())
	}
}

func TestAfterDeliverSeamRunsBeforeRecord(t *testing.T) {
	epic := t.TempDir()
	cfg := Config{Binary: fakeLavish(t, feedbackTOON)}
	ran := false
	var out, errb bytes.Buffer
	_, err := Poll(cfg, epic, "/x/arena.html", 500*time.Millisecond, func() { ran = true }, &out, &errb)
	if err != nil || !ran {
		t.Fatalf("afterDeliver seam should run once: ran=%v err=%v", ran, err)
	}
}

func TestPollUnavailablePrintsGuidance(t *testing.T) {
	t.Setenv("PATH", filepath.Join(t.TempDir(), "empty"))
	epic := t.TempDir()
	var out, errb bytes.Buffer
	res, err := Poll(Config{}, epic, "/x/arena.html", 100*time.Millisecond, nil, &out, &errb)
	if err != nil || res.ExitCode != ExitFeedback {
		t.Fatalf("unavailable poll should degrade, not fail: %+v %v", res, err)
	}
	if !strings.Contains(out.String(), "not available") {
		t.Fatalf("expected guidance, got: %s", out.String())
	}
}

func TestOpenPrintsLoopbackURLAndDeliveryReminder(t *testing.T) {
	toon := "session:\n  file: /x/arena.html\n  url: http://captain.tailnet.ts.net:4387/session/abc123\n  status: opened"
	var out, errb bytes.Buffer
	if err := Open(Config{Binary: fakeLavish(t, toon)}, "/x/arena.html", &out, &errb); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	if !strings.Contains(got, "local: http://127.0.0.1:4387/session/abc123") {
		t.Fatalf("loopback session URL missing:\n%s", got)
	}
	if !strings.Contains(got, "feedback is delivered only after Send to Agent in the page") {
		t.Fatalf("delivery reminder missing:\n%s", got)
	}
}

func TestLocalSessionURLRejectsIncompleteOutput(t *testing.T) {
	for _, raw := range []string{
		"session:\n  url: http://host/session/id",
		"session:\n  url: http://host:4387/not-a-session/id",
		"error: nope",
	} {
		if got := localSessionURL(raw); got != "" {
			t.Fatalf("localSessionURL(%q) = %q", raw, got)
		}
	}
}

func TestReplyPassesAgentReplyAndRecordsNextFeedback(t *testing.T) {
	epic := t.TempDir()
	argsFile := filepath.Join(t.TempDir(), "args")
	bin := filepath.Join(t.TempDir(), "lavish-axi")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > '" + argsFile + "'\ncat <<'LAVISH_EOF'\n" + feedbackTOON + "\nLAVISH_EOF\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	var out, errb bytes.Buffer
	res, err := Reply(Config{Binary: bin}, epic, "/x/arena.html", "clarified in chat", 500*time.Millisecond, &out, &errb)
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != ExitFeedback || len(res.Records) != 2 || len(unacked(t, epic)) != 2 {
		t.Fatalf("reply must retain poll record and wake behavior: %+v", res)
	}
	args, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Split(strings.TrimSpace(string(args)), "\n")
	want := []string{"poll", "/x/arena.html", "--timeout-ms", "500", "--agent-reply", "clarified in chat"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("lavish args = %q, want %q", got, want)
	}
}
