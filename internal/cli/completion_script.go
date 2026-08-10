package cli

import (
	"flag"
	"io"
	"slices"
	"strconv"
	"strings"
)

func buildZshCompletionScript() string {
	var b strings.Builder
	b.WriteString(`#compdef tao

_tao_at_positional() {
  local start=$1 target=$2 repeat=$3 disallowed_flags=$4
  shift 4
  local -a value_flags
  value_flags=("$@")
  local logical_position=0 skip_value=0 end_flags=0 index word

  [[ ${words[CURRENT]} == -*=* ]] && return 1
  for (( index = start; index < CURRENT; index++ )); do
    word=${words[index]}
    if (( skip_value )); then
      skip_value=0
      continue
    fi
    if (( end_flags )); then
      (( logical_position++ ))
      continue
    fi
    if [[ $word == -- ]]; then
      end_flags=1
      continue
    fi
    if [[ $word == -* ]]; then
      [[ $disallowed_flags == *,$word,* ]] && return 1
      if [[ $word != *=* ]] && (( ${value_flags[(Ie)$word]} )); then
        skip_value=1
      fi
      continue
    fi
    (( logical_position++ ))
  done

  (( skip_value )) && return 1
  if (( repeat )); then
    (( logical_position >= target - 1 ))
  else
    (( logical_position == target - 1 ))
  fi
}

_tao() {
  local -a commands plan_ids subcommands

  commands=(
`)
	b.WriteString(zshCommandEntries())
	b.WriteString(`
  )

  if (( CURRENT == 2 )); then
    _describe -t commands 'tao command' commands
    return
  fi

  case ${words[2]} in
`)
	for i := range commandRegistry {
		if commandRegistry[i].completionDescription == "" {
			continue
		}
		b.WriteString(zshCommandCase(&commandRegistry[i]))
	}
	b.WriteString(`  esac
}

compdef _tao tao ./bin/tao bin/tao
`)
	return b.String()
}

func zshCommandCase(metadata *commandMetadata) string {
	var b strings.Builder
	b.WriteString("    ")
	b.WriteString(commandAliasPattern(metadata.name))
	b.WriteString(")\n")
	b.WriteString(zshParentCompletionContext(metadata, "      "))
	if len(metadata.subcommands) > 0 {
		b.WriteString("      if (( CURRENT == 3 )); then\n")
		b.WriteString(zshSubcommandEntriesAt(metadata, "        "))
		b.WriteString("\n        _describe -t commands ")
		b.WriteString(zshSingleQuote(metadata.name + " command"))
		b.WriteString(" subcommands\n        return\n      fi\n")
		b.WriteString("      case ${words[3]} in\n")
		for i := range metadata.subcommands {
			subcommand := &metadata.subcommands[i]
			b.WriteString("        ")
			b.WriteString(strings.Join(subcommand.completionNames(), "|"))
			b.WriteString(")\n")
			b.WriteString(zshCompletionContext(metadata, subcommand, "          "))
			b.WriteString("          ;;\n")
		}
		b.WriteString("      esac\n")
	}
	b.WriteString("      ;;\n")
	return b.String()
}

func zshParentCompletionContext(metadata *commandMetadata, indent string) string {
	if hasSubcommandFlagRegistrations(metadata) {
		positional := zshPositionalCompletion(metadata.name)
		if positional == "" {
			return ""
		}
		return indent + strings.ReplaceAll(strings.TrimSpace(positional), "\n", "\n"+indent) + "\n"
	}
	return zshCompletionContext(metadata, nil, indent)
}

func hasSubcommandFlagRegistrations(metadata *commandMetadata) bool {
	for _, subcommand := range metadata.subcommands {
		if subcommand.registerFlags != nil {
			return true
		}
	}
	return false
}

func zshCompletionContext(metadata *commandMetadata, subcommand *commandSubcommand, indent string) string {
	var b strings.Builder
	var arguments, positional string
	if subcommand == nil {
		arguments = zshCommandArguments(metadata.name)
		positional = zshPositionalCompletion(metadata.name)
	} else {
		arguments = zshCommandArguments(metadata.name, subcommand.name)
		positional = zshPositionalCompletion(metadata.name, subcommand.name)
	}
	if arguments != "" {
		b.WriteString(indent)
		b.WriteString(strings.TrimSpace(arguments))
		b.WriteString("\n")
	}
	if positional != "" {
		b.WriteString(indent)
		b.WriteString(strings.ReplaceAll(strings.TrimSpace(positional), "\n", "\n"+indent))
		b.WriteString("\n")
	}
	return b.String()
}

