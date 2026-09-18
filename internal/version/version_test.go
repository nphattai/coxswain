package version

import "testing"

// Version must never be empty: `cox <version>` and `cox doctor` rely on it being printable.
func TestVersionNotEmpty(t *testing.T) {
	if Version == "" {
		t.Fatal("Version must not be empty (default is \"dev\", release overrides via -ldflags)")
	}
}
