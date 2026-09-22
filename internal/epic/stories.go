package epic

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/template"

	"github.com/nphattai/coxswain/internal/workspace"
	"github.com/nphattai/coxswain/templates"
)

// StorySpec is the per-story input the leader supplies; everything policy-driven (delivery style, context thresholds,
// harness default) is resolved from policy, not repeated here.
type StorySpec struct {
	ID      string
	Repo    string
	Title   string
	Depends []string
	Device  bool
	Host    string
	Harness string // "" => policy worker default
	Model   string
	Mode    string // "" => policy delivery.mode (item 8)
	Kind    string // "" => "ship" (item 9)
}

// storyData is what the story template renders against.
type storyData struct {
	ID           string
	Repo         string
	Title        string
	Depends      string
	Device       bool
	Host         string
	Harness      string
	Model        string
	Slug         string
	EpicDir      string
	PolicySource string
	Delivery     workspace.Delivery
	Mode         string // resolved delivery mode (item 8): the story's override, else policy delivery.mode
	Kind         string // ship|scout (item 9): the story's override, else ship
	Context      workspace.Context
	// EpicToken/WorkspaceToken render as the LITERAL tokens {{.EpicDir}} / {{.WorkspaceDir}} into the story's Read first
	// block, so the created story stays path-free; cox checkpoint inject resolves them to this machine's paths at inject
	// time (finding 1). Everything else in the template still renders the absolute EpicDir.
	EpicToken      string
	WorkspaceToken string
}

// Stories renders each spec into <epic>/stories/<id>.md from the one template, with the project policy resolved once
// and stamped into policy_source (F14). It returns the rendered file paths. An existing story file is not overwritten.
func Stories(epicDir, wsRoot, project string, specs []StorySpec) ([]string, error) {
	projectDir := filepath.Join(wsRoot, project)
	pol, err := workspace.Resolve(wsRoot, projectDir)
	if err != nil {
		return nil, err
	}
	source, err := policySource(wsRoot, projectDir)
	if err != nil {
		return nil, err
	}
	tmplBytes, err := templates.File("story.md")
	if err != nil {
		return nil, err
	}
	tmpl, err := template.New("story").Parse(string(tmplBytes))
	if err != nil {
		return nil, fmt.Errorf("parse story template: %w", err)
	}
	slug := filepath.Base(epicDir)
	var written []string
	for _, s := range specs {
		if s.ID == "" || s.Repo == "" {
			return nil, fmt.Errorf("story spec needs id and repo, got %+v", s)
		}
		path := filepath.Join(epicDir, "stories", s.ID+".md")
		if _, err := os.Stat(path); err == nil {
			written = append(written, path) // do not clobber an existing story
			continue
		}
		harness := s.Harness
		if harness == "" {
			harness = pol.Harness.Worker.Default
		}
		mode := s.Mode
		if mode == "" {
			mode = pol.DeliveryMode()
		}
		kind := s.Kind
		if kind == "" {
			kind = "ship"
		}
		data := storyData{
			ID: s.ID, Repo: s.Repo, Title: s.Title, Depends: strings.Join(s.Depends, ", "),
			Device: s.Device, Host: s.Host, Harness: harness, Model: s.Model,
			Slug: slug, EpicDir: epicDir, PolicySource: source,
			Delivery: pol.Delivery, Mode: mode, Kind: kind, Context: pol.Context,
			EpicToken: "{{.EpicDir}}", WorkspaceToken: "{{.WorkspaceDir}}",
		}
		var buf bytes.Buffer
		if err := tmpl.Execute(&buf, data); err != nil {
			return nil, fmt.Errorf("render story %s: %w", s.ID, err)
		}
		if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
			return nil, err
		}
		written = append(written, path)
	}
	return written, nil
}

// policySource returns the provenance string for the resolved policy: the workspace policy file and its short sha, plus
// the project override file and its sha when one exists. This is what the story's policy_source frontmatter records.
func policySource(wsRoot, projectDir string) (string, error) {
	wsPol := filepath.Join(wsRoot, workspace.ControlDir, "policy.json")
	sha, err := fileSha(wsPol)
	if err != nil {
		return "", err
	}
	src := fmt.Sprintf("cox/policy.json@%s", sha)
	if projectDir != "" {
		projPol := filepath.Join(projectDir, workspace.ControlDir, "policy.json")
		if psha, err := fileSha(projPol); err == nil {
			src += fmt.Sprintf(" + %s/cox/policy.json@%s", filepath.Base(projectDir), psha)
		}
	}
	return src, nil
}

func fileSha(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return fmt.Sprintf("%x", sum)[:12], nil
}
