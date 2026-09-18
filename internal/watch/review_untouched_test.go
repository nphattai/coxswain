package watch

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"
)

// TestWatcherDoesNotTouchReview enforces the M13 contract that the watcher never drives the review poll: its Tick is
// non-blocking by contract and owns liveness for every story, so a blocking lavish poll inside it would blind liveness
// detection (arena round-1 reviewer, epic-blocking). The poll runs only as the separate `cox review poll` subprocess.
//
// The check is structural (the watcher cannot call what it does not import and does not name): no non-test file in this
// package may import the review adapter or reference lavish/review-poll symbols.
func TestWatcherDoesNotTouchReview(t *testing.T) {
	fset := token.NewFileSet()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, parser.ParseComments)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		for _, imp := range f.Imports {
			path := strings.Trim(imp.Path.Value, `"`)
			if strings.Contains(path, "adapter/review") {
				t.Errorf("%s imports %q; the watcher must not touch the review adapter", name, path)
			}
		}
		ast.Inspect(f, func(n ast.Node) bool {
			id, ok := n.(*ast.Ident)
			if ok && (id.Name == "lavish") {
				t.Errorf("%s references %q; the watcher must not drive the review poll", name, id.Name)
			}
			return true
		})

		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(src), "review poll") {
			t.Errorf("%s mentions a review poll; the watcher Tick must not call it", name)
		}
	}
}
