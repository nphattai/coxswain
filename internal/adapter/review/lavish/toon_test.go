package lavish

import "testing"

// The two fixtures are verbatim @toon-format/toon output (captured from the installed axi-sdk-js encoder), so the
// decoder is tested against the real wire format, not a guess.

func TestDecodeTabular(t *testing.T) {
	toon := "prompts[1]{tag,text,message,target}:\n" +
		"  answer,\"line one\\nline two: with colon\",\"has, comma and \\\"quote\\\"\",#a\n"
	got := decodeTOON(toon)
	prompts, ok := got["prompts"].([]any)
	if !ok || len(prompts) != 1 {
		t.Fatalf("prompts: %#v", got["prompts"])
	}
	p := prompts[0].(map[string]any)
	if p["tag"] != "answer" {
		t.Errorf("tag = %v", p["tag"])
	}
	if p["text"] != "line one\nline two: with colon" {
		t.Errorf("text = %q", p["text"])
	}
	if p["message"] != `has, comma and "quote"` {
		t.Errorf("message = %q", p["message"])
	}
	if p["target"] != "#a" {
		t.Errorf("target = %v", p["target"])
	}
}

func TestDecodeListWithSessionEnd(t *testing.T) {
	toon := `session:
  file: /x/a.html
  status: feedback
  session_ended: true
  ended_by: user
prompts[2]:
  - id: p1
    tag: answer
    target: #claim-adversary-1-1
    quoted_text: Option C cannot
    text: answer adversary-1-1 yes
    message: looks fine
  - id: p2
    tag: comment
    target: .foo
    text: make this bigger
next_step: do things
dom_snapshot: <html>
`
	got := decodeTOON(toon)
	sess := got["session"].(map[string]any)
	if sess["status"] != "feedback" || sess["file"] != "/x/a.html" {
		t.Fatalf("session: %#v", sess)
	}
	if sess["session_ended"] != true || sess["ended_by"] != "user" {
		t.Fatalf("session end fields: %#v", sess)
	}
	prompts := got["prompts"].([]any)
	if len(prompts) != 2 {
		t.Fatalf("want 2 prompts, got %d: %#v", len(prompts), prompts)
	}
	p0 := prompts[0].(map[string]any)
	if p0["id"] != "p1" || p0["tag"] != "answer" || p0["text"] != "answer adversary-1-1 yes" || p0["message"] != "looks fine" {
		t.Errorf("prompt0: %#v", p0)
	}
	p1 := prompts[1].(map[string]any)
	if p1["id"] != "p2" || p1["tag"] != "comment" || p1["target"] != ".foo" {
		t.Errorf("prompt1: %#v", p1)
	}
}

func TestDecodeNestedTargetObject(t *testing.T) {
	// A layout-warnings prompt carries a nested target object; the list form keeps it as a nested map.
	toon := `prompts[1]:
  - tag: layout-warnings
    target:
      type: layout
      count: 3
    text: fix overflow
`
	got := decodeTOON(toon)
	p := got["prompts"].([]any)[0].(map[string]any)
	tgt, ok := p["target"].(map[string]any)
	if !ok || tgt["type"] != "layout" {
		t.Fatalf("nested target: %#v", p["target"])
	}
}
