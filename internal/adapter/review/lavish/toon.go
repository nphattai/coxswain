package lavish

import (
	"strconv"
	"strings"
)

// decodeTOON parses the TOON document lavish-axi prints (there is no --json; axi-sdk-js always encodes with
// @toon-format/toon, verified in the installed adapter). It handles the subset the poll output uses: indentation-based
// nested objects, `key: value` scalars, inline scalar arrays `key[N]: a,b`, tabular arrays `key[N]{c1,c2}:` with CSV
// rows, and list arrays `key[N]:` with `- ` items (whose fields may be scalars or a nested object). Scalars are unquoted
// per TOON's escape rules. Strings never span lines (TOON escapes a newline as \n), so a line-oriented parser is exact.
//
// ponytail: this is the poll shape only, not a general TOON library. It is Go-stdlib-only (no dependency), and its scope
// is documented in docs/adapters/lavish.md. Anything it cannot type stays a string, which is what the adapter records.
func decodeTOON(s string) map[string]any {
	p := &toonParser{}
	for _, raw := range strings.Split(s, "\n") {
		if strings.TrimSpace(raw) == "" {
			continue
		}
		indent := len(raw) - len(strings.TrimLeft(raw, " "))
		text := strings.TrimRight(strings.TrimLeft(raw, " "), " ")
		// Normalize a `- <rest>` list item into a bare `-` marker plus the rest at the item body indent, so the first
		// field on the dash line is parsed like any continuation field.
		if text == "-" {
			p.lines = append(p.lines, tline{indent, "-"})
		} else if rest, ok := strings.CutPrefix(text, "- "); ok {
			p.lines = append(p.lines, tline{indent, "-"}, tline{indent + 2, rest})
		} else {
			p.lines = append(p.lines, tline{indent, text})
		}
	}
	return p.parseObject(0)
}

type tline struct {
	indent int
	text   string
}

type toonParser struct {
	lines []tline
	pos   int
}

func (p *toonParser) peek() (tline, bool) {
	if p.pos < len(p.lines) {
		return p.lines[p.pos], true
	}
	return tline{}, false
}

// parseObject reads key entries at indent == base until a dedent or a list marker.
func (p *toonParser) parseObject(base int) map[string]any {
	obj := map[string]any{}
	for {
		ln, ok := p.peek()
		if !ok || ln.indent < base || ln.text == "-" {
			break
		}
		if ln.indent > base { // defensive: skip an over-indented stray line
			p.pos++
			continue
		}
		key, alen, cols, inline, hasInline, isArray := parseKeyLine(ln.text)
		p.pos++
		switch {
		case isArray:
			obj[key] = p.parseArray(base, alen, cols, inline, hasInline)
		case hasInline:
			obj[key] = scalar(inline)
		case p.childIndent() > base:
			obj[key] = p.parseObject(p.childIndent())
		default:
			obj[key] = "" // key: with no value and no children
		}
	}
	return obj
}

// childIndent returns the indent of the next line, or -1 at end.
func (p *toonParser) childIndent() int {
	if ln, ok := p.peek(); ok {
		return ln.indent
	}
	return -1
}

// parseArray reads the value of an array key. An inline value is a CSV scalar row; a cols header is a tabular array; else
// it is a list of `- ` items.
func (p *toonParser) parseArray(base, alen int, cols, inline string, hasInline bool) []any {
	if hasInline {
		out := []any{}
		for _, f := range splitCSV(inline) {
			out = append(out, scalar(f))
		}
		return out
	}
	if cols != "" {
		names := splitCSV(cols)
		var out []any
		for len(out) < alen {
			ln, ok := p.peek()
			if !ok || ln.indent <= base || ln.text == "-" {
				break
			}
			p.pos++
			fields := splitCSV(ln.text)
			row := map[string]any{}
			for i, n := range names {
				if i < len(fields) {
					row[strings.TrimSpace(n)] = scalar(fields[i])
				}
			}
			out = append(out, row)
		}
		return out
	}
	// List form: items are `-` markers at the child indent.
	ln, ok := p.peek()
	if !ok || ln.indent <= base {
		return []any{}
	}
	return p.parseList(ln.indent)
}

