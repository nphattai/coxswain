package arena

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// reportFenceRe captures the body of a ```report ... ``` fenced block (the headless role's report). (?s) lets `.` span
// newlines; the body is non-greedy so it stops at the first closing fence.
var reportFenceRe = regexp.MustCompile("(?s)```report\\s*\n(.*?)\n```")

// extractReport pulls the report markdown out of a harness's headless stdout. Claude's --output-format json is a single
// object with a "result" string; Codex's exec --json is JSONL, so the text is spread across events. collectText handles
// both by decoding and concatenating every string value, which unescapes the JSON so the ```report block's newlines are
// real. It returns an error when no report block is present.
func extractReport(stdout []byte) (string, error) {
	text := collectText(stdout)
	m := reportFenceRe.FindStringSubmatch(text)
	if m == nil {
		return "", fmt.Errorf("no ```report block in headless output")
	}
	return m[1], nil
}

// collectText turns harness JSON stdout into plain text. It tries a single JSON object first (claude: use "result" when
// present, else every string value), then JSONL (codex: every string value per line), then falls back to the raw bytes.
func collectText(stdout []byte) string {
	trimmed := strings.TrimSpace(string(stdout))
	if trimmed == "" {
		return ""
	}
	var obj map[string]any
	if json.Unmarshal([]byte(trimmed), &obj) == nil {
		if r, ok := obj["result"].(string); ok {
			return r
		}
		return strings.Join(stringValues(obj), "\n")
	}
	var sb strings.Builder
	decoded := false
	for _, line := range strings.Split(trimmed, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var v any
		if json.Unmarshal([]byte(line), &v) == nil {
			decoded = true
			for _, s := range stringValues(v) {
				sb.WriteString(s)
				sb.WriteByte('\n')
			}
		}
	}
	if decoded {
		return sb.String()
	}
	return trimmed
}

// stringValues walks a decoded JSON value and returns every string it contains, in a stable order (map keys sorted), so
// the ```report block a harness nested in some field is recovered wherever it sits.
func stringValues(v any) []string {
	var out []string
	switch t := v.(type) {
	case string:
		out = append(out, t)
	case []any:
		for _, e := range t {
			out = append(out, stringValues(e)...)
		}
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			out = append(out, stringValues(t[k])...)
		}
	}
	return out
}
