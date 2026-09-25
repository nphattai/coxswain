package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/nphattai/coxswain/internal/adapter/forge"
	"github.com/nphattai/coxswain/internal/adapter/forge/github"
	"github.com/nphattai/coxswain/internal/scorecard"
	"github.com/nphattai/coxswain/internal/state"
)

// cmdScorecard implements `cox scorecard --epic <dir> [--story <id>] [--json]`. It reports per-attempt effectiveness
// metrics (never merging attempts) from events.jsonl, the inbox, the wake queue, and, for a claude worker, its session
// log. A metric whose source is absent prints "unknown", never 0.
func cmdScorecard(args []string) int {
	fs := flag.NewFlagSet("scorecard", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	epicDir := epicFlag(fs, "", "epic directory")
	story := fs.String("story", "", "one story id (default: every dispatched story)")
	asJSON := fs.Bool("json", false, "emit coxswain.scorecard.v1 JSON")
	noForge := fs.Bool("no-forge", false, "skip the GitHub forge probe (ci_wall_incl_queue_s stays unknown)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *epicDir == "" {
		return usageErr("cox scorecard --epic <dir> [--story <id>] [--json] [--no-forge]")
	}
	card, err := scorecard.Build(*epicDir, *story, claudeUsageReader(*epicDir), forgeChecksReader(*epicDir, *noForge))
	if err != nil {
		return fail("%v", err)
	}
	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(card); err != nil {
			return fail("%v", err)
		}
		return 0
	}
	printScorecard(card)
	return 0
}

// printScorecard renders the human table: one block per story, one line per attempt, unknown for any nil metric.
func printScorecard(card scorecard.Card) {
	fmt.Printf("epic %s (%s)\n", card.Epic, card.GeneratedAt)
	for _, s := range card.Stories {
		fmt.Printf("  %s\n", s.ID)
		for _, a := range s.Attempts {
			fmt.Printf("    attempt %d  working=%s parked=%s  steers=%s questions=%s resumes=%s  tokens_in=%s tokens_out=%s cost_usd=%s  ci_wall_incl_queue=%s\n",
				a.Attempt, dur(a.WallWorkingS), dur(a.WallParkedS), i(a.Steers), i(a.Questions), i(a.Resumes),
				i(a.TokensIn), i(a.TokensOut), f(a.CostUSD), dur(a.CIWallInclQueueS))
		}
	}
}

func i(p *int) string {
	if p == nil {
		return "unknown"
	}
	return fmt.Sprintf("%d", *p)
}

func f(p *float64) string {
	if p == nil {
		return "unknown"
	}
	return fmt.Sprintf("%.2f", *p)
}

// dur renders a seconds pointer as "Nm" / "Hh MMm", or "unknown" when nil.
func dur(p *int) string {
	if p == nil {
		return "unknown"
	}
	s := *p
	h, m := s/3600, (s%3600)/60
	if h > 0 {
		return fmt.Sprintf("%dh%02dm", h, m)
	}
	return fmt.Sprintf("%dm", m)
}

// claudeModelPrice is the list price per MTok for a model prefix (ported from bin/session-cost.py PRICES). inp = fresh
// input, out = output, cr = cache read, c5 = 5-minute cache write, c1 = 1-hour cache write.
type claudeModelPrice struct{ inp, out, cr, c5, c1 float64 }

var claudePrices = []struct {
	prefix string
	p      claudeModelPrice
}{
	{"claude-fable-5-1", claudeModelPrice{10.0, 50.0, 0.25, 12.5, 20.0}},
	{"claude-fable-5", claudeModelPrice{10.0, 50.0, 1.0, 12.5, 20.0}},
	{"claude-opus", claudeModelPrice{5.0, 25.0, 0.5, 6.25, 10.0}},
	{"claude-haiku", claudeModelPrice{1.0, 5.0, 0.1, 1.25, 2.0}},
}

func priceFor(model string) claudeModelPrice {
	for _, e := range claudePrices {
		if strings.HasPrefix(model, e.prefix) {
			return e.p
		}
	}
	return claudeModelPrice{5.0, 25.0, 0.5, 6.25, 10.0} // default: opus list price
}

