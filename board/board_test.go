package board

import (
	"strings"
	"testing"
)

// Render inlines css and js, embeds the snapshot, and HTML-escapes the data so a note cannot break out of the script
// tag. The page never contains a form (read-only) or an external fetch.
func TestRenderSelfContained(t *testing.T) {
	data := map[string]any{
		"epic":            "v2",
		"pending_captain": []string{"note with </script> injection attempt"},
	}
	page, err := Render(data)
	if err != nil {
		t.Fatal(err)
	}
	s := string(page)
	if !strings.Contains(s, ":root {") {
		t.Error("css was not inlined")
	}
	if !strings.Contains(s, "__BOARD_DATA__") {
		t.Error("data was not embedded")
	}
	if !strings.Contains(s, `"epic":"v2"`) {
		t.Error("snapshot json not embedded")
	}
	if strings.Contains(s, "<form") {
		t.Error("board must have no form (read-only)")
	}
	// The literal </script> from the data must be escaped so it cannot close the embedding script tag.
	if strings.Contains(s, "injection attempt</script>") {
		t.Error("</script> in data was not escaped")
	}
	if !strings.Contains(s, `</script>`) {
		t.Error("expected the data's </script> to be unicode-escaped by json.Marshal")
	}
	// No external resource: the only fetch is same-origin and relative.
	for _, bad := range []string{`fetch("http`, `fetch('http`, "src=\"http", "href=\"http"} {
		if strings.Contains(s, bad) {
			t.Errorf("page loads an external resource: %q", bad)
		}
	}
}
