package cli

import (
	"flag"
	"strings"
)

type commandSubcommandAlias string

type commandSubcommand struct {
	name          string
	aliases       []commandSubcommandAlias
	description   string
	registerFlags func(*flag.FlagSet)
	completion    completionContext
}

func (s commandSubcommand) completionNames() []string {
	names := make([]string, 0, len(s.aliases)+1)
	names = append(names, s.name)
	for _, alias := range s.aliases {
		names = append(names, string(alias))
	}
	return names
}

func (s commandSubcommand) helpName() string {
	if len(s.aliases) == 0 {
		return s.name
	}
	aliases := make([]string, 0, len(s.aliases))
	for _, alias := range s.aliases {
		aliases = append(aliases, string(alias))
	}
	return s.name + " (" + strings.Join(aliases, "|") + ")"
}

type completionValueKind int

const (
	completionValueText completionValueKind = iota
	completionValuePath
	completionValueCount
	completionValueBoolean
	completionValueEnum
)

type completionFlagValue struct {
	kind   completionValueKind
	label  string
	values []string
}

type positionalCompleter string

const (
	completePlanIDs         positionalCompleter = "plan-ids"
	completeRunnablePlanIDs positionalCompleter = "run-plan-ids"
	completeNoteIDs         positionalCompleter = "note-ids"
)

// completionPositional describes a one-based logical positional argument.
// Renderers translate index into the shell's absolute word position.
type completionPositional struct {
	index              int
	label              string
	candidates         []string
	completer          positionalCompleter
	repeat             bool
	disallowAfterFlags []string
}

// completionContext adds semantic hints to the flags and positionals declared
// by a command. Flag registration remains authoritative for whether a flag
// exists and for its completion description.
type completionContext struct {
	flagValues     map[string]completionFlagValue
	positional     completionPositional
	flagExemptions map[string]string
}

type commandMetadata struct {
	name                  string
	minPrefix             string
	usageLines            []string
	completionDescription string
	long                  string
	examples              string
	subcommands           []commandSubcommand
	registerFlags         func(*flag.FlagSet)
	completion            completionContext
	// repository declares which plan repository, if any, the command needs. The
	// dispatcher resolves it once and hands the result to execute via
	// commandContext, so handlers no longer rebuild it from plansDir.
	repository repositoryKind
	execute    commandExecutor
}

// repositoryKind selects how the dispatcher resolves a command's plan
// repository. The zero value, repositoryNone, suits commands that never open a
// plan repository.
type repositoryKind int

const (
	repositoryNone repositoryKind = iota
	repositoryDefault
)

// commandExecutor runs a registered command against the dependencies the
// dispatcher resolved for one invocation.
type commandExecutor func(commandContext) error

var versionCommand = commandMetadata{
	name:                  "version",
	usageLines:            []string{"version"},
	completionDescription: "Show build commit",
	long:                  "Show the Tao build commit embedded at compile time. Development builds that do not set a commit value report dev.",
	examples:              "  tao version",
	execute: func(c commandContext) error {
		return c.app.version()
	},
}

var commandRegistry = []commandMetadata{
	listCommand,
	versionCommand,
	updateCommand,
	initCommand,
	logCommand,
	runCommand,
	commitCommand,
	queueCommand,
	repoCommand,
	noteCommand,
	approveCommand,
	sliceCompleteCommand,
	sliceBlockedCommand,
	showCommand,
	reportCommand,
	reviewCommand,
	reworkCommand,
	stalenessCommand,
	validateCommand,
	statusCommand,
	monitorCommand,
	uiCommand,
	insightsCommand,
	cleanupCommand,
	mergeCommand,
	deleteCommand,
	editCommand,
	capturePlanningSessionCommand,
	promptCommand,
	draftPromptCommand,
	installPromptsCommand,
	doctorCommand,
	completionCommand,
	workspaceCommand,
}

func init() {
	if metadata := commandByName("completion"); metadata != nil {
		metadata.execute = func(c commandContext) error {
			return c.app.completion(c.args)
		}
	}
}

func normalizeCommand(command string) string {
	for _, metadata := range commandRegistry {
		if metadata.minPrefix == "" {
			continue
		}
		if len(command) >= len(metadata.minPrefix) && strings.HasPrefix(metadata.name, command) {
			return metadata.name
		}
	}
	return command
}

func commandByName(command string) *commandMetadata {
	for i := range commandRegistry {
		if commandRegistry[i].name == command {
			return &commandRegistry[i]
		}
	}
	return nil
}

func commandAliases(metadata commandMetadata) []string {
	if metadata.minPrefix == "" {
		return []string{metadata.name}
	}
	aliases := make([]string, 0, len(metadata.name)-len(metadata.minPrefix)+1)
	for length := len(metadata.minPrefix); length <= len(metadata.name); length++ {
		aliases = append(aliases, metadata.name[:length])
	}
	return aliases
}
