package service

import (
	"os"
	"path/filepath"
	"testing"
)

func writeScript(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "svc.sh")
	if err := os.WriteFile(p, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestHealthTriState(t *testing.T) {
	// exit 0 -> pass
	pass := New(writeScript(t, "#!/bin/sh\nexit 0\n"), nil)
	if pass.Health() != Pass {
		t.Errorf("exit 0 should be Pass, got %v", pass.Health())
	}
	// exit 1 -> fail
	fail := New(writeScript(t, "#!/bin/sh\nexit 1\n"), nil)
	if fail.Health() != Fail {
		t.Errorf("exit 1 should be Fail, got %v", fail.Health())
	}
	// missing script -> unknown, never fail
	missing := New(filepath.Join(t.TempDir(), "nope.sh"), nil)
	if missing.Health() != Unknown {
		t.Errorf("missing script should be Unknown, got %v", missing.Health())
	}
	// non-executable script -> unknown
	dir := t.TempDir()
	ne := filepath.Join(dir, "svc.sh")
	if err := os.WriteFile(ne, []byte("#!/bin/sh\nexit 0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if New(ne, nil).Health() != Unknown {
		t.Errorf("non-executable script should be Unknown")
	}
}

func TestActionSurfacesNonZeroExit(t *testing.T) {
	a := New(writeScript(t, "#!/bin/sh\necho boom >&2\nexit 3\n"), nil)
	if err := a.Start(); err == nil {
		t.Fatal("non-zero start must error")
	}
	ok := New(writeScript(t, "#!/bin/sh\nexit 0\n"), nil)
	if err := ok.Preflight(); err != nil {
		t.Fatalf("exit 0 preflight should succeed, got %v", err)
	}
}

func TestEnvIsPassed(t *testing.T) {
	// The script fails unless COX_TEST is set, proving Env reaches it.
	a := New(writeScript(t, "#!/bin/sh\n[ \"$COX_TEST\" = yes ] && exit 0\nexit 1\n"), []string{"COX_TEST=yes"})
	if a.Health() != Pass {
		t.Fatalf("env var not passed to script")
	}
}
