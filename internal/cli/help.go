package cli

import (
	"flag"
	"io"
	"strings"

	"github.com/iamseth/tao/internal/term/cells"
)

const (
	topLevelHelpTagline = "Tao brings discipline to agentic coding sessions."
	topLevelHelpURL     = "https://github.com/iamseth/tao"
)

func (a App) usage() error {
	lines := []string{
		topLevelHelpTagline,
		"",
		"Find more information at: " + topLevelHelpURL,
		"",
	}
	nameWidth := topLevelCommandNameWidth()
	for groupIndex, group := range topLevelCommandGroups {
		if groupIndex > 0 {
			lines = append(lines, "")
		}
		lines = append(lines, group.heading+":")
		for _, commandName := range group.commands {
			metadata := commandByName(commandName)
			if metadata == nil {
				continue
			}
			lines = append(lines, "  "+cells.Pad(commandName, nameWidth+2)+metadata.completionDescription)
		}
	}
	lines = append(lines,
		"",
		"Usage:",
		"  tao [--plans-dir DIR] <command> [options]",
		"",
		`Use "tao <command> --help" for more information about a given command.`,
	)
	return writeLines(a.Out, lines...)
}

func topLevelCommandNameWidth() int {
	width := 0
	for _, group := range topLevelCommandGroups {
		for _, commandName := range group.commands {
			if len(commandName) > width {
				width = len(commandName)
			}
		}
	}
	return width
}

func renderCommandHelp(out io.Writer, metadata *commandMetadata) error {
	if metadata == nil {
		return nil
	}
	lines := make([]string, 0, 16)
	description := strings.TrimSpace(metadata.long)
	if description == "" {
		description = strings.TrimSpace(metadata.completionDescription)
	}
	lines = appendHelpBlock(lines, description)
	if strings.TrimSpace(metadata.examples) != "" {
		lines = appendHelpSection(lines, "Examples:")
		lines = appendPreformatted(lines, metadata.examples)
	}
	if len(metadata.subcommands) > 0 {
		lines = appendHelpSection(lines, "Available Commands:")
		lines = append(lines, commandHelpSubcommandLines(metadata.subcommands)...)
	}
	if optionLines := commandHelpOptionLines(metadata); len(optionLines) > 0 {
		lines = appendHelpSection(lines, "Options:")
		lines = append(lines, optionLines...)
	}
	if len(metadata.usageLines) > 0 {
		lines = appendHelpSection(lines, "Usage:")
		for _, usageLine := range metadata.usageLines {
			lines = append(lines, "  tao "+usageLine)
		}
	}
	lines = append(lines,
		"",
		`Use "tao <command> --help" for more information about a given command.`,
	)
	return writeLines(out, lines...)
}

func appendHelpSection(lines []string, heading string) []string {
	if len(lines) > 0 {
		lines = append(lines, "")
	}
	return append(lines, heading)
}

func appendHelpBlock(lines []string, text string) []string {
	text = strings.Trim(text, "\n")
	if text == "" {
		return lines
	}
	return append(lines, strings.Split(text, "\n")...)
}

func appendPreformatted(lines []string, text string) []string {
	return appendHelpBlock(lines, text)
}

func commandHelpSubcommandLines(subcommands []commandSubcommand) []string {
	nameWidth := 0
	for _, subcommand := range subcommands {
		if len(subcommand.helpName()) > nameWidth {
			nameWidth = len(subcommand.helpName())
		}
	}
	lines := make([]string, 0, len(subcommands))
	for _, subcommand := range subcommands {
		lines = append(lines, "  "+cells.Pad(subcommand.helpName(), nameWidth+2)+subcommand.description)
	}
	return lines
}

func commandHelpOptionLines(metadata *commandMetadata) []string {
	if metadata.registerFlags == nil {
		return nil
	}
	fs := flag.NewFlagSet(metadata.name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	metadata.registerFlags(fs)
	flags := make([]*flag.Flag, 0)
	fs.VisitAll(func(fl *flag.Flag) {
		flags = append(flags, fl)
	})
	if len(flags) == 0 {
		return nil
	}
	nameWidth := 0
	for _, fl := range flags {
		if len(fl.Name)+2 > nameWidth {
			nameWidth = len(fl.Name) + 2
		}
	}
	lines := make([]string, 0, len(flags))
	for _, fl := range flags {
		lines = append(lines, "  "+cells.Pad("--"+fl.Name, nameWidth+2)+commandHelpFlagUsage(fl))
	}
	return lines
}

func commandHelpFlagUsage(fl *flag.Flag) string {
	usage := fl.Usage
	if fl.DefValue == "" {
		return usage
	}
	if usage != "" {
		usage += " "
	}
	return usage + "(default " + fl.DefValue + ")"
}
