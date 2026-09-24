package supervision

import (
	"os"
	"path/filepath"
	"testing"
)

// Register binds a check to its bytes; drifted bytes stay counted as need but no longer verify; Unregister retires it.
func TestRegisterStatusUnregister(t *testing.T) {
	control := t.TempDir()
	check := filepath.Join(control, "issue-comments.check.sh")
	if err := os.WriteFile(check, []byte("#!/usr/bin/env bash\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if n := Status(control); n.Checks != 0 {
		t.Fatalf("an unregistered check must not count, got %+v", n)
	}
	if err := Register(control, "issue-comments"); err != nil {
		t.Fatal(err)
	}
	if n := Status(control); n.Checks != 1 || !Registered(control, "issue-comments") {
		t.Fatalf("a registered check must count and verify, got %+v", n)
	}
	if err := os.WriteFile(check, []byte("#!/usr/bin/env bash\necho drifted\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if n := Status(control); n.Checks != 1 || Registered(control, "issue-comments") {
		t.Fatalf("a drifted check stays a need but must not verify, got %+v", n)
	}
	if err := Register(control, "../escape"); err == nil {
		t.Fatal("a path-unsafe id must be refused")
	}
	if err := Unregister(control, "issue-comments"); err != nil || Status(control).Checks != 0 {
		t.Fatalf("unregister must retire the check, err=%v", err)
	}
}
