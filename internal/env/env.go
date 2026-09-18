package env

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// Infra endpoints default to v1 bin/lib.sh. They are fields on Allocator so a test can point them anywhere.
const (
	defaultPGPort       = 5433
	defaultPGUser       = "dev"
	defaultPGPass       = "dev"
	defaultRedisPort    = 6380
	defaultTemporalPort = 7234
)

// StoryResource is one story's owned resources in .cox/resources.json. ConfirmedReleased flips to true only after every
// owned external (db, sim) has been confirmed deleted; until then ownership is retained (F03/F04).
type StoryResource struct {
	Port              int    `json:"port,omitempty"`
	DB                string `json:"db,omitempty"`
	Sim               string `json:"sim,omitempty"`
	Env               string `json:"env,omitempty"`
	ConfirmedReleased bool   `json:"confirmed_released"`
}

// Backend is the epic backend process ownership: the pid cox started and the port it holds (F13). stop kills only this
// pid; a port held by a foreign pid is a conflict, not a kill.
type Backend struct {
	PID  int `json:"pid,omitempty"`
	Port int `json:"port,omitempty"`
}

// Resources is .cox/resources.json: per-story ownership plus the epic backend process.
type Resources struct {
	Stories map[string]*StoryResource `json:"stories"`
	Backend *Backend                  `json:"backend,omitempty"`
}

func resourcesPath(epicDir string) string {
	return filepath.Join(epicDir, ".cox", "resources.json")
}

// loadResources reads .cox/resources.json, returning an empty Resources when the file is absent.
func loadResources(epicDir string) (*Resources, error) {
	b, err := os.ReadFile(resourcesPath(epicDir))
	if err != nil {
		if os.IsNotExist(err) {
			return &Resources{Stories: map[string]*StoryResource{}}, nil
		}
		return nil, fmt.Errorf("read resources: %w", err)
	}
	var r Resources
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, fmt.Errorf("parse resources.json: %w", err)
	}
	if r.Stories == nil {
		r.Stories = map[string]*StoryResource{}
	}
	return &r, nil
}

