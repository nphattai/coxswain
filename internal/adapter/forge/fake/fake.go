// Package fake is a Forge that replays a fixture for tests. A fixture is a single JSON file describing the PR, its
// diff, checks, comments, merged flag, and an optional `errors` map that makes a named method fail - so the verdict
// layer's "retrieval error -> unknown" paths are exercised the way the real gh would fail.
package fake

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/nphattai/coxswain/internal/adapter/forge"
)

// Fixture is the on-disk shape (tests/fixtures/verdict/<name>/pr.json).
type Fixture struct {
	PR       forge.PR          `json:"pr"`
	Diff     string            `json:"diff"`
	Checks   []forge.Check     `json:"checks"`
	Comments []forge.Comment   `json:"comments"`
	Merged   bool              `json:"merged"`
	Errors   map[string]string `json:"errors"` // method name ("pr","diff","checks","comments","merged") -> error text
}

// Forge replays a Fixture.
type Forge struct{ F Fixture }

// Load reads a fixture JSON file.
func Load(path string) (*Forge, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var f Fixture
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, fmt.Errorf("parse fixture %s: %w", path, err)
	}
	return &Forge{F: f}, nil
}

func (f *Forge) err(method string) error {
	if msg, ok := f.F.Errors[method]; ok {
		return fmt.Errorf("%s", msg)
	}
	return nil
}

func (f *Forge) PR(headRef string) (forge.PR, error) {
	if err := f.err("pr"); err != nil {
		return forge.PR{}, err
	}
	return f.F.PR, nil
}
func (f *Forge) Diff(pr forge.PR) (string, error) {
	if err := f.err("diff"); err != nil {
		return "", err
	}
	return f.F.Diff, nil
}
func (f *Forge) Checks(pr forge.PR) ([]forge.Check, error) {
	if err := f.err("checks"); err != nil {
		return nil, err
	}
	return f.F.Checks, nil
}
func (f *Forge) Comments(pr forge.PR) ([]forge.Comment, error) {
	if err := f.err("comments"); err != nil {
		return nil, err
	}
	return f.F.Comments, nil
}
func (f *Forge) Merged(pr forge.PR) (bool, error) {
	if err := f.err("merged"); err != nil {
		return false, err
	}
	return f.F.Merged, nil
}
