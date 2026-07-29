package cli

import (
	"flag"
	"io"
	"slices"
	"strconv"
	"strings"
)

func buildZshCompletionScript() string {
	return `#compdef tao

_tao() {
  local -a commands plan_ids subcommands

  commands=(
` + zshCommandEntries() + `
  )

  if (( CURRENT == 2 )); then
    _describe -t commands 'tao command' commands
    return
  fi

	case ${words[2]} in
	    ` + commandAliasPattern("list") + `)
` + zshCommandArguments("list") + `
	      ;;
    init)
` + zshCommandArguments("init") + `
      ;;
	    ` + commandAliasPattern("run") + `)
` + zshCommandArguments("run") + `
` + zshPositionalCompletion("run") + `
      ;;
    commit)
` + zshCommandArguments("commit") + `
      ;;
    ` + commandAliasPattern("queue") + `)
      if (( CURRENT == 3 )); then
` + zshSubcommandEntries("queue") + `
        _describe -t commands 'queue command' subcommands
        return
      fi
      case ${words[3]} in
        add)
` + zshPositionalCompletion("queue", "add") + `
          ;;
        start)
` + zshCommandArguments("queue", "start") + `
          ;;
        status)
` + zshCommandArguments("queue", "status") + `
          ;;
        stop|dequeue)
` + zshPositionalCompletion("queue", "stop") + `
          ;;
      esac
      ;;
    ` + commandAliasPattern("repo") + `)
      if (( CURRENT == 3 )); then
        compadd list show doctor
      fi
      ;;
    ` + commandAliasPattern("note") + `)
` + zshCommandArguments("note") + `
      if (( CURRENT == 3 )); then
        compadd c create l list s show e edit a archive reopen p plan r run
        return
      fi
      case ${words[3]} in
        s|show|e|edit|a|archive|reopen|p|plan|r|run)
          if (( CURRENT == 4 )); then
            plan_ids=("${(@f)$(${words[1]} complete note-ids 2>/dev/null)}")
            compadd -a plan_ids
          fi
          ;;
      esac
      ;;
    ` + commandAliasPattern("approve") + `)
` + zshCommandArguments("approve") + `
      if (( CURRENT == 3 )); then
        plan_ids=("${(@f)$(${words[1]} complete plan-ids 2>/dev/null)}")
        compadd -a plan_ids
      fi
      ;;
    slice-complete)
` + zshCommandArguments("slice-complete") + `
      ;;
    slice-blocked)
` + zshCommandArguments("slice-blocked") + `
      ;;
    ` + commandAliasPattern("cleanup") + `)
` + zshCommandArguments("cleanup") + `
      ;;
    ` + commandAliasPattern("merge") + `)
` + zshCommandArguments("merge") + `
` + zshPositionalCompletion("merge") + `
      ;;
    ` + commandAliasPattern("workspace") + `)
      if (( CURRENT == 3 )); then
        compadd list prepare status clean
        return
      fi
      case ${words[3]} in
        prepare|status)
          if (( CURRENT == 4 )); then
            plan_ids=("${(@f)$(${words[1]} complete plan-ids 2>/dev/null)}")
            compadd -a plan_ids
          fi
          ;;
        clean)
` + zshCommandArguments("workspace", "clean") + `
          if (( CURRENT == 4 )); then
            plan_ids=("${(@f)$(${words[1]} complete plan-ids 2>/dev/null)}")
            compadd -a plan_ids
          fi
          ;;
      esac
      ;;
    ` + commandAliasPattern("delete") + `)
` + zshCommandArguments("delete") + `
      if (( CURRENT == 3 )); then
        plan_ids=("${(@f)$(${words[1]} complete plan-ids 2>/dev/null)}")
        compadd -a plan_ids
      fi
      ;;
    ` + commandAliasPattern("edit") + `)
      if (( CURRENT == 3 )); then
        compadd remove skip move
        return
      fi
      case ${words[3]} in
        remove|skip)
          if (( CURRENT == 4 )); then
            plan_ids=("${(@f)$(${words[1]} complete plan-ids 2>/dev/null)}")
            compadd -a plan_ids
          fi
          ;;
        move)
` + zshCommandArguments("edit", "move") + `
          if (( CURRENT == 4 )); then
            plan_ids=("${(@f)$(${words[1]} complete plan-ids 2>/dev/null)}")
            compadd -a plan_ids
          fi
          ;;
      esac
      ;;
    ` + commandAliasPattern("capture-planning-session") + `)
` + zshCommandArguments("capture-planning-session") + `
      ;;
    ` + commandAliasPattern("log") + `|` + commandAliasPattern("show") + `|` + commandAliasPattern("review") + `|` + commandAliasPattern("rework") + `|` + commandAliasPattern("staleness") + `|` + commandAliasPattern("validate") + `)
      if [[ ${words[2]} == rev* ]]; then
` + zshCommandArguments("review") + `
      fi
      if [[ ${words[2]} == rew* ]]; then
` + zshCommandArguments("rework") + `
      fi
      if (( CURRENT == 3 )); then
        plan_ids=("${(@f)$(${words[1]} complete plan-ids 2>/dev/null)}")
        compadd -a plan_ids
      fi
      if [[ ${words[2]} == lo* ]]; then
` + zshCommandArguments("log") + `
      fi
      ;;
    ` + commandAliasPattern("status") + `)
` + zshCommandArguments("status") + `
      ;;
    ` + commandAliasPattern("insights") + `)
` + zshCommandArguments("insights") + `
      ;;
	    ` + commandAliasPattern("prompt") + `)
` + zshCommandArguments("prompt") + `
	      compadd note slice run commit grill-me improve-codebase-architecture improve-documentation repo-health pr
      ;;
    ` + commandAliasPattern("draft-prompt") + `)
` + zshCommandArguments("draft-prompt") + `
      ;;
    ` + commandAliasPattern("install-prompts") + `)
` + zshCommandArguments("install-prompts") + `
      ;;
    ` + commandAliasPattern("doctor") + `)
` + zshCommandArguments("doctor") + `
      ;;
    ` + commandAliasPattern("completion") + `)
      compadd zsh
      ;;
  esac
}

compdef _tao tao ./bin/tao bin/tao
`
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
	for _, spec := range completion.argumentSpecs {
		specs = append(specs, zshSingleQuote(spec))
	}
	if len(specs) == 0 {
		return ""
	}
	return "      _arguments " + strings.Join(specs, " ")
}

