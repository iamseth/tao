package cli

import "testing"

func TestTopLevelCommandGroupsCoverCommandRegistry(t *testing.T) {
	registry := make(map[string]struct{}, len(commandRegistry))
	for _, metadata := range commandRegistry {
		registry[metadata.name] = struct{}{}
	}

	seen := make(map[string]string, len(commandRegistry))
	for _, group := range topLevelCommandGroups {
		if group.heading == "" {
			t.Fatal("command group heading must not be empty")
		}
		for _, commandName := range group.commands {
			if _, ok := registry[commandName]; !ok {
				t.Fatalf("command group %q references unknown command %q", group.heading, commandName)
			}
			if previousGroup, ok := seen[commandName]; ok {
				t.Fatalf("command %q appears in both %q and %q", commandName, previousGroup, group.heading)
			}
			seen[commandName] = group.heading
		}
	}

	for _, metadata := range commandRegistry {
		if _, ok := seen[metadata.name]; !ok {
			t.Fatalf("command %q is not assigned to a top-level command group", metadata.name)
		}
	}
}
