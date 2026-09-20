package pi

import (
	"fmt"
	"strings"
)

// ValidateModel enforces Pi's provider/model syntax before spawn. Pi supports many providers and Coxswain must never
// invent a provider from the harness name (DESIGN §2), so a bare, empty, or provider-less model is rejected with a
// bounded diagnostic rather than silently launched. The check is intentionally lenient about the model id itself
// (some providers namespace ids with further slashes); it only requires a non-empty provider and a non-empty id.
func ValidateModel(model string) error {
	m := strings.TrimSpace(model)
	if m == "" {
		return fmt.Errorf("pi model is empty: expected provider/model (e.g. anthropic/claude-opus-4-8)")
	}
	provider, id, ok := strings.Cut(m, "/")
	if !ok {
		return fmt.Errorf("pi model %q lacks a provider: expected provider/model (e.g. anthropic/claude-opus-4-8)", model)
	}
	if strings.TrimSpace(provider) == "" || strings.TrimSpace(id) == "" {
		return fmt.Errorf("pi model %q must be provider/model with a non-empty provider and model", model)
	}
	return nil
}
