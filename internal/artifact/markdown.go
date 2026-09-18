package artifact

import (
	"fmt"
	"html"
	"regexp"
	"strings"
)

// Markdown is a deliberately minimal, stdlib-only markdown-to-HTML renderer for the review pages (DESIGN: markdown ->
// HTML by stdlib, renderer tối giản). It handles exactly what the epic files use: ATX headings (# .. ######), fenced
// code blocks (```), GFM pipe tables, unordered (-, *) and ordered (1.) lists, blockquotes (>), horizontal rules, and
// paragraphs, with inline `code`, [text](url) links, and **bold**. Everything is HTML-escaped first, so no source text
// can inject markup.
//
// ponytail: known ceiling - no nested lists, no inline images, no reference links, no HTML passthrough, no setext
// headings. The epic's DESIGN/plan/synthesis files use none of these; extend the renderer (not a dependency) if one
// appears. This is documented in skills/cox-visualize and docs/review.md.
func Markdown(src string) string {
	lines := strings.Split(src, "\n")
	var b strings.Builder
	i := 0
	for i < len(lines) {
		line := lines[i]
		trimmed := strings.TrimSpace(line)

		switch {
		case strings.HasPrefix(trimmed, "```"):
			// Fenced code block: verbatim until the closing fence (or end of input).
			i++
			var code []string
			for i < len(lines) && !strings.HasPrefix(strings.TrimSpace(lines[i]), "```") {
				code = append(code, lines[i])
				i++
			}
			i++ // consume the closing fence
			b.WriteString("<pre><code>" + html.EscapeString(strings.Join(code, "\n")) + "</code></pre>\n")

		case isTableHeader(lines, i):
			i = renderTable(&b, lines, i)

		case headingLevel(trimmed) > 0:
			n := headingLevel(trimmed)
			text := strings.TrimSpace(trimmed[n:])
			fmt.Fprintf(&b, "<h%d>%s</h%d>\n", n, inline(text), n)
			i++

		case trimmed == "---" || trimmed == "***" || trimmed == "___":
			b.WriteString("<hr>\n")
			i++

		case isListItem(trimmed):
			i = renderList(&b, lines, i)

		case strings.HasPrefix(trimmed, ">"):
			var quote []string
			for i < len(lines) && strings.HasPrefix(strings.TrimSpace(lines[i]), ">") {
				quote = append(quote, strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(lines[i]), ">")))
				i++
			}
			b.WriteString("<blockquote>" + inline(strings.Join(quote, " ")) + "</blockquote>\n")

		case trimmed == "":
			i++

		default:
			// Paragraph: gather consecutive non-blank, non-structural lines.
			var para []string
			for i < len(lines) {
				t := strings.TrimSpace(lines[i])
				if t == "" || headingLevel(t) > 0 || isListItem(t) || strings.HasPrefix(t, "```") || strings.HasPrefix(t, ">") || isTableHeader(lines, i) {
					break
				}
				para = append(para, t)
				i++
			}
			b.WriteString("<p>" + inline(strings.Join(para, " ")) + "</p>\n")
		}
	}
	return b.String()
}

func headingLevel(t string) int {
	n := 0
	for n < len(t) && t[n] == '#' {
		n++
	}
	if n >= 1 && n <= 6 && n < len(t) && t[n] == ' ' {
		return n
	}
	return 0
}

func isListItem(t string) bool {
	return strings.HasPrefix(t, "- ") || strings.HasPrefix(t, "* ") || orderedItem(t) != ""
}

var orderedRe = regexp.MustCompile(`^(\d+)\.\s+(.*)$`)

// orderedItem returns the content of an ordered list item, or "" when the line is not one.
func orderedItem(t string) string {
	if m := orderedRe.FindStringSubmatch(t); m != nil {
		return m[2]
	}
	return ""
}