// parseList reads `-` marker items at itemIndent; each item's body is the value block below it.
func (p *toonParser) parseList(itemIndent int) []any {
	var out []any
	for {
		ln, ok := p.peek()
		if !ok || ln.indent != itemIndent || ln.text != "-" {
			break
		}
		p.pos++ // consume the marker
		body, ok := p.peek()
		if !ok || body.indent <= itemIndent {
			out = append(out, "")
			continue
		}
		if isKeyLine(body.text) {
			out = append(out, p.parseObject(body.indent))
		} else {
			p.pos++
			out = append(out, scalar(body.text))
		}
	}
	return out
}

// parseKeyLine splits a `key`, optional `[N]` length, optional `{cols}` header, and an optional inline value off a line.
func parseKeyLine(text string) (key string, alen int, cols, inline string, hasInline, isArray bool) {
	alen = -1
	idx := strings.IndexByte(text, ':')
	if idx < 0 {
		return text, alen, "", "", false, false
	}
	head := text[:idx]
	rest := text[idx+1:]
	if rest != "" {
		inline = strings.TrimPrefix(rest, " ")
		hasInline = inline != ""
	}
	if i := strings.LastIndexByte(head, '{'); i >= 0 && strings.HasSuffix(head, "}") {
		cols = head[i+1 : len(head)-1]
		head = head[:i]
	}
	if i := strings.LastIndexByte(head, '['); i >= 0 && strings.HasSuffix(head, "]") {
		if n, err := strconv.Atoi(head[i+1 : len(head)-1]); err == nil {
			alen = n
			isArray = true
			head = head[:i]
		}
	}
	return unquote(head), alen, cols, inline, hasInline, isArray
}

// isKeyLine reports whether a line looks like a `key:` entry (so a list item body is an object, not a scalar).
func isKeyLine(text string) bool {
	idx := strings.IndexByte(text, ':')
	if idx <= 0 {
		return false
	}
	head := text[:idx]
	for _, r := range head {
		if !(r == '_' || r == '-' || r == ' ' || r == '[' || r == ']' || r == '{' || r == '}' || r == '"' ||
			(r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')) {
			return false
		}
	}
	return true
}

// scalar converts a TOON scalar token to a Go value: an unquoted string, a bool, or nil for null. Numbers stay strings
// (the adapter only reads string and bool fields).
func scalar(tok string) any {
	tok = strings.TrimSpace(tok)
	switch tok {
	case "true":
		return true
	case "false":
		return false
	case "null":
		return nil
	}
	return unquote(tok)
}

// unquote removes a wrapping double-quote and applies TOON's string escapes.
func unquote(s string) string {
	if len(s) < 2 || s[0] != '"' || s[len(s)-1] != '"' {
		return s
	}
	inner := s[1 : len(s)-1]
	var b strings.Builder
	for i := 0; i < len(inner); i++ {
		if inner[i] == '\\' && i+1 < len(inner) {
			i++
			switch inner[i] {
			case 'n':
				b.WriteByte('\n')
			case 't':
				b.WriteByte('\t')
			case 'r':
				b.WriteByte('\r')
			case '"':
				b.WriteByte('"')
			case '\\':
				b.WriteByte('\\')
			case '/':
				b.WriteByte('/')
			default:
				b.WriteByte(inner[i])
			}
			continue
		}
		b.WriteByte(inner[i])
	}
	return b.String()
}

// splitCSV splits a TOON delimited row on commas, honoring double-quoted fields (a comma inside quotes is literal). Each
// field is returned raw (with its quotes) for scalar() to unquote, so escape handling stays in one place.
func splitCSV(s string) []string {
	var fields []string
	var cur strings.Builder
	inQuote := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '"':
			inQuote = !inQuote
			cur.WriteByte(c)
		case c == '\\' && inQuote && i+1 < len(s):
			cur.WriteByte(c)
			i++
			cur.WriteByte(s[i])
		case c == ',' && !inQuote:
			fields = append(fields, cur.String())
			cur.Reset()
		default:
			cur.WriteByte(c)
		}
	}
	fields = append(fields, cur.String())
	return fields
}
