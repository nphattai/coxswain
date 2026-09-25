package main

import (
	"flag"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/nphattai/coxswain/board"
	"github.com/nphattai/coxswain/internal/arena/synth"
	"github.com/nphattai/coxswain/internal/artifact"
	"github.com/nphattai/coxswain/internal/protocol/inbox"
	"github.com/nphattai/coxswain/internal/scorecard"
	"github.com/nphattai/coxswain/internal/state"
	"github.com/nphattai/coxswain/internal/wake"
)

// cmdBoard implements `cox board --epic <dir> --out <file> | --serve :port`. The board is read-only (ADR 0011): --out
// writes a self-contained snapshot HTML that opens with no network, and --serve serves that page plus a same-origin
// /data.json the page refreshes every 10s. It renders state only; it never mutates anything.
func cmdBoard(args []string) int {
	fs := flag.NewFlagSet("board", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	epicDir := epicFlag(fs, "", "epic directory")
	out := fs.String("out", "", "write a self-contained HTML snapshot to this file")
	serve := fs.String("serve", "", "serve the board on this address (e.g. :8787)")
	noForge := fs.Bool("no-forge", false, "skip the GitHub forge probe (pr/checks stay unknown)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *epicDir == "" || (*out == "" && *serve == "") {
		return usageErr("cox board --epic <dir> (--out <file> | --serve :port) [--no-forge]")
	}
	if *serve != "" {
		if err := http.ListenAndServe(*serve, boardMux(*epicDir, *noForge)); err != nil {
			return fail("board serve: %v", err)
		}
		return 0
	}
	data, err := buildBoardData(*epicDir, *noForge)
	if err != nil {
		return fail("%v", err)
	}
	page, err := board.Render(data)
	if err != nil {
		return fail("%v", err)
	}
	if err := os.WriteFile(*out, page, 0o644); err != nil {
		return fail("write board: %v", err)
	}
	// A board snapshot is a coxswain.artifact.v1 (kind=board) so cox review can open it and cox artifact list sees it.
	// The board is rendered from live state, not a source file, so it carries no sources.
	if err := artifact.Write(*out, artifact.Sidecar{
		Kind: artifact.KindBoard, Generator: artifact.GenCox,
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		return fail("write board sidecar: %v", err)
	}
	fmt.Printf("wrote board snapshot to %s\n", *out)
	return 0
}

// boardMux is the read-only HTTP surface: GET / renders the page and GET /data.json returns the snapshot. Every other
// method is 405 (the board never accepts a write), and every other path is 404.
func boardMux(epicDir string, noForge bool) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/data.json", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "read-only board", http.StatusMethodNotAllowed)
			return
		}
		data, err := buildBoardData(epicDir, noForge)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		b, err := board.DataJSON(data)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(b)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodGet {
			http.Error(w, "read-only board", http.StatusMethodNotAllowed)
			return
		}
		data, err := buildBoardData(epicDir, noForge)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		page, err := board.Render(data)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(page)
	})
	return mux
}

// boardData is the coxswain.board.v1 snapshot the page renders.
type boardData struct {
	Schema      string       `json:"schema"`
	GeneratedAt string       `json:"generated_at"`
	Epic        string       `json:"epic"`
	Stories     []boardStory `json:"stories"`
	Arena       boardArena   `json:"arena"`
	LatestWake  *boardWake   `json:"latest_wake"`
	Pending     []string     `json:"pending_captain"`
	Quota       []boardQuota `json:"quota"`
}

// boardQuota is one merged quota reading row rendered in the read-only Quota panel (M11, observe-only).
type boardQuota struct {
	Harness string `json:"harness"`
	Model   string `json:"model,omitempty"`
	Known   bool   `json:"known"`
	Percent int    `json:"percent_remaining"`
	Runway  string `json:"runway"`
	Source  string `json:"source"`
	Reason  string `json:"reason,omitempty"`
}

type boardStory struct {
	ID               string              `json:"id"`
	State            string              `json:"state"`
	Attempt          int                 `json:"attempt"`
	Liveness         string              `json:"liveness"`
	Composer         string              `json:"composer"`
	Forge            string              `json:"forge"`
	Attempts         []scorecard.Attempt `json:"attempts"`
	SteerUsed        int                 `json:"steer_used"`
	SteerBudget      int                 `json:"steer_budget"`
	QuestionsPending int                 `json:"questions_pending"`
	UnackedWakes     int                 `json:"unacked_wakes"`
}

type boardArena struct {
	Round          int      `json:"round"`
	Round2         bool     `json:"round2"`
	Round2Reasons  []string `json:"round2_reasons"`
	DesignSigned   bool     `json:"design_signed"`
	SynthesisReady bool     `json:"synthesis_ready"`
}

type boardWake struct {
	Gen   int    `json:"gen"`
	Kind  string `json:"kind"`
	Story string `json:"story"`
	Note  string `json:"note"`
	TS    string `json:"ts"`
}