func zshCommandArguments(command string, subcommand ...string) string {
	metadata := commandByName(command)
	if metadata == nil {
		return ""
	}
	registerFlags := metadata.registerFlags
	completion := metadata.completion
	contextName := metadata.name
	if len(subcommand) > 0 {
		matched := completionSubcommand(metadata, subcommand[0])
		if matched == nil {
			return ""
		}
		registerFlags = matched.registerFlags
		completion = matched.completion
		contextName += " " + subcommand[0]
	}
	if registerFlags == nil {
		return ""
	}

	fs := flag.NewFlagSet(contextName, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	registerFlags(fs)

	var specs []string
	fs.VisitAll(func(fl *flag.Flag) {
		if reason := completion.flagExemptions[fl.Name]; reason != "" {
			return
		}
		prefix := ""
		if _, repeatable := fl.Value.(*stringListFlag); repeatable {
			prefix = "*"
		}
		value, described := completion.flagValues[fl.Name]
		flagName := "--" + fl.Name
		if len(fl.Name) == 1 {
			flagName = "-" + fl.Name
		}
		booleanFlag := isBooleanFlag(fl)
		description := "[" + zshArgumentsDescription(fl.Usage) + "]"
		spec := prefix + flagName + description
		switch {
		case described && booleanFlag && value.kind == completionValueBoolean:
			spec = prefix + flagName + "=-" + description + zshCompletionValue(value)
		case described && !booleanFlag:
			spec += zshCompletionValue(value)
		case !described && !booleanFlag:
			spec += ":value:"
		}
		specs = append(specs, zshSingleQuote(spec))
	})
	if len(specs) == 0 {
		return ""
	}
	return "      _arguments " + strings.Join(specs, " ")
}

func completionSubcommand(metadata *commandMetadata, name string) *commandSubcommand {
	for i := range metadata.subcommands {
		if slices.Contains(metadata.subcommands[i].completionNames(), name) {
			return &metadata.subcommands[i]
		}
	}
	return nil
}

func zshCompletionValue(value completionFlagValue) string {
	label := value.label
	if label == "" {
		label = "value"
	}
	switch value.kind {
	case completionValuePath:
		return ":" + label + ":_files"
	case completionValueBoolean, completionValueEnum:
		if len(value.values) == 0 {
			return ":" + label + ":"
		}
		return ":" + label + ":(" + strings.Join(value.values, " ") + ")"
	case completionValueText, completionValueCount:
		return ":" + label + ":"
	default:
		return ":" + label + ":"
	}
}

func isBooleanFlag(fl *flag.Flag) bool {
	boolean, ok := fl.Value.(interface{ IsBoolFlag() bool })
	return ok && boolean.IsBoolFlag()
}

func zshArgumentsDescription(description string) string {
	return strings.NewReplacer(`\`, `\\`, `]`, `\]`).Replace(description)
}

func zshSingleQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}

func zshPositionalCompletion(command string, subcommand ...string) string {
	metadata := commandByName(command)
	if metadata == nil {
		return ""
	}
	completion := metadata.completion
	registerFlags := metadata.registerFlags
	contextName := metadata.name
	start := 3
	if len(subcommand) > 0 {
		matched := completionSubcommand(metadata, subcommand[0])
		if matched == nil {
			return ""
		}
		completion = matched.completion
		registerFlags = matched.registerFlags
		if registerFlags == nil && !hasSubcommandFlagRegistrations(metadata) {
			registerFlags = metadata.registerFlags
		}
		contextName += " " + matched.name
		start++
	}
	positional := completion.positional
	if positional.index == 0 {
		return ""
	}

	repeat := 0
	if positional.repeat {
		repeat = 1
	}
	disallowedFlags := "," + strings.Join(positional.disallowAfterFlags, ",") + ","
	condition := "_tao_at_positional " + strconv.Itoa(start) + " " + strconv.Itoa(positional.index) + " " + strconv.Itoa(repeat) + " " + zshSingleQuote(disallowedFlags)
	for _, name := range zshValueFlagNames(contextName, registerFlags) {
		condition += " " + zshSingleQuote(name)
	}

	var action string
	switch {
	case positional.completer != "":
		action = "  plan_ids=(\"${(@f)$(${words[1]} complete " + string(positional.completer) + " 2>/dev/null)}\")\n" +
			"  compadd -a plan_ids"
	case len(positional.candidates) > 0:
		candidates := make([]string, 0, len(positional.candidates))
		for _, candidate := range positional.candidates {
			candidates = append(candidates, zshSingleQuote(candidate))
		}
		action = "  compadd -- " + strings.Join(candidates, " ")
	default:
		action = "  _message " + zshSingleQuote(positional.label)
	}
	return "if " + condition + "; then\n" + action + "\nfi"
}

func zshValueFlagNames(contextName string, registerFlags func(*flag.FlagSet)) []string {
	if registerFlags == nil {
		return nil
	}
	fs := flag.NewFlagSet(contextName, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	registerFlags(fs)
	var names []string
	fs.VisitAll(func(fl *flag.Flag) {
		if isBooleanFlag(fl) {
			return
		}
		prefix := "--"
		if len(fl.Name) == 1 {
			prefix = "-"
		}
		names = append(names, prefix+fl.Name)
	})
	return names
}

func zshSubcommandEntriesAt(metadata *commandMetadata, indent string) string {
	if metadata == nil {
		return ""
	}
	var entries []string
	for _, subcommand := range metadata.subcommands {
		for _, name := range subcommand.completionNames() {
			entries = append(entries, zshSingleQuote(name+":"+subcommand.description))
		}
	}
	if len(entries) == 0 {
		return ""
	}
	return indent + "subcommands=(" + strings.Join(entries, " ") + ")"
}

func zshCommandEntries() string {
	var b strings.Builder
	for _, metadata := range commandRegistry {
		if metadata.completionDescription == "" {
			continue
		}
		aliases := []string{metadata.name}
		if metadata.minPrefix != "" && metadata.minPrefix != metadata.name {
			aliases = []string{metadata.minPrefix, metadata.name}
		}
		for _, alias := range aliases {
			b.WriteString("    ")
			b.WriteString(zshSingleQuote(alias + ":" + metadata.completionDescription))
			b.WriteString("\n")
		}
	}
	return strings.TrimSuffix(b.String(), "\n")
}

func commandAliasPattern(command string) string {
	for _, metadata := range commandRegistry {
		if metadata.name == command {
			aliases := commandAliases(metadata)
			if command == "completion" {
				aliases = slices.DeleteFunc(aliases, func(value string) bool { return value == "complet" })
			}
			return strings.Join(aliases, "|")
		}
	}
	return command
}