// saveResources writes .cox/resources.json atomically (tmp + rename) so a crash never leaves the ownership ledger torn.
func saveResources(epicDir string, r *Resources) error {
	if err := os.MkdirAll(filepath.Join(epicDir, ".cox"), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(resourcesPath(epicDir), append(b, '\n'), 0o644)
}

// epicEnv is the leader-set epic.env (a subset of v1's keys env allocation needs).
type epicEnv struct {
	Slug          string
	Project       string
	Backend       string // alias whose stories run their own backend; "" = none
	APIPort       int
	StoryPortBase int
	DBName        string
	SimBase       string
	StoryEnvExtra string
}

// loadEpicEnv parses <epic>/epic.env (KEY=value lines, # comments).
func loadEpicEnv(epicDir string) (*epicEnv, error) {
	b, err := os.ReadFile(filepath.Join(epicDir, "epic.env"))
	if err != nil {
		return nil, fmt.Errorf("read epic.env: %w", err)
	}
	kv := parseEnvKV(string(b))
	return &epicEnv{
		Slug:          kv["EPIC"],
		Project:       kv["PROJECT"],
		Backend:       kv["BACKEND"],
		APIPort:       atoiSafe(kv["API_PORT"]),
		StoryPortBase: atoiSafe(kv["STORY_PORT_BASE"]),
		DBName:        kv["DB_NAME"],
		SimBase:       kv["SIM_BASE"],
		StoryEnvExtra: kv["STORY_ENV_EXTRA"],
	}, nil
}

// Allocator generates and publishes story env files and owns story resources.
type Allocator struct {
	EpicDir string
	Ops     Ops
	// Infra endpoints (0/"" => v1 defaults).
	PGPort       int
	PGUser       string
	PGPass       string
	RedisPort    int
	TemporalPort int
	// TwinDir, when set, receives a copy of the published env file AFTER publish (F13: mirror only after publish).
	TwinDir string
}

func (a *Allocator) pgPort() int {
	if a.PGPort > 0 {
		return a.PGPort
	}
	return defaultPGPort
}
func (a *Allocator) pgUser() string {
	if a.PGUser != "" {
		return a.PGUser
	}
	return defaultPGUser
}
func (a *Allocator) pgPass() string {
	if a.PGPass != "" {
		return a.PGPass
	}
	return defaultPGPass
}
func (a *Allocator) redisPort() int {
	if a.RedisPort > 0 {
		return a.RedisPort
	}
	return defaultRedisPort
}
func (a *Allocator) temporalPort() int {
	if a.TemporalPort > 0 {
		return a.TemporalPort
	}
	return defaultTemporalPort
}

// storyMeta is the subset of a story frontmatter allocation needs.
type storyMeta struct {
	Repo   string
	Device bool
}

func (a *Allocator) readStoryMeta(story string) storyMeta {
	var m storyMeta
	b, err := os.ReadFile(filepath.Join(a.EpicDir, "stories", story+".md"))
	if err != nil {
		return m
	}
	for k, v := range frontmatter(string(b)) {
		switch k {
		case "repo":
			m.Repo = v
		case "device":
			m.Device = v == "true"
		}
	}
	return m
}

// Allocate generates story's complete env file, validates it, and publishes it atomically, creating the story database
// (backend stories) and simulator (device stories) first so their identities are in the file before it is published.
// It is idempotent: a re-run reuses the port, database and simulator already recorded. External failures (a missing
// snapshot template, a clone failure) stop before any env file is written, so a partial env is never published (F13).
func (a *Allocator) Allocate(story, worktree string) (*StoryResource, error) {
	ee, err := loadEpicEnv(a.EpicDir)
	if err != nil {
		return nil, err
	}
	meta := a.readStoryMeta(story)
	res, err := loadResources(a.EpicDir)
	if err != nil {
		return nil, err
	}
	sr := res.Stories[story]
	if sr == nil {
		sr = &StoryResource{}
		res.Stories[story] = sr
	}

	// Port: assign once, reuse thereafter. Deterministic from the count of already-ported stories.
	if sr.Port == 0 {
		sr.Port = ee.StoryPortBase + 10*countPorted(res, story)
		if err := saveResources(a.EpicDir, res); err != nil {
			return nil, err
		}
	}

	short := strings.ReplaceAll(story, "-", "_")
	backendStory := ee.Backend != "" && meta.Repo == ee.Backend

	// Database first (external, real error). Only a backend story gets its own database.
	if backendStory && sr.DB == "" && ee.DBName != "" {
		db := ee.DBName + "_" + short
		tpl := ee.DBName + "_tpl"
		exists, err := a.Ops.DBExists(db)
		if err != nil {
			return nil, fmt.Errorf("db exists check for %s: %w", db, err)
		}
		if !exists {
			if err := a.Ops.DBCreate(db, tpl); err != nil {
				return nil, fmt.Errorf("clone database %s from %s: %w", db, tpl, err)
			}
		}
		sr.DB = db
		if err := saveResources(a.EpicDir, res); err != nil {
			return nil, err
		}
	}

	// Simulator (external, real error).
	if meta.Device && sr.Sim == "" && ee.SimBase != "" {
		udid, err := a.Ops.SimClone(ee.SimBase, ee.Slug+"-"+short)
		if err != nil {
			return nil, err
		}
		sr.Sim = udid
		if err := saveResources(a.EpicDir, res); err != nil {
			return nil, err
		}
	}

	// Build the complete env content in a buffer, then validate before writing anything.
	content, required := a.render(story, sr, ee, meta, backendStory)
	if missing := validate(content, required); len(missing) > 0 {
		return nil, fmt.Errorf("env for %s incomplete, refusing to publish: missing values for %s", story, strings.Join(missing, ", "))
	}

	envPath := filepath.Join(a.EpicDir, ".env."+story)
	if err := writeAtomic(envPath, []byte(content), 0o644); err != nil {
		return nil, fmt.Errorf("publish env: %w", err)
	}
	sr.Env = envPath
	if err := saveResources(a.EpicDir, res); err != nil {
		return nil, err
	}

	// Mirror to the twin path only after the authoritative file is published (F13).
	if a.TwinDir != "" {
		if err := os.MkdirAll(a.TwinDir, 0o755); err != nil {
			return nil, fmt.Errorf("twin dir: %w", err)
		}
		if err := writeAtomic(filepath.Join(a.TwinDir, ".env."+story), []byte(content), 0o644); err != nil {
			return nil, fmt.Errorf("mirror env to twin: %w", err)
		}
	}
	return sr, nil
}

// render builds the env file content and the list of keys that must carry a value for this story.
func (a *Allocator) render(story string, sr *StoryResource, ee *epicEnv, meta storyMeta, backendStory bool) (string, []string) {
	var b strings.Builder
	w := func(k, v string) { fmt.Fprintf(&b, "%s=%s\n", k, v) }
	w("STORY", story)
	w("PORT", strconv.Itoa(sr.Port))
	w("API_URL", fmt.Sprintf("http://localhost:%d", ee.APIPort))
	w("SEED_PREFIX", story+"-")
	required := []string{"STORY", "PORT", "API_URL", "SEED_PREFIX"}

	if extra := strings.TrimSpace(ee.StoryEnvExtra); extra != "" {
		for _, pair := range strings.Fields(extra) {
			if k, v, ok := strings.Cut(pair, "="); ok {
				w(k, v)
			}
		}
	}

	if backendStory && sr.DB != "" {
		w("DB_HOST", "localhost")
		w("DB_PORT", strconv.Itoa(a.pgPort()))
		w("DB_NAME", sr.DB)
		w("DB_USERNAME", a.pgUser())
		w("DB_PASSWORD", a.pgPass())
		w("REDIS_URL", fmt.Sprintf("redis://localhost:%d", a.redisPort()))
		w("REDIS_PREFIX", fmt.Sprintf("%s:%s:", ee.DBName, strings.ReplaceAll(story, "-", "_")))
		w("TYPEORM_HOST", "localhost")
		w("TYPEORM_PORT", strconv.Itoa(a.pgPort()))
		w("TYPEORM_SECONDARY_HOST", "localhost")
		w("TYPEORM_SECONDARY_PORT", strconv.Itoa(a.pgPort()))
		w("TYPEORM_DATABASE", sr.DB)
		w("TYPEORM_USERNAME", a.pgUser())
		w("TYPEORM_PASSWORD", a.pgPass())
		w("TEMPORAL_HOST", fmt.Sprintf("localhost:%d", a.temporalPort()))
		required = append(required, "DB_HOST", "DB_PORT", "DB_NAME", "DB_USERNAME", "DB_PASSWORD",
			"REDIS_URL", "REDIS_PREFIX", "TYPEORM_HOST", "TYPEORM_PORT", "TYPEORM_DATABASE",
			"TYPEORM_USERNAME", "TYPEORM_PASSWORD", "TEMPORAL_HOST")
	}
	if meta.Device && sr.Sim != "" {
		w("SIM_UDID", sr.Sim)
		required = append(required, "SIM_UDID")
	}
	return b.String(), required
}

// Release deletes a story's external resources and clears ownership, but only per-resource on confirmation: a database
// or simulator whose delete fails keeps its ownership and Release returns the error (F03/F04). confirmed_released flips
// to true only when every owned external is gone. The env file is removed last.
func (a *Allocator) Release(story string) error {
	res, err := loadResources(a.EpicDir)
	if err != nil {
		return err
	}
	sr := res.Stories[story]
	if sr == nil {
		return nil // nothing owned
	}
	var errs []string
	if sr.DB != "" {
		if err := a.Ops.DBDrop(sr.DB); err != nil {
			errs = append(errs, fmt.Sprintf("drop db %s: %v", sr.DB, err))
		} else {
			sr.DB = ""
		}
	}
	if sr.Sim != "" {
		if err := a.Ops.SimDelete(sr.Sim); err != nil {
			errs = append(errs, fmt.Sprintf("delete sim %s: %v", sr.Sim, err))
		} else {
			sr.Sim = ""
		}
	}
	if sr.Env != "" {
		if err := os.Remove(sr.Env); err != nil && !os.IsNotExist(err) {
			errs = append(errs, fmt.Sprintf("rm env %s: %v", sr.Env, err))
		} else {
			sr.Env = ""
		}
	}
	sr.ConfirmedReleased = sr.DB == "" && sr.Sim == "" && sr.Env == ""
	if saveErr := saveResources(a.EpicDir, res); saveErr != nil {
		errs = append(errs, saveErr.Error())
	}
	if len(errs) > 0 {
		return fmt.Errorf("release %s incomplete (ownership kept): %s", story, strings.Join(errs, "; "))
	}
	return nil
}

// BackendEnv returns the KEY=value environment for the epic backend service adapter and the API port it holds, built
// from epic.env the way v1 bin/local-env.sh backend_env did. The port lets the caller check ownership before start.
func (a *Allocator) BackendEnv() ([]string, int, error) {
	ee, err := loadEpicEnv(a.EpicDir)
	if err != nil {
		return nil, 0, err
	}
	pairs := []string{
		"PORT=" + itoa(ee.APIPort),
		"DB_HOST=localhost",
		"DB_PORT=" + itoa(a.pgPort()),
		"DB_NAME=" + ee.DBName,
		"DB_USERNAME=" + a.pgUser(),
		"DB_PASSWORD=" + a.pgPass(),
		fmt.Sprintf("REDIS_URL=redis://localhost:%d", a.redisPort()),
		"REDIS_PREFIX=" + ee.DBName + ":",
		"TYPEORM_HOST=localhost",
		"TYPEORM_PORT=" + itoa(a.pgPort()),
		"TYPEORM_DATABASE=" + ee.DBName,
		"TYPEORM_USERNAME=" + a.pgUser(),
		"TYPEORM_PASSWORD=" + a.pgPass(),
		fmt.Sprintf("TEMPORAL_HOST=localhost:%d", a.temporalPort()),
	}
	return pairs, ee.APIPort, nil
}

// EpicDBName returns the epic's own DB_NAME and its snapshot template name (DB_NAME + "_tpl").
func (a *Allocator) EpicDBName() (name, tpl string, err error) {
	ee, err := loadEpicEnv(a.EpicDir)
	if err != nil {
		return "", "", err
	}
	if ee.DBName == "" {
		return "", "", fmt.Errorf("epic.env has no DB_NAME")
	}
	return ee.DBName, ee.DBName + "_tpl", nil
}

// countPorted counts stories (other than the given one) that already hold a port, to place the next port block.
func countPorted(res *Resources, story string) int {
	n := 0
	for id, sr := range res.Stories {
		if id != story && sr.Port > 0 {
			n++
		}
	}
	return n
}

// validate returns the required keys whose value in content is empty.
func validate(content string, required []string) []string {
	have := parseEnvKV(content)
	var missing []string
	for _, k := range required {
		if strings.TrimSpace(have[k]) == "" {
			missing = append(missing, k)
		}
	}
	sort.Strings(missing)
	return missing
}

func parseEnvKV(s string) map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if k, v, ok := strings.Cut(line, "="); ok {
			out[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
	}
	return out
}

// frontmatter parses a leading --- fenced key: value block into a map.
func frontmatter(s string) map[string]string {
	out := map[string]string{}
	in := false
	for _, line := range strings.Split(s, "\n") {
		t := strings.TrimSpace(line)
		if t == "---" {
			if in {
				break
			}
			in = true
			continue
		}
		if !in {
			continue
		}
		if k, v, ok := strings.Cut(t, ":"); ok {
			v = strings.TrimSpace(strings.SplitN(v, "#", 2)[0])
			out[strings.TrimSpace(k)] = v
		}
	}
	return out
}

func atoiSafe(s string) int {
	n, _ := strconv.Atoi(strings.TrimSpace(s))
	return n
}

func itoa(n int) string { return strconv.Itoa(n) }
