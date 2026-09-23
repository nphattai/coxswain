package state

import "strings"

// SessionStory is the ONE filter over <epic>/.cox/sessions: it maps a file name to the story whose worker session it
// holds. Only <story>.json is a session; the harness busy record <story>.busy.json that lives beside it (and anything
// that is not .json) is not, so no reader ever stops or tracks a busy record as a worker.
func SessionStory(name string) (story string, ok bool) {
	if !strings.HasSuffix(name, ".json") || strings.HasSuffix(name, ".busy.json") {
		return "", false
	}
	return strings.TrimSuffix(name, ".json"), true
}
