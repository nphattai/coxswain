package workspace

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Item 8: the default template resolves delivery.mode = direct-PR (today's behaviour) and merge.yolo = false (the captain
// merges). Base-behavior probe: on the base sha there is no delivery.mode or merge section, so DeliveryMode/MergeYolo do
// not exist and the template carries neither key.
func TestDeliveryModeAndMergeYoloTemplate(t *testing.T) {
	root := t.TempDir()
	if _, err := Init(root, nil); err != nil {
		t.Fatal(err)
	}
	pol, err := LoadPolicy(root)
	if err != nil {
		t.Fatalf("default policy must validate: %v", err)
	}
	if pol.Delivery.Mode != ModeDirectPR {
		t.Errorf("template delivery.mode = %q, want %q", pol.Delivery.Mode, ModeDirectPR)
	}
	if pol.DeliveryMode() != ModeDirectPR {
		t.Errorf("DeliveryMode() = %q, want %q", pol.DeliveryMode(), ModeDirectPR)
	}
	if pol.MergePosture.Yolo {
		t.Error("template merge.yolo must default false (the captain merges)")
	}
	if pol.MergeYolo() {
		t.Error("MergeYolo() must be false for the default template")
	}
}

// DeliveryMode falls back to direct-PR for a nil policy and an unset mode; MergeYolo is false for a nil policy.
func TestDeliveryModeDefaults(t *testing.T) {
	var nilPol *Policy
	if nilPol.DeliveryMode() != DefaultDeliveryMode {
		t.Errorf("nil policy DeliveryMode = %q, want %q", nilPol.DeliveryMode(), DefaultDeliveryMode)
	}
	if nilPol.MergeYolo() {
		t.Error("nil policy MergeYolo must be false")
	}
	if got := (&Policy{}).DeliveryMode(); got != DefaultDeliveryMode {
		t.Errorf("empty policy DeliveryMode = %q, want %q", got, DefaultDeliveryMode)
	}
}

// A merge.yolo: true policy loads and MergeYolo reports it (a project may opt into an unattended merge).
func TestMergeYoloOptIn(t *testing.T) {
	root := t.TempDir()
	if _, err := Init(root, nil); err != nil {
		t.Fatal(err)
	}
	projDir := filepath.Join(root, "yolo")
	if err := os.MkdirAll(filepath.Join(projDir, "cox"), 0o755); err != nil {
		t.Fatal(err)
	}
	override := `{"merge":{"yolo":true,"why":"trusted unattended merge","review_when":"never"}}`
	if err := os.WriteFile(filepath.Join(projDir, "cox", "policy.json"), []byte(override), 0o644); err != nil {
		t.Fatal(err)
	}
	merged, err := Resolve(root, projDir)
	if err != nil {
		t.Fatalf("resolve with merge override: %v", err)
	}
	if !merged.MergeYolo() {
		t.Error("merge.yolo override did not resolve to true")
	}
}

// Base-behavior probe (item 8): a policy with an unrecognised delivery.mode is refused at load. On the base sha the mode
// field does not exist, so the loader ignores it and returns no error - this test then fails (expected an error, got nil).
func TestMalformedDeliveryModeRefused(t *testing.T) {
	root := t.TempDir()
	if _, err := Init(root, nil); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, ControlDir, "policy.json")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// Corrupt the delivery mode to an unknown value; everything else stays valid.
	bad := strings.Replace(string(b), `"mode": "direct-PR"`, `"mode": "bogus"`, 1)
	if bad == string(b) {
		t.Fatal("template did not contain the delivery.mode line to corrupt")
	}
	if err := os.WriteFile(path, []byte(bad), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = LoadPolicy(root)
	if err == nil {
		t.Fatal("a policy with an invalid delivery.mode must be refused")
	}
	if !strings.Contains(err.Error(), "mode") {
		t.Errorf("refusal must name the bad mode, got: %v", err)
	}
}

// The merge section is justified: a merge override that drops its why is caught by the whole-policy validation.
func TestMergeSectionIsJustified(t *testing.T) {
	root := t.TempDir()
	if _, err := Init(root, nil); err != nil {
		t.Fatal(err)
	}
	projDir := filepath.Join(root, "nowhy")
	if err := os.MkdirAll(filepath.Join(projDir, "cox"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projDir, "cox", "policy.json"), []byte(`{"merge":{"yolo":true}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Resolve(root, projDir); err == nil || !strings.Contains(err.Error(), "merge") {
		t.Fatalf("merge override without why must be refused naming merge, got: %v", err)
	}
}
