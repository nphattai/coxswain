// Package board renders the read-only captain board (ADR 0011): one self-contained HTML page with its CSS and JS
// inlined and the current snapshot embedded, so it opens with no network and loads no external resource. It renders
// state only - there is no form, button, or POST anywhere in the page; every action stays in the leader chat.
package board

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
)

//go:embed board.html
var htmlTpl string

//go:embed board.css
var css string

// CSS is the board stylesheet, exported so M13 visual artifacts share it (DESIGN: generators share the board CSS so a
// cox-generated page and a leader-authored one do not drift in look).
var CSS = css

//go:embed board.js
var js string

// markers in board.html that Render replaces with the inlined css, the embedded snapshot JSON, and the inlined js.
const (
	cssMark  = "/*BOARD_CSS*/"
	dataMark = "/*BOARD_DATA*/"
	jsMark   = "/*BOARD_JS*/"
)

// Render returns the self-contained board HTML with data embedded as window.__BOARD_DATA__. data is marshalled with
// HTML escaping on (the default), so a note containing </script> is neutralised and cannot break out of the script tag.
func Render(data any) ([]byte, error) {
	raw, err := json.Marshal(data) // HTML-escaping default: <, >, & become < etc.
	if err != nil {
		return nil, fmt.Errorf("marshal board data: %w", err)
	}
	out := htmlTpl
	out = replaceOnce(out, cssMark, css)
	out = replaceOnce(out, dataMark, string(raw))
	out = replaceOnce(out, jsMark, js)
	return []byte(out), nil
}

// DataJSON returns the snapshot as indented JSON for the /data.json endpoint.
func DataJSON(data any) ([]byte, error) {
	return json.MarshalIndent(data, "", "  ")
}

// replaceOnce replaces the first occurrence of mark in s with val; if the marker is absent the template is returned
// unchanged (a missing marker is caught by the board tests, which assert the rendered page carries css, data, and js).
func replaceOnce(s, mark, val string) string {
	i := bytes.Index([]byte(s), []byte(mark))
	if i < 0 {
		return s
	}
	return s[:i] + val + s[i+len(mark):]
}