// forgeChecksReader returns a scorecard.ChecksReader that resolves a story's PR check runs (with timestamps) through the
// github forge in the story worktree, so the scorecard can measure ci_wall_incl_queue_s. It returns ok=false when the
// forge is disabled (--no-forge), the story has no worktree, or gh cannot resolve a PR or its checks - the metric then
// stays unknown, never 0. The head is the recorded PR (evidence.pr) when present, else the story branch story/<id>.
func forgeChecksReader(epicDir string, disabled bool) scorecard.ChecksReader {
	if disabled {
		return nil
	}
	return func(story string) ([]forge.Check, bool) {
		dir := readWorktree(epicDir, story)
		if dir == "" {
			dir = epicDir
		}
		f := github.New(dir)
		pr, err := f.PR(forgeHeadFor(epicDir, story))
		if err != nil {
			return nil, false
		}
		checks, err := f.Checks(pr)
		if err != nil {
			return nil, false
		}
		return checks, true
	}
}

// forgeHeadFor returns the forge selector for a story: the recorded PR number (evidence.pr) when one exists, else the
// story branch story/<id>. It mirrors forgeHead in state.go, reading the story's folded last event.
func forgeHeadFor(epicDir, story string) string {
	events, _, err := state.Load(epicDir)
	if err != nil {
		return "story/" + story
	}
	if s := state.Fold(events).Stories[story]; s != nil {
		return forgeHead(s)
	}
	return "story/" + story
}

// claudeUsageReader returns a scorecard.UsageReader that reads a story worker's Claude session log (the newest project
// dir for its worktree) and yields one Usage per assistant API call. It returns ok=false when the story has no recorded
// worktree or no session log (a codex worker or an unread log stays unknown, never 0). COX_SCORECARD_HOME overrides
// $HOME for tests.
func claudeUsageReader(epicDir string) scorecard.UsageReader {
	home := os.Getenv("HOME")
	if h := os.Getenv("COX_SCORECARD_HOME"); h != "" {
		home = h
	}
	return func(story string) ([]scorecard.Usage, bool) {
		wt := readWorktree(epicDir, story)
		if wt == "" {
			return nil, false
		}
		dir := filepath.Join(home, ".claude", "projects", strings.ReplaceAll(wt, "/", "-"))
		files, _ := filepath.Glob(filepath.Join(dir, "*.jsonl"))
		if len(files) == 0 {
			return nil, false // no session log: tokens/cost are unknown, not 0
		}
		var calls []scorecard.Usage
		for _, fpath := range files {
			calls = append(calls, parseClaudeLog(fpath)...)
		}
		return calls, true
	}
}

// parseClaudeLog reads one Claude session jsonl and returns a Usage per assistant message that carries usage. A line
// that does not parse, or lacks a timestamp/usage, is skipped.
func parseClaudeLog(path string) []scorecard.Usage {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []scorecard.Usage
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		var o struct {
			Type      string `json:"type"`
			Timestamp string `json:"timestamp"`
			Message   struct {
				Model string `json:"model"`
				Usage struct {
					InputTokens              int `json:"input_tokens"`
					OutputTokens             int `json:"output_tokens"`
					CacheReadInputTokens     int `json:"cache_read_input_tokens"`
					CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
					CacheCreation            struct {
						Ephemeral5m int `json:"ephemeral_5m_input_tokens"`
						Ephemeral1h int `json:"ephemeral_1h_input_tokens"`
					} `json:"cache_creation"`
				} `json:"usage"`
			} `json:"message"`
		}
		if err := json.Unmarshal(sc.Bytes(), &o); err != nil || o.Type != "assistant" || o.Timestamp == "" {
			continue
		}
		u := o.Message.Usage
		if u.InputTokens == 0 && u.OutputTokens == 0 && u.CacheReadInputTokens == 0 && u.CacheCreationInputTokens == 0 {
			continue
		}
		at, err := time.Parse(time.RFC3339, o.Timestamp)
		if err != nil {
			continue
		}
		p := priceFor(o.Message.Model)
		// Cache-write split: prefer the ephemeral breakdown; else charge the flat cache_creation at the 1h rate (v1).
		c5, c1 := u.CacheCreation.Ephemeral5m, u.CacheCreation.Ephemeral1h
		if c5 == 0 && c1 == 0 {
			c1 = u.CacheCreationInputTokens
		}
		usd := (float64(u.InputTokens)*p.inp + float64(u.OutputTokens)*p.out + float64(u.CacheReadInputTokens)*p.cr +
			float64(c5)*p.c5 + float64(c1)*p.c1) / 1e6
		out = append(out, scorecard.Usage{
			At:  at.UTC(),
			In:  u.InputTokens + u.CacheReadInputTokens + u.CacheCreationInputTokens,
			Out: u.OutputTokens,
			USD: usd,
		})
	}
	return out
}
