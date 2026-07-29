package cli

type commandGroup struct {
	heading  string
	commands []string
}

var topLevelCommandGroups = []commandGroup{
	{
		heading:  "Plan Commands",
		commands: []string{"list", "show", "note", "validate", "staleness", "edit", "delete"},
	},
	{
		heading:  "Execution Commands",
		commands: []string{"run", "commit", "queue", "approve", "review", "rework"},
	},
	{
		heading:  "Workspace & Cleanup Commands",
		commands: []string{"workspace", "cleanup", "merge"},
	},
	{
		heading:  "Repository Commands",
		commands: []string{"init", "repo"},
	},
	{
		heading:  "Monitoring Commands",
		commands: []string{"monitor", "status", "insights", "log"},
	},
	{
		heading:  "Prompt & Agent Commands",
		commands: []string{"prompt", "draft-prompt", "install-prompts"},
	},
	{
		heading:  "Settings Commands",
		commands: []string{"completion", "doctor", "update"},
	},
	{
		heading:  "Other Commands",
		commands: []string{"version", "slice-complete", "slice-blocked", "capture-planning-session"},
	},
}
