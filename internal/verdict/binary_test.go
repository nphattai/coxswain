package verdict_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// Binary integration (DESIGN Contract 2): the real built cox runs `cox ship merge` / `cox audit pr` on a temp workspace
// root with a fake gh on PATH, so the verdict layer is exercised through the CLI exactly as a leader runs it.

var (
	coxOnce sync.Once
	coxPath string
	coxErr  error
)

// TestMain removes the built binary's temp dir after the run, so no ~10 MB cox is left behind per test run.
func TestMain(m *testing.M) {
	code := m.Run()
	if coxPath != "" {
		_ = os.RemoveAll(filepath.Dir(coxPath))
	}
	os.Exit(code)
}

func coxBin(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("builds cmd/cox; skipped under -short")
	}
	coxOnce.Do(func() {
		dir, err := os.MkdirTemp("", "cox-verdict-bin-")
		if err != nil {
			coxErr = err
			return
		}
		coxPath = filepath.Join(dir, "cox")
		cmd := exec.Command("go", "build", "-o", coxPath, "./cmd/cox")
		cmd.Dir, _ = filepath.Abs(filepath.Join("..", ".."))
		if out, err := cmd.CombinedOutput(); err != nil {
			coxErr = fmt.Errorf("go build cox: %v\n%s", err, out)
		}
	})
	if coxErr != nil {
		t.Fatal(coxErr)
	}
	return coxPath
}

// fakeGH answers the gh calls cox ship merge and cox audit pr make. State lives in $FAKE_GH_DIR: a `merged` file after
// `pr merge`, the PR's mergeable value in $FAKE_GH_MERGEABLE, the diff text in $FAKE_GH_DIR/diff.
const fakeGH = `#!/bin/sh
echo "$@" >> "$FAKE_GH_DIR/gh.log"
case "$*" in
'pr view '*' --json state')
  if [ -f "$FAKE_GH_DIR/merged" ]; then echo '{"state":"MERGED"}'; else echo '{"state":"OPEN"}'; fi ;;
'pr view '*' --json comments,reviews') echo '{"comments":[],"reviews":[]}' ;;
'pr view '*)
  echo '{"number":9,"headRefOid":"deadbeef12345678","headRefName":"story/s1","baseRefName":"epic/e1","state":"OPEN","isDraft":false,"mergeable":"'"${FAKE_GH_MERGEABLE:-MERGEABLE}"'"}' ;;
'pr checks '*) echo '[{"name":"ci","state":"SUCCESS","bucket":"pass"}]' ;;
'pr merge '*) : > "$FAKE_GH_DIR/merged" ;;
'pr diff '*) cat "$FAKE_GH_DIR/diff" ;;
*) echo "fake gh: unexpected $*" >&2; exit 1 ;;
esac
`

type binCase struct {
	epic, ghDir string
	env         []string
}

// newBinCase builds a workspace root (cox/workspace.json with repo app), epic e1 whose repos file names app, and a fake
// gh on PATH. COX_* is scrubbed so the test never inherits a worker terminal (COX_STORY refuses a merge).
func newBinCase(t *testing.T) binCase {
	t.Helper()
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "cox", "workspace.json"), `{"repos":[{"alias":"app","path":"`+root+`","production":"main"}]}`)
	c := binCase{epic: filepath.Join(root, "epics", "e1"), ghDir: t.TempDir()}
	mustWrite(t, filepath.Join(c.epic, "repos"), "app\n")
	if err := os.MkdirAll(filepath.Join(c.epic, "app"), 0o755); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(c.ghDir, "diff"), "")
	bin := t.TempDir()
	mustWrite(t, filepath.Join(bin, "gh"), fakeGH)
	if err := os.Chmod(filepath.Join(bin, "gh"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "COX_") && !strings.HasPrefix(kv, "PATH=") && !strings.HasPrefix(kv, "ORCA_") {
			c.env = append(c.env, kv)
		}
	}
	c.env = append(c.env, "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"), "FAKE_GH_DIR="+c.ghDir)
	return c
}

func (c binCase) cox(t *testing.T, args ...string) (string, int) {
	t.Helper()
	cmd := exec.Command(coxBin(t), args...)
	cmd.Env = c.env
	out, err := cmd.CombinedOutput()
	if ee, ok := err.(*exec.ExitError); ok {
		return string(out), ee.ExitCode()
	} else if err != nil {
		t.Fatalf("run cox: %v", err)
	}
	return string(out), 0
}

func mustWrite(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// B-59: a real merge through the built binary reads back merged, exits 0 and writes the ledger `merged` row. On the base
// the read-back returned the pre-merge struct (open), so every real merge exited 1 with no ledger row.
func TestBinaryShipMergeReadsBackMerged(t *testing.T) {
	c := newBinCase(t)
	out, rc := c.cox(t, "ship", "merge", "--pr", "9", "--epic", c.epic, "--captain")
	if rc != 0 || !strings.Contains(out, "verdict=merged") {
		t.Fatalf("cox ship merge rc=%d, want 0 and verdict=merged:\n%s", rc, out)
	}
	ledger, err := os.ReadFile(filepath.Join(c.epic, "ledger.jsonl"))
	if err != nil || !strings.Contains(string(ledger), `"type":"merged"`) {
		t.Fatalf("a confirmed merge must write the ledger merged row (err=%v):\n%s", err, ledger)
	}
}
