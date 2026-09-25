package question

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// B-61: ids are case-insensitive. A worker that retypes q001 as Q001 addresses the same question, for both the leader's
// Answer and the worker's Wait; a non-id is still refused.
func TestIDCaseInsensitive(t *testing.T) {
	epic := t.TempDir()
	id, err := Alloc(epic, "s1", "ok?")
	if err != nil || id != "q001" {
		t.Fatalf("Alloc = %q, %v; want q001", id, err)
	}
	if _, err := Answer(epic, "s1", "Q001", "yes", false); err != nil {
		t.Fatalf("Answer(Q001): %v", err)
	}
	got, timedOut, err := Wait(epic, "s1", " Q001 ", time.Second)
	if err != nil || timedOut || got != "yes" {
		t.Fatalf("Wait(Q001) = %q timedOut=%v err=%v; want yes", got, timedOut, err)
	}
	if _, err := os.Stat(filepath.Join(Dir(epic, "s1"), "handled", "q001.answer.md")); err != nil {
		t.Fatalf("answer not moved to handled under its canonical name: %v", err)
	}
	for _, bad := range []string{"Q1", "x001", "q00a", ""} {
		if _, _, err := Wait(epic, "s1", bad, time.Millisecond); err == nil || !strings.Contains(err.Error(), "not a question id") {
			t.Fatalf("Wait(%q) err = %v; want not a question id", bad, err)
		}
	}
}

// B-61 against the real binary on a temp epic: `cox story report question` prints q001, the leader answers Q001 with
// `cox reply`, and `cox question wait Q001` returns the answer (exit 0) instead of "not a question id".
func TestBinaryQuestionIDCaseInsensitive(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the cox binary")
	}
	root, _ := filepath.Abs(filepath.Join("..", "..", ".."))
	cox := filepath.Join(t.TempDir(), "cox")
	build := exec.Command("go", "build", "-o", cox, "./cmd/cox")
	build.Dir = root
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build cox: %v\n%s", err, out)
	}
	epic := filepath.Join(t.TempDir(), "epics", "e1")
	if err := os.MkdirAll(filepath.Join(epic, "stories"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(epic, "stories", "s1.md"), []byte("---\nid: s1\n---\nbody\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var env []string
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "ORCA_") && !strings.HasPrefix(kv, "COX_") {
			env = append(env, kv)
		}
	}
	env = append(env, "COX_PLANE=terminal", "PATH="+t.TempDir()) // no orca on PATH: reply's ring is best-effort
	run := func(args ...string) (string, int) {
		cmd := exec.Command(cox, args...)
		cmd.Env = env
		out, err := cmd.CombinedOutput()
		if ee, ok := err.(*exec.ExitError); ok {
			return string(out), ee.ExitCode()
		} else if err != nil {
			t.Fatalf("run cox %v: %v", args, err)
		}
		return string(out), 0
	}
	out, code := run("story", "report", "question", "--body", "ok?", "--epic", epic, "--story", "s1")
	if code != 0 || strings.TrimSpace(out) != "q001" {
		t.Fatalf("report question = %q (exit %d); want q001", out, code)
	}
	if out, code := run("reply", "s1", "Q001", "go ahead", "--epic", epic); code != 0 {
		t.Fatalf("cox reply s1 Q001 exit %d: %s", code, out)
	}
	out, code = run("question", "wait", "Q001", "--max", "5s", "--epic", epic, "--story", "s1")
	if code != 0 || !strings.Contains(out, "go ahead") {
		t.Fatalf("cox question wait Q001 = %q (exit %d); want the answer, exit 0", out, code)
	}
}
