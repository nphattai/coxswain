package pi

import (
	"fmt"
	"strings"
)

// thinkingLevels are the Pi 0.85.1 thinking levels (verified via `pi --help`: --thinking <level>). An empty level is
// allowed (Pi uses its configured default); any non-empty level must be one of these.
var thinkingLevels = map[string]bool{
	"off": true, "minimal": true, "low": true, "medium": true, "high": true, "xhigh": true, "max": true,
}

// ValidateThinking rejects an unsupported Pi thinking level before spawn with a bounded diagnostic. An empty level is
// accepted: Pi falls back to its default and no --thinking flag is typed.
func ValidateThinking(level string) error {
	l := strings.TrimSpace(level)
	if l == "" {
		return nil
	}
	if !thinkingLevels[l] {
		return fmt.Errorf("pi thinking level %q is unsupported: expected one of off, minimal, low, medium, high, xhigh, max", level)
	}
	return nil
}

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
