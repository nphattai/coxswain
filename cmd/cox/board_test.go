package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The board data reflects the fleet, and the served /data.json is valid JSON while / is a self-contained read-only page.
func TestBoardHandlerAndData(t *testing.T) {
	t.Setenv("ORCA_RUN_ID", "") // no live backend: liveness/composer stay unknown
	epic := t.TempDir()
	writeStory(t, epic, "s1", "web")
	appendWorking(t, epic, "s1")

	data, err := buildBoardData(epic, true) // --no-forge
	if err != nil {
		t.Fatal(err)
	}
	if data.Schema != "coxswain.board.v1" || len(data.Stories) != 1 || data.Stories[0].ID != "s1" {
		t.Fatalf("board data wrong: %+v", data)
	}
	if data.Stories[0].SteerBudget != 5 {
		t.Fatalf("steer budget = %d, want 5", data.Stories[0].SteerBudget)
	}

	srv := httptest.NewServer(boardMux(epic, true))
	defer srv.Close()

	// /data.json is valid JSON.
	resp, err := http.Get(srv.URL + "/data.json")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("/data.json status = %d", resp.StatusCode)
	}
	var got boardData
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("/data.json is not valid JSON: %v", err)
	}

	// / is html; POST is refused (read-only); an unknown path is 404.
	if r, _ := http.Get(srv.URL + "/"); r == nil || r.StatusCode != 200 {
		t.Fatal("GET / must be 200")
	}
	if r, _ := http.Post(srv.URL+"/", "text/plain", nil); r == nil || r.StatusCode != http.StatusMethodNotAllowed {
		t.Fatal("POST / must be 405 (read-only board)")
	}
	if r, _ := http.Get(srv.URL + "/nope"); r == nil || r.StatusCode != 404 {
		t.Fatal("GET /nope must be 404")
	}
}

// pendingCaptain surfaces an unsigned-but-ready design and a handoff's open questions, and skips a "None" section.
func TestPendingCaptainOpenQuestions(t *testing.T) {
	epic := t.TempDir()
	handoffs := filepath.Join(epic, "handoffs")
	if err := os.MkdirAll(handoffs, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(handoffs, "s1.md"), []byte("## Verified\nok\n\n## Open questions\nShould we split the API?\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(handoffs, "s2.md"), []byte("## Open questions\nNone blocking.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := pendingCaptain(epic, boardArena{SynthesisReady: true, DesignSigned: false})
	joined := strings.Join(got, "\n")
	if !strings.Contains(joined, "design awaiting captain signature") {
		t.Errorf("missing design-sign pending: %v", got)
	}
	if !strings.Contains(joined, "open question (s1): Should we split the API?") {
		t.Errorf("missing s1 open question: %v", got)
	}
	if strings.Contains(joined, "s2") {
		t.Errorf("s2 says None and must be skipped: %v", got)
	}
}
