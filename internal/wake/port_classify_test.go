//go:build port

// Port tests (wave 1, cox-supervision-port-triage): firstmate's classifier suites fm-classify-corr-token and
// fm-classify-decision-key translated case by case against cox's wake.Classify. Firstmate pinned at 1e0e773
// (references/firstmate, read only). Every case is t.Run("FM/<suite>/<case>") with a `// fm: path:line` citation and a
// `// cox:` mechanism tag; a case whose mechanism cox lacks calls notImplemented and fails (DESIGN translation contract).
package wake

import (
	"testing"

	"github.com/nphattai/coxswain/internal/adapter/backend"
)

// notImplemented fails a case whose firstmate mechanism cox does not have, naming the gap (contract rule 3).
func notImplemented(t *testing.T, mechanism string) {
	t.Helper()
	t.Fatalf("not implemented in cox: %s", mechanism)
}

// statusKind classifies one worker status line the way the watcher sees it: a status message whose subject is the line.
func statusKind(line string) Kind {
	return Classify(backend.Message{Type: "status", Subject: line})
}
