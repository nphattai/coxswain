package orca

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// B-34a: Orca exits non-zero on an ok=false envelope and still prints it on stdout. call must surface Orca's code and
// message, not the bare exit status, and a worktree create on an unregistered repo must say how to register it.
func TestCallParsesEnvelopeOnNonZeroExit(t *testing.T) {
	exitErr := exec.Command("sh", "-c", "exit 1").Run() // a real *exec.ExitError, as exec.Command.Output returns
	c := New("run")
	c.run = func(args ...string) ([]byte, error) {
		return []byte(`{"ok":false,"error":{"code":"repo_not_found","message":"No repo matches path:/tmp/app"}}`), exitErr
	}
	_, err := c.WorktreeCreate("/tmp/app", "epic/e1", "main")
	if err == nil {
		t.Fatal("worktree create on an unregistered repo must fail")
	}
	for _, want := range []string{"repo_not_found", "No repo matches path:/tmp/app", "orca repo add --path /tmp/app`"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q lacks %q", err, want)
		}
	}
	if strings.Contains(err.Error(), "exit status") {
		t.Errorf("error still reports the bare exit status: %v", err)
	}
}

// A failed process whose stdout is not an envelope keeps the process error (nothing to parse, nothing invented).
func TestCallKeepsExitErrorWithoutEnvelope(t *testing.T) {
	c := New("run")
	c.run = func(args ...string) ([]byte, error) { return []byte("boom\n"), errors.New("exit status 2") }
	if _, err := c.call("status", "--json"); err == nil || !strings.Contains(err.Error(), "exit status 2") {
		t.Fatalf("err = %v; want the process error", err)
	}
}

// B-34a against the real binary: `cox epic new` on a repo Orca has not registered fails with Orca's repo_not_found and
// the registration command for that checkout, not "exit status 1" (fake orca exits 1 with the envelope, as Orca does).
func TestBinaryEpicNewReportsRepoNotFound(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the cox binary")
	}
	root, _ := filepath.Abs(filepath.Join("..", "..", "..", ".."))
	tmp := t.TempDir()
	cox := filepath.Join(tmp, "cox")
	build := exec.Command("go", "build", "-o", cox, "./cmd/cox")
	build.Dir = root
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build cox: %v\n%s", err, out)
	}
	app, ws, bin := filepath.Join(tmp, "app"), filepath.Join(tmp, "ws"), filepath.Join(tmp, "bin")
	for _, d := range []string{app, filepath.Join(ws, "cox"), filepath.Join(ws, "proj"), bin} {
		must(t, os.MkdirAll(d, 0o755))
	}
	for _, a := range [][]string{{"init", "-q", "-b", "main"}, {"-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "--allow-empty", "-m", "i"}} {
		if out, err := exec.Command("git", append([]string{"-C", app}, a...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", a, err, out)
		}
	}
	pol, err := os.ReadFile(filepath.Join(root, "templates", "policy.json"))
	must(t, err)
	must(t, os.WriteFile(filepath.Join(ws, "cox", "policy.json"), pol, 0o644))
	must(t, os.WriteFile(filepath.Join(ws, "cox", "workspace.json"), []byte(`{"projects":[{"name":"proj","path":"proj"}],"repos":[{"alias":"app","path":"`+app+`","production":"main"}],"hosts":[{"name":"local"}]}`), 0o644))
	must(t, os.WriteFile(filepath.Join(bin, "orca"), []byte(`#!/bin/sh
case "$1 $2" in
'worktree create') echo '{"ok":false,"error":{"code":"repo_not_found","message":"No repo matches the selector"}}'; exit 1 ;;
*) echo '{"ok":true,"result":{}}' ;;
esac
`), 0o755))
	var env []string
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "ORCA_") && !strings.HasPrefix(kv, "COX_") {
			env = append(env, kv)
		}
	}
	cmd := exec.Command(cox, "epic", "new", "proj", "e1", "--repo", "app", "--no-push")
	cmd.Dir = ws
	cmd.Env = append(env, "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("epic new on an unregistered repo succeeded: %s", out)
	}
	for _, want := range []string{"repo_not_found", "orca repo add --path " + app} {
		if !strings.Contains(string(out), want) {
			t.Errorf("epic new output lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(string(out), "exit status") {
		t.Errorf("epic new still reports a bare exit status:\n%s", out)
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
