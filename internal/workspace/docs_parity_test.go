package workspace

import (
	"os"
	"reflect"
	"strings"
	"testing"
)

// The reference pages under docs/reference/ must document every field of the Go types they describe, so a reader who
// copies from the docs never hits a key the loader does not know and a field added or renamed in Go fails the build
// until the docs are updated. This mirrors the harness card/doc parity test (tests/integration): enumerate the
// authoritative shape from the code, then assert the doc names each field. A field is "documented" when its JSON key
// appears backtick-quoted (`field`) somewhere in the page.

// jsonNames walks a struct type and collects every JSON key it (recursively) exposes: embedded structs contribute their
// own keys, and a field whose type is a struct (possibly behind a pointer, slice, array or map value) is recursed into
// so nested sections are covered too. Fields tagged `json:"-"` or with no usable name are skipped.
func jsonNames(t reflect.Type, out map[string]bool) {
	for t.Kind() == reflect.Ptr || t.Kind() == reflect.Slice || t.Kind() == reflect.Array || t.Kind() == reflect.Map {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return
	}
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if f.Anonymous { // embedded (e.g. Meta): contribute its keys directly
			jsonNames(f.Type, out)
			continue
		}
		name := strings.SplitN(f.Tag.Get("json"), ",", 2)[0]
		if name != "" && name != "-" {
			out[name] = true
		}
		jsonNames(f.Type, out) // recurse into nested/slice/map-of struct fields
	}
}

func assertDocuments(t *testing.T, doc string, names map[string]bool) {
	t.Helper()
	b, err := os.ReadFile(doc)
	if err != nil {
		t.Fatalf("read %s: %v", doc, err)
	}
	text := string(b)
	for name := range names {
		if !strings.Contains(text, "`"+name+"`") {
			t.Errorf("%s does not document field `%s` (add a row for it; the Go type is the authority)", doc, name)
		}
	}
}

func TestWorkspaceJSONDocParity(t *testing.T) {
	names := map[string]bool{}
	jsonNames(reflect.TypeOf(Workspace{}), names)
	assertDocuments(t, "../../docs/reference/workspace-json.md", names)
}

func TestPolicyJSONDocParity(t *testing.T) {
	names := map[string]bool{}
	jsonNames(reflect.TypeOf(Policy{}), names)
	// Launch has a custom UnmarshalJSON, so its `arena` sub-key is not reachable by struct tags; assert it explicitly.
	names["arena"] = true
	assertDocuments(t, "../../docs/reference/policy-json.md", names)
}

// storyFrontmatterKeys is the frontmatter contract: every key the story template renders, plus `harness`, the alias the
// story-meta reader (cmd/cox: readStoryMeta) accepts for `agent`. Adding a key to templates/story.md's frontmatter fails
// this test until docs/reference/story-frontmatter.md documents it.
func TestStoryFrontmatterDocParity(t *testing.T) {
	keys := frontmatterKeys(t, "../../templates/story.md")
	keys["harness"] = true // reader alias for `agent`, not in the template
	assertDocuments(t, "../../docs/reference/story-frontmatter.md", keys)
}

// frontmatterKeys reads the leading --- ... --- block of a markdown file and returns the top-level `key:` names.
func frontmatterKeys(t *testing.T, path string) map[string]bool {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	keys := map[string]bool{}
	inFM := false
	for _, line := range strings.Split(string(b), "\n") {
		if strings.TrimSpace(line) == "---" {
			if inFM {
				break
			}
			inFM = true
			continue
		}
		if !inFM {
			continue
		}
		k, _, ok := strings.Cut(line, ":")
		if ok && k != "" && !strings.HasPrefix(k, " ") {
			keys[strings.TrimSpace(k)] = true
		}
	}
	if len(keys) == 0 {
		t.Fatalf("no frontmatter keys parsed from %s", path)
	}
	return keys
}
