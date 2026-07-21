package herdr

import "strings"

// StripInjectedEnv returns environ without Herdr's pane ownership metadata.
func StripInjectedEnv(environ []string) []string {
	filtered := make([]string, 0, len(environ))
	for _, entry := range environ {

		key := entry
		if before, _, ok := strings.Cut(entry, "="); ok {
			key = before
		}
		switch key {
		case envHerdr, envSocketPath, envPaneID:
			continue
		default:
			filtered = append(filtered, entry)
		}
	}
	return filtered
}
