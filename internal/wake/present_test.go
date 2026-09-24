package wake

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nphattai/coxswain/internal/protocol/status"
)

func present(t *testing.T, epic string, opts PresentOptions) (string, string) {
	t.Helper()
	var out, errOut strings.Builder
	if err := Present(epic, &out, &errOut, opts); err != nil {
		t.Fatal(err)
	}
	return out.String(), errOut.String()
}

func logStatus(t *testing.T, epic, story, phase, note string) {
	t.Helper()
	if err := status.Report(epic, story, 1, phase, note, nil, ""); err != nil {
		t.Fatal(err)
	}
}

func TestPresentOpenDecisionsFoldAcrossHistory(t *testing.T) {
	epic := t.TempDir()
	logStatus(t, epic, "a", "status", "needs-decision [key=api]: pick REST or RPC")
	logStatus(t, epic, "a", "status", "working: more")
	logStatus(t, epic, "b", "stuck", "no deploy token")
	out, _ := present(t, epic, PresentOptions{})
	for _, want := range []string{"OPEN DECISIONS (still open", "a [key=api] needs-decision: pick REST or RPC", "b blocked: no deploy token", "OPEN DECISIONS: close one"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	logStatus(t, epic, "a", "status", "resolved [key=api]: REST")
	logStatus(t, epic, "b", "status", "resolved: token rotated")
	if out, _ := present(t, epic, PresentOptions{}); strings.Contains(out, "OPEN DECISIONS") {
		t.Errorf("resolved decisions still open:\n%s", out)
	}
}

func TestPresentBackstopReceiptsAndCoverage(t *testing.T) {
	epic := t.TempDir()
	logStatus(t, epic, "s", "status", "done: shipped")
	out, _ := present(t, epic, PresentOptions{Peek: true})
	if !strings.Contains(out, "STATUS OUTCOME BACKSTOP (") || !strings.Contains(out, "s done: shipped") {
		t.Fatalf("uncovered done not surfaced:\n%s", out)
	}
	if out, _ := present(t, epic, PresentOptions{Peek: true}); !strings.Contains(out, "s done: shipped") {
		t.Fatalf("peek committed a receipt:\n%s", out)
	}
	present(t, epic, PresentOptions{})
	if out, _ := present(t, epic, PresentOptions{}); out != "" {
		t.Fatalf("receipted backstop repeated: %q", out)
	}

	// A handled row covers its event; an acknowledgement past every row does not.
	epic = t.TempDir()
	logStatus(t, epic, "s", "done", "done: PR 1")
	g, _ := Append(epic, Wake{Epic: "e", Story: "s", Kind: KindWorkerDone, Note: "done: PR 1"})
	if out, _ := present(t, epic, PresentOptions{Peek: true}); strings.Contains(out, "BACKSTOP") {
		t.Fatalf("an unacked row is presented already; the backstop duplicated it:\n%s", out)
	}
	if err := AckThrough(epic, g+5); err != nil {
		t.Fatal(err)
	}
	if out, _ := present(t, epic, PresentOptions{Peek: true}); !strings.Contains(out, "s done: PR 1") {
		t.Fatalf("an ack past every row covered the event:\n%s", out)
	}
	if err := AckThrough(epic, g); err != nil { // naming the row marks it handled
		t.Fatal(err)
	}
	if out, _ := present(t, epic, PresentOptions{Peek: true}); out != "" {
		t.Fatalf("a handled row did not cover its event: %q", out)
	}
}

func TestPresentQuestionsAndStoryKind(t *testing.T) {
	epic := t.TempDir()
	qdir := filepath.Join(epic, "questions", "s")
	if err := os.MkdirAll(qdir, 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(qdir, "q001.md"), []byte("---\nschema: x\n---\npick\nREST or RPC\n"), 0o644)
	logStatus(t, epic, "s", "question", "asked q001")
	logStatus(t, epic, "s", "done", "shipped")
	if out, _ := present(t, epic, PresentOptions{}); !strings.Contains(out, "s [key=q001] needs-decision: pick REST or RPC") {
		t.Fatalf("no story file (unknown kind): the question must stay open past done:\n%s", out)
	}
	os.MkdirAll(filepath.Join(epic, "stories"), 0o755)
	os.WriteFile(filepath.Join(epic, "stories", "s.md"), []byte("---\nid: s\n---\n"), 0o644)
	if out, _ := present(t, epic, PresentOptions{}); strings.Contains(out, "OPEN DECISIONS") {
		t.Fatalf("a ship story's done must supersede its question:\n%s", out)
	}
	os.Remove(filepath.Join(epic, "stories", "s.md"))
	os.WriteFile(filepath.Join(qdir, "q001.answer.md"), []byte("REST"), 0o644)
	if out, _ := present(t, epic, PresentOptions{}); strings.Contains(out, "OPEN DECISIONS") {
		t.Fatalf("an answered question stayed open:\n%s", out)
	}
}

func TestPresentCapsAndWatcherBanner(t *testing.T) {
	if got := CapLine(strings.Repeat("x", 300), 219); len(got) != 219 || !strings.HasSuffix(got, " [truncated]") {
		t.Errorf("CapLine = %d chars %q", len(got), got[len(got)-15:])
	}
	if got := CapLine("short", 219); got != "short" {
		t.Errorf("CapLine altered a short line: %q", got)
	}
	epic := t.TempDir()
	if _, err := Append(epic, Wake{Epic: "e", Story: "s", Kind: KindStuck, Note: "x"}); err != nil {
		t.Fatal(err)
	}
	// No story in flight: no banner whatever the watcher says.
	if _, e := present(t, epic, PresentOptions{WatcherAlive: func() bool { return false }}); e != "" {
		t.Errorf("banner with nothing in flight: %q", e)
	}
}
