package control_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nphattai/coxswain/internal/adapter/backend"
	"github.com/nphattai/coxswain/internal/adapter/backend/fake"
	"github.com/nphattai/coxswain/internal/adapter/harness/registry"
	"github.com/nphattai/coxswain/internal/protocol/busy"
)

// B-41 against the real binary: the launch line `cox control relaunch` types into a fresh terminal (fake orca logs it)
// carries COMPACT_ADVISER_DISABLE=1 in its env prefix, so an unattended claude worker's compact-adviser stays inert.
func TestRefreshRelaunchLineDisablesCompactAdviser(t *testing.T) {
	c := workspaceCase(t, "harness: claude\n")
	c.priorExited()
	c.cox(t, "control", story, "relaunch", "--note", "resume", "--epic", c.epic, "--allow-unsandboxed")
	l := c.launchLine(t)
	if i, j := strings.Index(l, "COMPACT_ADVISER_DISABLE=1 "), strings.Index(l, "'claude'"); i < 0 || j < 0 || i > j {
		t.Fatalf("relaunch line lacks COMPACT_ADVISER_DISABLE=1 before the harness argv: %q", l)
	}
}

// dialogOrca is a fake orca whose worker terminal is live with its agent "waiting" on a local dialog; every call is
// logged. The interrupt key is accepted (a key may or may not close a dialog; here it does not).
const dialogOrca = `#!/bin/sh
echo "$@" >> '%LOG%'
case "$1 $2" in
'terminal show') echo '{"ok":true,"result":{"terminal":{"connected":true,"tabId":"T","leafId":"L"}}}' ;;
'worktree ps') echo '{"ok":true,"result":{"worktrees":[{"agents":[{"paneKey":"T:L","state":"waiting"}]}]}}' ;;
'terminal read') echo '{"ok":true,"result":{"terminal":{"tail":["Do you want to proceed?","> 1. Yes","  2. No"]}}}' ;;
'terminal send') echo '{"ok":true,"result":{}}' ;;
*) echo '{"ok":false,"error":{"message":"fake orca"}}'; exit 1 ;;
esac
`

// B-01 against the real binary: `cox control s1 interrupt` over a busy worker on a local harness dialog exits non-zero, says
// so, never prints "interrupt: s1 ok", and types nothing into the dialog (only the interrupt key reaches orca).
func TestRefreshInterruptOverDialogFails(t *testing.T) {
	c := workspaceCase(t, "harness: claude\n")
	must(t, os.WriteFile(filepath.Join(filepath.Dir(c.log), "orca"), []byte(strings.ReplaceAll(dialogOrca, "%LOG%", c.log)), 0o755))
	// A claude worker on a dialog is mid-turn by its own hook: its busy record reads busy.
	_, err := busy.Arm(c.epic, story, "claude", registry.Card("claude").BusySources)
	must(t, err)
	out, code := c.cox(t, "control", story, "interrupt", "--epic", c.epic)
	if code == 0 || strings.Contains(out, "interrupt: s1 ok") || !strings.Contains(out, "dialog") {
		t.Fatalf("interrupt over an open dialog: exit %d, out %q; want a non-zero dialog failure", code, out)
	}
	for _, l := range strings.Split(c.calls(t), "\n") {
		if strings.HasPrefix(l, "terminal send") && !strings.Contains(l, "--interrupt") {
			t.Fatalf("text was typed into the open dialog: %q", l)
		}
	}
}

// dialogBackend is the fake backend plus the optional DialogReader capability, answering from a script of reads.
type dialogBackend struct {
	*fake.Backend
	reads []bool
}

func (d *dialogBackend) Dialog(backend.Session) bool {
	if len(d.reads) == 0 {
		return false
	}
	v := d.reads[0]
	d.reads = d.reads[1:]
	return v
}

// B-01: a dialog the backend sees behind a busy composer fails the interrupt with no doorbell; a dialog the interrupt
// key closes within the settle window is not a failure, and the doorbell then rings as usual.
func TestRefreshInterruptDialogReader(t *testing.T) {
	epic := epicWith(t, "claude")
	b := &dialogBackend{Backend: fake.New(), reads: []bool{true, true, true, true, true, true, true, true}}
	b.Liveness, b.ComposerState = backend.Alive, backend.ComposerBusy
	if err := ctl(epic, b, "claude").Interrupt(story, sess); err == nil || !strings.Contains(err.Error(), "dialog") {
		t.Fatalf("dialog behind a busy composer: err = %v, want a dialog failure", err)
	}
	if n := called(b.Backend, "Send"); n > 0 {
		t.Fatalf("%d doorbell(s) typed into the dialog", n)
	}

	epic2 := epicWith(t, "claude")
	b2 := &dialogBackend{Backend: fake.New(), reads: []bool{true}} // open right after the key, closed on the next read
	b2.Liveness, b2.ComposerState = backend.Alive, backend.ComposerBusy
	if err := ctl(epic2, b2, "claude").Interrupt(story, sess); err != nil {
		t.Fatalf("a dialog closed within the settle window must not fail the interrupt: %v", err)
	}
	if n := called(b2.Backend, "Send"); n != 1 {
		t.Fatalf("doorbell sends = %d, want 1 once the dialog closed", n)
	}
}
