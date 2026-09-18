// Package artifact is the coxswain.artifact.v1 contract (M13, ADR 0008/0013): a review page under
// <epic>/reports/visual/<name>.html carries a sidecar <name>.artifact.json naming its kind, its sources with content
// shas, when it was generated, and whether cox or the leader generated it. cox owns the artifact and its record; lavish
// is only the review surface (DESIGN "Decision after arena round 1"). The sidecar's synthesis_sha (kind=arena) lets the
// answer path refuse a stale artifact before it writes a captain cell.
package artifact

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Schema is the sidecar schema id.
const Schema = "coxswain.artifact.v1"

// Kinds an artifact can carry.
const (
	KindDesign     = "design"
	KindPlan       = "plan"
	KindArena      = "arena"
	KindBoard      = "board"
	KindComparison = "comparison"
)

// Generators: cox for a template generator, leader for a hand-authored page (per a lavish playbook).
const (
	GenCox    = "cox"
	GenLeader = "leader"
)

// Source is one input a page was generated from, with the sha256 of its content at generation time.
type Source struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

// Sidecar is the parsed <name>.artifact.json. HTML is the sibling page basename, filled by List (never serialized).
type Sidecar struct {
	Schema       string   `json:"schema"`
	Kind         string   `json:"kind"`
	Sources      []Source `json:"sources"`
	GeneratedAt  string   `json:"generated_at"`
	Generator    string   `json:"generator"`
	SynthesisSHA string   `json:"synthesis_sha,omitempty"` // kind=arena only: synth.ContentSha of the synthesis it renders
	HTML         string   `json:"-"`
}

// VisualDir is <epic>/reports/visual, where every generator writes its page and sidecar.
func VisualDir(epicDir string) string { return filepath.Join(epicDir, "reports", "visual") }

// SidecarPath maps a page path to its sidecar: <name>.html -> <name>.artifact.json (any other extension is replaced too).
func SidecarPath(htmlPath string) string {
	return strings.TrimSuffix(htmlPath, filepath.Ext(htmlPath)) + ".artifact.json"
}

// HashFile returns the full sha256 hex of a file's content.
func HashFile(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return HashBytes(b), nil
}

// HashBytes returns the full sha256 hex of content.
func HashBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return fmt.Sprintf("%x", sum)
}

// NewSource builds a Source for absPath, recording the path relative to epicDir when it lives under it (else as given),
// and the sha256 of its current content.
func NewSource(epicDir, absPath string) (Source, error) {
	sum, err := HashFile(absPath)
	if err != nil {
		return Source{}, err
	}
	return Source{Path: relTo(epicDir, absPath), SHA256: sum}, nil
}

// relTo returns p relative to base when p is under base, else p unchanged.
func relTo(base, p string) string {
	rel, err := filepath.Rel(base, p)
	if err != nil || strings.HasPrefix(rel, "..") {
		return p
	}
	return filepath.ToSlash(rel)
}

// Write records the sidecar next to htmlPath. Schema defaults; the caller fills kind, sources, generated_at, generator,
// and synthesis_sha (arena). The page's own file is written by the generator; Write only publishes the sidecar.
func Write(htmlPath string, s Sidecar) error {
	if s.Schema == "" {
		s.Schema = Schema
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal sidecar: %w", err)
	}
	return os.WriteFile(SidecarPath(htmlPath), append(b, '\n'), 0o644)
}

// List returns every artifact sidecar under <epic>/reports/visual, sorted by page basename. A missing dir yields none.
func List(epicDir string) ([]Sidecar, error) {
	dir := VisualDir(epicDir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read visual dir: %w", err)
	}
	var out []Sidecar
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".artifact.json") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, err
		}
		var s Sidecar
		if err := json.Unmarshal(b, &s); err != nil {
			return nil, fmt.Errorf("parse %s: %w", e.Name(), err)
		}
		s.HTML = strings.TrimSuffix(e.Name(), ".artifact.json") + ".html"
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].HTML < out[j].HTML })
	return out, nil
}
