package workspace

import (
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// The reference pages under docs/reference/ must document every field of the Go types they describe, so a reader who
// copies from the docs never hits a key the loader does not know and a field added or renamed in Go fails the build
// until the docs are updated. This mirrors the harness card/doc parity test (tests/integration): enumerate the
// authoritative shape from the code, then assert the doc names each field.
//
// Fields are tracked by their FULL JSON path (section-qualified), not by bare leaf name, so a field that shares a leaf
// with another section (policy has `default`, `binary`, `npx`, `value`, `rule`; workspace has `name`, `path`, `alias`)
// must be documented in ITS OWN section, not merely somewhere on the page (finding 10 / codex PR#4 #5). A field is
// "documented" when its JSON leaf appears backtick-quoted inside the doc region for its top-level section (the text
// under a `### <section>` / `## <Type>` heading up to the next heading); top-level section names and the shared Meta
// fields (`why`, `review_when`, documented once) are matched anywhere on the page.

// metaLeaves are the Meta fields every justified section embeds; the reference pages document them once in a shared
// table, so they are matched page-wide rather than per section.
var metaLeaves = map[string]bool{"why": true, "review_when": true}

// jsonPaths walks a struct type and collects every JSON path it (recursively) exposes as a dotted key: an embedded
// struct contributes its keys at the parent's path, and a field whose type is a struct (behind a pointer, slice, array,
// or map value) is recursed into so nested sections get their own qualified paths. Fields tagged `json:"-"` or with no
// usable name are skipped.
func jsonPaths(t reflect.Type, prefix string, out map[string]bool) {
	for t.Kind() == reflect.Ptr || t.Kind() == reflect.Slice || t.Kind() == reflect.Array || t.Kind() == reflect.Map {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return
	}
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if f.Anonymous { // embedded (e.g. Meta): its keys promote to the parent path
			jsonPaths(f.Type, prefix, out)
			continue
		}
		name := strings.SplitN(f.Tag.Get("json"), ",", 2)[0]
		if name == "" || name == "-" {
			continue
		}
		path := name
		if prefix != "" {
			path = prefix + "." + name
		}
		out[path] = true
		jsonPaths(f.Type, path, out) // recurse into nested/slice/map-of struct fields
	}
}

// normHeading normalises a doc heading (or a path segment) for section matching: lower-cased, backtick- and space-free,
// with a single trailing "s" dropped so a plural JSON key (`repos`) matches a singular type heading (`Repo`).
func normHeading(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.TrimLeft(s, "# ")
	s = strings.ReplaceAll(s, "`", "")
	s = strings.TrimSpace(s)
	return strings.TrimSuffix(s, "s")
}

// docSections splits a markdown page into heading-delimited regions: the normalised heading text maps to the body below
// it (up to the next heading). Regions that normalise to the same key are concatenated.
func docSections(text string) map[string]string {
	sections := map[string]string{}
	cur := ""
	var buf strings.Builder
	flush := func() {
		if cur != "" {
			sections[cur] += buf.String()
		}
		buf.Reset()
	}
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "#") {
			flush()
			cur = normHeading(line)
			continue
		}
		buf.WriteString(line)
		buf.WriteByte('\n')
	}
	flush()
	return sections
}

// undocumented returns the JSON paths that the page fails to document under the one-row-per-field-per-section rule. A
// top-level section name (no dot) and a shared Meta leaf are documented anywhere; every other field's leaf must appear
// backtick-quoted inside its own section's region.
func undocumented(text string, paths map[string]bool) []string {
	sections := docSections(text)
	var missing []string
	for p := range paths {
		leaf := p
		if i := strings.LastIndex(p, "."); i >= 0 {
			leaf = p[i+1:]
		}
		token := "`" + leaf + "`"
		section, nested := "", false
		if i := strings.Index(p, "."); i >= 0 {
			section, nested = p[:i], true
		}
		if !nested || metaLeaves[leaf] {
			if !strings.Contains(text, token) {
				missing = append(missing, p)
			}
			continue
		}
		if !strings.Contains(sections[normHeading(section)], token) {
			missing = append(missing, p)
		}
	}
	sort.Strings(missing)
	return missing
}

func assertDocuments(t *testing.T, doc string, paths map[string]bool) {
	t.Helper()
	b, err := os.ReadFile(doc)
	if err != nil {
		t.Fatalf("read %s: %v", doc, err)
	}
	for _, p := range undocumented(string(b), paths) {
		t.Errorf("%s does not document field `%s` in its section (the Go type is the authority; add a row for it)", doc, p)
	}
}

func TestWorkspaceJSONDocParity(t *testing.T) {
	paths := map[string]bool{}
	jsonPaths(reflect.TypeOf(Workspace{}), "", paths)
	assertDocuments(t, "../../docs/reference/workspace-json.md", paths)
}

func TestPolicyJSONDocParity(t *testing.T) {
	paths := map[string]bool{}
	jsonPaths(reflect.TypeOf(Policy{}), "", paths)
	// Launch has a custom UnmarshalJSON, so its `arena` sub-key is not reachable by struct tags; assert it explicitly.
	paths["arena"] = true
	assertDocuments(t, "../../docs/reference/policy-json.md", paths)
}

// A nested field that shares a leaf name with another section must be documented in ITS OWN section: documenting the
// leaf only under a different section no longer passes (the leaf-name-anywhere bug, finding 10).
func TestParityCatchesNestedCollision(t *testing.T) {
	// Two sections both expose a `x`, but the page documents it only under `a`.
	doc := "## `a`\n| Field | Meaning |\n|---|---|\n| `x` | the a one |\n\n## `b`\n| Field | Meaning |\n|---|---|\n| (todo) | |\n"
	missing := undocumented(doc, map[string]bool{"a.x": true, "b.x": true})
	if len(missing) != 1 || missing[0] != "b.x" {
		t.Fatalf("section-qualified parity must flag exactly b.x, got %v", missing)
	}
	// When each section documents its own copy, nothing is missing.
	ok := doc + "\n## `b`\n| `x` | the b one |\n"
	if m := undocumented(ok, map[string]bool{"a.x": true, "b.x": true}); len(m) != 0 {
		t.Fatalf("both sections documented must be clean, got %v", m)
	}
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