func completionSubcommand(metadata *commandMetadata, name string) *commandSubcommand {
	for i := range metadata.subcommands {
		if slices.Contains(metadata.subcommands[i].completionNames, name) {
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
	if len(subcommand) > 0 {
		matched := completionSubcommand(metadata, subcommand[0])
		if matched == nil {
			return ""
		}
		completion = matched.completion
	}
	if completion.positional.completer == "" {
		return ""
	}
	positional := completion.positional
	operator := "=="
	if positional.repeat {
		operator = ">="
	}
	return "      if (( CURRENT " + operator + " " + strconv.Itoa(positional.position) + " )); then\n" +
		"        plan_ids=(\"${(@f)$(${words[1]} complete " + string(positional.completer) + " 2>/dev/null)}\")\n" +
		"        compadd -a plan_ids\n" +
		"      fi"
}

func zshSubcommandEntries(command string) string {
	metadata := commandByName(command)
	if metadata == nil {
		return ""
	}
	var entries []string
	for _, subcommand := range metadata.subcommands {
		for _, name := range subcommand.completionNames {
			entries = append(entries, zshSingleQuote(name+":"+subcommand.description))
		}
	}
	if len(entries) == 0 {
		return ""
	}
	return "        subcommands=(" + strings.Join(entries, " ") + ")"
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
			b.WriteString("    '")
			b.WriteString(alias)
			b.WriteString(":")
			b.WriteString(metadata.completionDescription)
			b.WriteString("'\n")
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
