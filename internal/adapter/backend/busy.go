package backend

import "github.com/nphattai/coxswain/internal/protocol/busy"

// BusyComposer maps the harness-owned busy record to a composer verdict, so every backend consults the harness FIRST and
// in the same way before any UI-derived guess (DESIGN wave-3 item 3, captain ruling: idle/busy is a fact the harness
// reports, not something a backend infers from a TUI). idle -> ComposerEmpty (ready to ring / idle), busy -> ComposerBusy
// (skip), and unknown or absent -> ("", false) so the caller falls back to its own classifier. epic or story empty (a
// session with no story, or a harness that reports no state) is unknown, so existing callers are unaffected.
func BusyComposer(epic, story string) (string, bool) {
	if epic == "" || story == "" {
		return "", false
	}
	switch busy.Read(epic, story) {
	case busy.Idle:
		return ComposerEmpty, true
	case busy.Busy:
		return ComposerBusy, true
	default:
		return "", false
	}
}