// buildBoardData aggregates the read-only snapshot from the fleet view, per-attempt scorecard, wake queue, inbox
// budget, and arena state. Every sub-read is best-effort: a source that fails leaves its field empty/unknown rather
// than failing the whole board (the board must render even when the forge or a session log is unavailable).
func buildBoardData(epicDir string, noForge bool) (boardData, error) {
	now := time.Now().UTC()
	fleet, _, err := buildFleet(epicDir, "", now, noForge)
	if err != nil {
		return boardData{}, err
	}

	// Per-attempt scorecard, keyed by story (best-effort: a scorecard failure leaves attempts empty).
	byStory := map[string][]scorecard.Attempt{}
	if card, err := scorecard.Build(epicDir, "", claudeUsageReader(epicDir), forgeChecksReader(epicDir, noForge)); err == nil {
		for _, s := range card.Stories {
			byStory[s.ID] = s.Attempts
		}
	}

	// Unacked wakes: count per story, and the latest overall.
	unacked, _ := wake.Drain(epicDir, true)
	var latest *boardWake
	perStoryWakes := map[string]int{}
	perStoryQuestions := map[string]int{}
	for i := range unacked {
		w := unacked[i]
		perStoryWakes[w.Story]++
		if w.Kind == "question" || w.Kind == "input_required" {
			perStoryQuestions[w.Story]++
		}
		latest = &boardWake{Gen: w.Gen, Kind: string(w.Kind), Story: w.Story, Note: w.Note, TS: w.TS}
	}

	stories := make([]boardStory, 0, len(fleet.Stories))
	for _, s := range fleet.Stories {
		used := 0
		if recs, err := inbox.All(epicDir, s.ID); err == nil {
			for _, r := range recs {
				if r.Urgency == inbox.Steer {
					used++
				}
			}
		}
		stories = append(stories, boardStory{
			ID:               s.ID,
			State:            string(s.State),
			Attempt:          s.Attempt,
			Liveness:         obsString(s.Observations["liveness"]),
			Composer:         obsString(s.Observations["composer"]),
			Forge:            forgeSummary(s.Observations["forge"]),
			Attempts:         byStory[s.ID],
			SteerUsed:        used,
			SteerBudget:      inbox.DefaultBudget,
			QuestionsPending: perStoryQuestions[s.ID],
			UnackedWakes:     perStoryWakes[s.ID],
		})
	}

	var quotaRows []boardQuota
	for _, r := range mergedQuotaReadings(epicDir) {
		quotaRows = append(quotaRows, boardQuota{
			Harness: r.Harness, Model: r.Model, Known: r.Known,
			Percent: r.PercentRemaining, Runway: r.Runway, Source: r.Source, Reason: r.Reason,
		})
	}

	arena := buildBoardArena(epicDir)
	return boardData{
		Schema:      "coxswain.board.v1",
		GeneratedAt: fleet.GeneratedAt,
		Epic:        fleet.Epic,
		Stories:     stories,
		Arena:       arena,
		LatestWake:  latest,
		Pending:     pendingCaptain(epicDir, arena),
		Quota:       quotaRows,
	}, nil
}

// obsString renders an observation value as a string ("unknown" when absent).
func obsString(o state.Observation) string {
	if o.Value == nil {
		return "unknown"
	}
	if s, ok := o.Value.(string); ok {
		return s
	}
	return fmt.Sprint(o.Value)
}

var roundReportRe = regexp.MustCompile(`^round-(\d+)-[a-z]+\.md$`)

// buildBoardArena reads the arena state from the epic's reports and event log: the highest round with reports, whether
// a second round is required (from synthesis.md), and whether the design has been signed.
func buildBoardArena(epicDir string) boardArena {
	var a boardArena
	dir := filepath.Join(epicDir, "reports", "arena")
	if entries, err := os.ReadDir(dir); err == nil {
		for _, e := range entries {
			if m := roundReportRe.FindStringSubmatch(e.Name()); m != nil {
				if n := atoiSafe(m[1]); n > a.Round {
					a.Round = n
				}
			}
		}
	}
	if b, err := os.ReadFile(filepath.Join(dir, "synthesis.md")); err == nil {
		a.SynthesisReady = true
		a.Round2, a.Round2Reasons = synth.Round2(string(b))
	}
	if events, _, err := state.Load(epicDir); err == nil {
		for _, ev := range events {
			if ev.Type == state.DesignSigned {
				a.DesignSigned = true
			}
			if ev.Type == state.DesignAmended {
				a.DesignSigned = false // an amend re-opens the signature (P7)
			}
		}
	}
	return a
}

// pendingCaptain lists the decisions waiting on the captain: an unsigned but ready design, a required arena round 2,
// and any non-empty "Open questions" section in a story handoff.
//
// ponytail: open questions are read by a heading scan of the handoff markdown (## Open questions ... to the next ##),
// which is the format the checkpoints already use. If checkpoints move to structured fields, read those instead.
func pendingCaptain(epicDir string, a boardArena) []string {
	var out []string
	if a.SynthesisReady && !a.DesignSigned {
		out = append(out, "design awaiting captain signature (synthesis ready)")
	}
	if a.Round2 {
		out = append(out, "arena round 2 required: "+strings.Join(a.Round2Reasons, "; "))
	}
	handoffs := filepath.Join(epicDir, "handoffs")
	entries, err := os.ReadDir(handoffs)
	if err != nil {
		return out
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(handoffs, e.Name()))
		if err != nil {
			continue
		}
		if q := openQuestions(string(b)); q != "" {
			out = append(out, fmt.Sprintf("open question (%s): %s", strings.TrimSuffix(e.Name(), ".md"), q))
		}
	}
	return out
}

// openQuestions returns the first meaningful line of a checkpoint's "## Open questions" section, or "" when the section
// is absent or says none. It reads to the next "## " heading or the end of the file.
func openQuestions(md string) string {
	lines := strings.Split(md, "\n")
	in := false
	for _, raw := range lines {
		line := strings.TrimSpace(raw)
		if strings.HasPrefix(line, "## ") {
			in = strings.EqualFold(strings.TrimSpace(strings.TrimPrefix(line, "## ")), "open questions")
			continue
		}
		if !in || line == "" {
			continue
		}
		low := strings.ToLower(line)
		if strings.HasPrefix(low, "none") {
			return ""
		}
		return line // first non-empty content line of the section
	}
	return ""
}

func atoiSafe(s string) int {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return n
		}
		n = n*10 + int(c-'0')
	}
	return n
}
