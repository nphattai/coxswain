package lavish

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nphattai/coxswain/internal/arena/synth"
	"github.com/nphattai/coxswain/internal/artifact"
)

const answerSynth = `---
epic: demo
---
# Arena synthesis - demo

## Claims

| id | role | claim | evidence | tier | severity | verified | verdict | question | reason | DESIGN.md change | captain agrees |
|---|---|---|---|---|---|---|---|---|---|---|---|
| reviewer-1-1 | reviewer | who owns the cell | x:1 | 3 | significant | pass | captain_decision | who owns it? | | | |
`

// seedSynthesis writes a round-1 synthesis, points synthesis.md at it, records its sha (as cox arena synth does), and
// generates the arena artifact so its sidecar carries the same synthesis sha.
func seedSynthesis(t *testing.T, epic string) string {
	t.Helper()
	dir := filepath.Join(epic, "reports", "arena")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	round := filepath.Join(dir, "synthesis-round-1.md")
	if err := os.WriteFile(round, []byte(answerSynth), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("synthesis-round-1.md", filepath.Join(dir, "synthesis.md")); err != nil {
		t.Fatal(err)
	}
	if err := synth.WriteSha(epic, 1, []byte(answerSynth)); err != nil {
		t.Fatal(err)
	}
	page, err := artifact.GenerateArenaSynth(epic, 1, synth.ContentSha)
	if err != nil {
		t.Fatal(err)
	}
	return page
}

func answerPollTOON(page string) string {
	return "session:\n  file: " + page + "\n  status: feedback\n" +
		"prompts[1]:\n  - tag: answer\n    prompt: answer reviewer-1-1 yes\n    claim_id: reviewer-1-1"
}

func TestAnswerViaArtifactWritesCell(t *testing.T) {
	epic := t.TempDir()
	page := seedSynthesis(t, epic)

	cfg := Config{Binary: fakeLavish(t, answerPollTOON(page))}
	var out, errb bytes.Buffer
	res, err := Poll(cfg, epic, page, 500*time.Millisecond, nil, &out, &errb)
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != ExitFeedback {
		t.Fatalf("exit = %d", res.ExitCode)
	}
	if !strings.Contains(out.String(), "recorded in synthesis round 1") {
		t.Fatalf("answer should be recorded, got:\n%s", out.String())
	}
	// The captain-agrees cell for reviewer-1-1 now carries the answer.
	got, _ := os.ReadFile(filepath.Join(epic, "reports", "arena", "synthesis-round-1.md"))
	if !strings.Contains(string(got), "yes (by captain)") {
		t.Fatalf("synthesis cell not written:\n%s", got)
	}
}

func TestAnswerRefusedWhenArtifactStale(t *testing.T) {
	epic := t.TempDir()
	page := seedSynthesis(t, epic)

	// Re-synthesize after the artifact was generated: the synthesis content (and its recorded sha) change, so the
	// artifact's sidecar sha no longer matches.
	round := filepath.Join(epic, "reports", "arena", "synthesis-round-1.md")
	changed := strings.Replace(answerSynth, "who owns the cell", "who owns the cell (edited)", 1)
	if err := os.WriteFile(round, []byte(changed), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := synth.WriteSha(epic, 1, []byte(changed)); err != nil {
		t.Fatal(err)
	}

	cfg := Config{Binary: fakeLavish(t, answerPollTOON(page))}
	var out, errb bytes.Buffer
	if _, err := Poll(cfg, epic, page, 500*time.Millisecond, nil, &out, &errb); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "refused") {
		t.Fatalf("a stale artifact answer must be refused, got:\n%s", out.String())
	}
	// The cell must NOT have been written.
	got, _ := os.ReadFile(round)
	if strings.Contains(string(got), "by captain") {
		t.Fatalf("stale answer should not write the cell:\n%s", got)
	}
}