// renderList consumes a run of list items of one kind (ordered or unordered) and returns the next index.
func renderList(b *strings.Builder, lines []string, i int) int {
	ordered := orderedItem(strings.TrimSpace(lines[i])) != ""
	tag := "ul"
	if ordered {
		tag = "ol"
	}
	fmt.Fprintf(b, "<%s>\n", tag)
	for i < len(lines) {
		t := strings.TrimSpace(lines[i])
		if !isListItem(t) || (orderedItem(t) != "") != ordered {
			break
		}
		var item string
		if ordered {
			item = orderedItem(t)
		} else {
			item = strings.TrimSpace(t[2:])
		}
		b.WriteString("<li>" + inline(item) + "</li>\n")
		i++
	}
	fmt.Fprintf(b, "</%s>\n", tag)
	return i
}

// isTableHeader reports whether the line at i is a GFM table header (a pipe row immediately followed by a separator row
// of dashes and optional colons).
func isTableHeader(lines []string, i int) bool {
	if i+1 >= len(lines) {
		return false
	}
	if !strings.Contains(lines[i], "|") {
		return false
	}
	sep := strings.TrimSpace(lines[i+1])
	if !strings.Contains(sep, "|") && !strings.HasPrefix(sep, "-") {
		return false
	}
	for _, c := range tableCells(sep) {
		if strings.Trim(strings.TrimSpace(c), "-: ") != "" {
			return false
		}
	}
	return len(tableCells(sep)) > 0
}

// renderTable renders a GFM pipe table starting at the header row and returns the next index.
func renderTable(b *strings.Builder, lines []string, i int) int {
	header := tableCells(lines[i])
	i += 2 // header + separator
	b.WriteString("<table>\n<thead><tr>")
	for _, h := range header {
		b.WriteString("<th>" + inline(strings.TrimSpace(h)) + "</th>")
	}
	b.WriteString("</tr></thead>\n<tbody>\n")
	for i < len(lines) && strings.Contains(lines[i], "|") && strings.TrimSpace(lines[i]) != "" {
		cells := tableCells(lines[i])
		b.WriteString("<tr>")
		for _, c := range cells {
			b.WriteString("<td>" + inline(strings.TrimSpace(c)) + "</td>")
		}
		b.WriteString("</tr>\n")
		i++
	}
	b.WriteString("</tbody>\n</table>\n")
	return i
}

// tableCells splits a pipe-table row into cells, dropping the empty edges from a leading/trailing pipe.
func tableCells(line string) []string {
	s := strings.TrimSpace(line)
	s = strings.TrimPrefix(s, "|")
	s = strings.TrimSuffix(s, "|")
	return strings.Split(s, "|")
}

var (
	inlineCodeRe = regexp.MustCompile("`([^`]+)`")
	linkRe       = regexp.MustCompile(`\[([^\]]+)\]\(([^)]+)\)`)
	boldRe       = regexp.MustCompile(`\*\*([^*]+)\*\*`)
)

// inline renders the inline subset (code, links, bold) over already-escaped text. It escapes first so no source markup
// survives, then unescapes only inside the spans it emits by re-inserting escaped content. Backtick code is handled
// before links/bold so a URL or asterisk inside code is not re-processed.
func inline(s string) string {
	esc := html.EscapeString(s)
	// Inline code: escape the inner text again is unnecessary (already escaped); wrap in <code>.
	esc = inlineCodeRe.ReplaceAllString(esc, "<code>$1</code>")
	// Links: [text](url) - href is escaped text already; guard javascript: schemes.
	esc = linkRe.ReplaceAllStringFunc(esc, func(m string) string {
		sm := linkRe.FindStringSubmatch(m)
		href := sm[2]
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(html.UnescapeString(href))), "javascript:") {
			return sm[1] // drop an unsafe scheme, keep the text
		}
		return fmt.Sprintf(`<a href="%s">%s</a>`, href, sm[1])
	})
	esc = boldRe.ReplaceAllString(esc, "<strong>$1</strong>")
	return esc
}
