package control_test

import (
	"strings"
	"testing"
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
