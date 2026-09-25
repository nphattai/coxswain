package backend

import (
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"
)

// LaunchLine prefixes the cox env (COX_EPIC/COX_STORY from the story path, COX_PLANE=terminal) and then types the
// adapter-owned argv (HarnessSpec.Argv) token by token, each shell-quoted. The order of the argv is preserved and only
// shell-safe quoting is added, so the process argv is byte-for-byte what the adapter returned.
func TestLaunchLineQuotesArgvWithEnv(t *testing.T) {
	defer pinCoxSelf("")()
	spec := HarnessSpec{Name: "claude", Argv: []string{"claude", "--model", "claude-opus-4-8", "the prompt"}}
	brief := Brief{StoryPath: "/epics/v2/stories/m10.md"}
	got := LaunchLine(spec, brief)
	want := "COX_EPIC='/epics/v2' COX_STORY='m10' COMPACT_ADVISER_DISABLE=1 COX_PLANE=terminal 'claude' '--model' 'claude-opus-4-8' 'the prompt'"
	if got != want {
		t.Fatalf("LaunchLine =\n  %q\nwant\n  %q", got, want)
	}
}

// B-41: every worker launch line disables compact-adviser for the unattended session, whatever the harness, and the
// variable is in the env prefix (before the argv), so it reaches the harness process rather than being an argument.
func TestLaunchLineDisablesCompactAdviser(t *testing.T) {
	for _, h := range []string{"claude", "codex", "pi"} {
		line := LaunchLine(HarnessSpec{Name: h, Argv: []string{h, "prompt"}}, Brief{StoryPath: "/e/stories/s.md"})
		i, j := strings.Index(line, "COMPACT_ADVISER_DISABLE=1 "), strings.Index(line, shellQuote(h))
		if i < 0 || j < 0 || i > j {
			t.Errorf("%s launch line lacks COMPACT_ADVISER_DISABLE=1 in its env prefix: %q", h, line)
		}
	}
}

// pinCoxSelf sets coxSelf for a test and returns a restore func, so the COX_BIN prefix is deterministic (or absent).
func pinCoxSelf(v string) func() {
	prev := coxSelf
	coxSelf = v
	return func() { coxSelf = prev }
}

// A dispatching cox forwards its own absolute path as COX_BIN so the worker's harness hooks call the same binary (not
// the worker terminal's PATH cox, which during an epic is the pinned driver that may lack a subcommand the candidate
// added).
func TestLaunchLineForwardsCoxBin(t *testing.T) {
	defer pinCoxSelf("/tmp/dist/cox")()
	got := LaunchLine(HarnessSpec{Name: "pi", Argv: []string{"pi", "hi"}}, Brief{StoryPath: "/e/stories/s.md"})
	want := "COX_EPIC='/e' COX_STORY='s' COX_BIN='/tmp/dist/cox' COMPACT_ADVISER_DISABLE=1 COX_PLANE=terminal 'pi' 'hi'"
	if got != want {
		t.Fatalf("LaunchLine =\n  %q\nwant\n  %q", got, want)
	}
}

// A launch with no story path types no COX_EPIC/COX_STORY (nothing to derive), only COX_PLANE plus the argv. Empty argv
// tokens are skipped so a harness that omits an optional flag never types a bare ”.
func TestLaunchLineNoStoryPathSkipsEnvAndEmptyTokens(t *testing.T) {
	defer pinCoxSelf("")()
	got := LaunchLine(HarnessSpec{Name: "pi", Argv: []string{"pi", "", "hello"}}, Brief{})
	want := "COMPACT_ADVISER_DISABLE=1 COX_PLANE=terminal 'pi' 'hello'"
	if got != want {
		t.Fatalf("LaunchLine = %q, want %q", got, want)
	}
}

// A fake/arbitrary adapter argv (what registry.LaunchArgs threads in as data) is typed verbatim by the backend: the
// backend adds only env + quoting and never rebuilds or reinterprets the argv (launch seam, ADR 0002).
func TestLaunchLineTypesArbitraryAdapterArgvVerbatim(t *testing.T) {
	argv := []string{"fakeharness", "--flag", "value with space", "-x", "prompt text"}
	line := LaunchLine(HarnessSpec{Name: "fakeharness", Argv: argv}, Brief{StoryPath: "/e/stories/s.md"})
	for _, tok := range argv {
		if !strings.Contains(line, shellQuote(tok)) {
			t.Errorf("launch line missing quoted token %q: %q", tok, line)
		}
	}
}

// ADR 0002: the backend must not import the harness layer. Terminal launch argv is composed by cmd/cox/internal-arena
// via registry.LaunchArgs and threaded in as HarnessSpec.Argv (data), so no file in this package may import
// internal/adapter/harness or any adapter under it.
func TestBackendDoesNotImportHarness(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		for _, imp := range f.Imports {
			path := strings.Trim(imp.Path.Value, `"`)
			if strings.Contains(path, "internal/adapter/harness") {
				t.Errorf("%s imports %q; the backend must not import the harness layer (ADR 0002)", name, path)
			}
		}
	}
}
