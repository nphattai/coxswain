package workspace

import (
	"encoding/json"
	"os"
	"sort"

	"github.com/nphattai/coxswain/templates"
)

// templatePolicyRaw parses the embedded template policy.json into a generic map, for key-drift comparison and default
// lookup against a workspace policy that may predate newer keys.
func templatePolicyRaw() (map[string]any, error) {
	b, err := templates.File("policy.json")
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	return m, nil
}

// TemplateWorkerModel returns the template's default worker model for a harness (harness.worker.models[h]), or "" when
// the template maps none. Dispatch uses it as the fallback when a workspace policy predates the harness.worker.models
// map, so a codex worker gets gpt-5.6-sol from the template instead of launching with no --model (M14).
func TemplateWorkerModel(h string) string {
	m, err := templatePolicyRaw()
	if err != nil {
		return ""
	}
	models, _ := dig(m, "harness", "worker", "models").(map[string]any)
	if s, ok := models[h].(string); ok {
		return s
	}
	return ""
}

// PolicyDrift returns the dotted key paths the template policy declares but the workspace policy at wsPolicyPath lacks
// (e.g. "harness.worker.models"), so `cox doctor` can flag a workspace policy that predates newer keys. It reports only
// keys present in the template and absent in the workspace (workspace-only keys are not drift, and value differences are
// not compared); the `why`/`review_when` justification prose is ignored. A read or parse failure returns the error.
func PolicyDrift(wsPolicyPath string) ([]string, error) {
	tmpl, err := templatePolicyRaw()
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(wsPolicyPath)
	if err != nil {
		return nil, err
	}
	var ws map[string]any
	if err := json.Unmarshal(b, &ws); err != nil {
		return nil, err
	}
	var missing []string
	diffKeys("", tmpl, ws, &missing)
	sort.Strings(missing)
	return missing, nil
}

// diffKeys walks the template map and records every key path absent from ws. It recurses only where both sides are
// objects; a key present in ws (any type) is satisfied. The `why`/`review_when` justification fields are prose, not
// behavioral keys, so they are never reported as drift.
func diffKeys(prefix string, tmpl, ws map[string]any, missing *[]string) {
	for k, tv := range tmpl {
		if k == "why" || k == "review_when" {
			continue
		}
		path := k
		if prefix != "" {
			path = prefix + "." + k
		}
		wv, ok := ws[k]
		if !ok {
			*missing = append(*missing, path)
			continue
		}
		if tm, tobj := tv.(map[string]any); tobj {
			if wm, wobj := wv.(map[string]any); wobj {
				diffKeys(path, tm, wm, missing)
			}
		}
	}
}

// dig walks a nested generic map by keys, returning the value at the path or nil when any step is not an object.
func dig(m map[string]any, keys ...string) any {
	var cur any = m
	for _, k := range keys {
		mm, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = mm[k]
	}
	return cur
}
