package integration

import (
	"strings"
	"testing"
)

// TestCIWorkflowRunsOncePerRef guards B-76: a story branch runs CI through its PR only (push is limited to main and the
// epic branches), and a newer push cancels the superseded run of the same ref instead of queueing behind it (main keeps
// every run).
func TestCIWorkflowRunsOncePerRef(t *testing.T) {
	ci := readFile(t, "../../.github/workflows/ci.yml")
	head := ci[:strings.Index(ci, "\njobs:")]
	for _, want := range []string{"push:\n    branches: [main, 'epic/**']", "pull_request:", "concurrency:\n  group: ci-${{ github.ref }}\n  cancel-in-progress: ${{ github.ref != 'refs/heads/main' }}"} {
		if !strings.Contains(head, want) {
			t.Errorf("ci.yml triggers lack %q:\n%s", want, head)
		}
	}
}
