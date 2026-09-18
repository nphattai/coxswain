package artifact

import (
	"strings"
	"testing"
)

func TestMarkdownBlocks(t *testing.T) {
	src := "# Title\n\nA para with `code` and [link](https://x) and **bold**.\n\n" +
		"- one\n- two\n\n1. first\n2. second\n\n| a | b |\n|---|---|\n| 1 | 2 |\n\n```\nraw code\n```\n"
	out := Markdown(src)
	for _, want := range []string{
		"<h1>Title</h1>", "<code>code</code>", `<a href="https://x">link</a>`, "<strong>bold</strong>",
		"<ul>", "<li>one</li>", "<ol>", "<li>first</li>", "<table>", "<th>a</th>", "<td>1</td>",
		"<pre><code>raw code</code></pre>",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("markdown missing %q in:\n%s", want, out)
		}
	}
}

func TestMarkdownEscapesHTML(t *testing.T) {
	out := Markdown("A <script>alert(1)</script> paragraph.")
	if strings.Contains(out, "<script>") {
		t.Fatalf("raw <script> survived escaping: %s", out)
	}
	if !strings.Contains(out, "&lt;script&gt;") {
		t.Fatalf("expected escaped script tag: %s", out)
	}
}

func TestMarkdownDropsJavascriptLink(t *testing.T) {
	out := Markdown("[click](javascript:alert(1))")
	if strings.Contains(strings.ToLower(out), "href=\"javascript:") {
		t.Fatalf("javascript: link should be dropped: %s", out)
	}
}
