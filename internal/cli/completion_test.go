package cli

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"io"
	"os/exec"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/prompts"
)

func zshFlagSpec(t *testing.T, command, subcommand, flagName, valueSuffix string) string {
	t.Helper()
	metadata := commandByName(command)
	if metadata == nil {
		t.Fatalf("missing command metadata for %q", command)
	}
	register := metadata.registerFlags
	if subcommand != "" {
		matched := completionSubcommand(metadata, subcommand)
		if matched == nil {
			t.Fatalf("missing subcommand %q for %q", subcommand, command)
		}
		register = matched.registerFlags
	}
	fs := flag.NewFlagSet(command+" "+subcommand, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	if register != nil {
		register(fs)
	}
	fl := fs.Lookup(flagName)
	if fl == nil {
		t.Fatalf("missing flag %q for %s %s", flagName, command, subcommand)
	}
	prefix := "--"
	if len(flagName) == 1 {
		prefix = "-"
	}
	return prefix + flagName + "[" + zshArgumentsDescription(fl.Usage) + "]" + valueSuffix
}

func zshEntry(t *testing.T, alias, command, subcommand string) string {
	t.Helper()
	metadata := commandByName(command)
	if metadata == nil {
		t.Fatalf("missing command metadata for %q", command)
	}
	description := metadata.completionDescription
	if subcommand != "" {
		matched := completionSubcommand(metadata, subcommand)
		if matched == nil {
			t.Fatalf("missing subcommand %q for %q", subcommand, command)
		}
		description = matched.description
	}
	return strings.TrimSuffix(strings.TrimPrefix(zshSingleQuote(alias+":"+description), "'"), "'")
}

func TestReviewAgentFlagScopeAndCompletion(t *testing.T) {
	for _, command := range []commandMetadata{runCommand, reviewCommand, reworkCommand} {
		fs := flag.NewFlagSet(command.name, flag.ContinueOnError)
		command.registerFlags(fs)
		if fs.Lookup("review-agent") == nil {
			t.Fatalf("%s missing review selector", command.name)
		}
		if got := command.completion.flagValues["review-agent"].values; !slices.Equal(got, []string{"pi", "claude"}) {
			t.Fatalf("%s values: %v", command.name, got)
		}
	}
	fs := flag.NewFlagSet("merge", flag.ContinueOnError)
	registerMergeFlags(fs)
	if fs.Lookup("review-agent") != nil {
		t.Fatal("merge acquired plan review selector")
	}
	fs = flag.NewFlagSet("note run", flag.ContinueOnError)
	(newTestApp(App{})).registerRunRequestFlags(fs)
	if fs.Lookup("review-agent") != nil {
		t.Fatal("shared note-run flags acquired review selector")
	}
	for _, subcommand := range repoCommand.subcommands {
		if subcommand.name == "config" && !slices.Equal(subcommand.completion.flagValues["review-agent"].values, []string{"pi", "claude", "unset"}) {
			t.Fatal("repo config selector completions missing")
		}
	}
}

func TestCommandAliases(t *testing.T) {
	tests := []struct {
		name string
		want []string
	}{
		{name: "list", want: []string{"l", "li", "lis", "list"}},
		{name: "repo", want: []string{"repo"}},
		{name: "config", want: []string{"config"}},
		{name: "report", want: []string{"report"}},
		{name: "note", want: []string{"n", "no", "not", "note"}},
		{name: "monitor", want: []string{"mon", "moni", "monit", "monito", "monitor"}},
		{name: "slice-complete", want: []string{"slice-complete"}},
		{name: "abandon", want: []string{"abandon"}},
		{name: "commit", want: []string{"commit"}},
		{name: "version", want: []string{"version"}},
		{name: "update", want: []string{"update"}},
		{name: "completion", want: []string{"co", "com", "comp", "compl", "comple", "complet", "completi", "completio", "completion"}}, //nolint:misspell // intentional completion-prefix fixture
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			metadata := commandByName(test.name)
			if metadata == nil {
				t.Fatalf("missing command metadata for %q", test.name)
			}
			if got := commandAliases(*metadata); !reflect.DeepEqual(got, test.want) {
				t.Fatalf("commandAliases(%q) = %#v, want %#v", test.name, got, test.want)
			}
		})
	}
}

func TestZshCompletionIncludesExactUpdateCommand(t *testing.T) {
	script := buildZshCompletionScript()
	if !strings.Contains(script, "'"+zshEntry(t, "update", "update", "")+"'") {
		t.Fatalf("completion script is missing update command: %q", script)
	}
	if strings.Contains(script, "'"+zshEntry(t, "up", "update", "")) {
		t.Fatalf("completion script contains an unrequested update alias: %q", script)
	}
}

func TestZshCommandEntriesUseShortestAliasAndFullCommandInRegistryOrder(t *testing.T) {
	entries := strings.Split(zshCommandEntries(), "\n")
	wantPrefix := []string{
		"    '" + zshEntry(t, "l", "list", "") + "'",
		"    '" + zshEntry(t, "list", "list", "") + "'",
		"    '" + zshEntry(t, "version", "version", "") + "'",
		"    '" + zshEntry(t, "update", "update", "") + "'",
		"    '" + zshEntry(t, "init", "init", "") + "'",
		"    '" + zshEntry(t, "lo", "log", "") + "'",
		"    '" + zshEntry(t, "log", "log", "") + "'",
		"    '" + zshEntry(t, "r", "run", "") + "'",
		"    '" + zshEntry(t, "run", "run", "") + "'",
	}
	if len(entries) < len(wantPrefix) {
		t.Fatalf("zshCommandEntries produced %d entries, want at least %d", len(entries), len(wantPrefix))
	}
	for i, want := range wantPrefix {
		if entries[i] != want {
			t.Fatalf("entry %d = %q, want %q", i, entries[i], want)
		}
	}

	for _, notWant := range []string{"    '" + zshEntry(t, "li", "list", "") + "'", "    '" + zshEntry(t, "ru", "run", "") + "'", "    '" + zshEntry(t, "v", "version", "") + "'"} {
		if slices.Contains(entries, notWant) {
			t.Fatalf("zshCommandEntries should not include generated intermediate alias %q", notWant)
		}
	}
}

func TestZshCommandCasesFollowRegistryOrder(t *testing.T) {
	script := buildZshCompletionScript()
	cursor := strings.Index(script, "  case ${words[2]} in\n")
	if cursor < 0 {
		t.Fatal("completion script is missing top-level command dispatch")
	}
	for i := range commandRegistry {
		metadata := &commandRegistry[i]
		if metadata.completionDescription == "" {
			continue
		}
		marker := "    " + commandAliasPattern(metadata.name) + ")\n"
		offset := strings.Index(script[cursor:], marker)
		if offset < 0 {
			t.Fatalf("completion script is missing registry command case %q", metadata.name)
		}
		cursor += offset + len(marker)
	}
}

func TestZshCompletionScriptMatchesGolden(t *testing.T) {
	assertGolden(t, "testdata/zsh_completion.golden", []byte(buildZshCompletionScript()))
}

func TestCommandAliasPatternCompletionSpecialCases(t *testing.T) {
	tests := []struct {
		command string
		want    string
	}{
		{command: "list", want: "l|li|lis|list"},
		{command: "completion", want: "co|com|comp|compl|comple|completi|completio|completion"}, //nolint:misspell // intentional completion-prefix fixture
		{command: "repo", want: "repo"},
		{command: "config", want: "config"},
		{command: "report", want: "report"},
		{command: "note", want: "n|no|not|note"},
		{command: "monitor", want: "mon|moni|monit|monito|monitor"},
		{command: "slice-complete", want: "slice-complete"},
		{command: "abandon", want: "abandon"},
		{command: "commit", want: "commit"},
		{command: "update", want: "update"},
		{command: "missing", want: "missing"},
	}

	for _, test := range tests {
		t.Run(test.command, func(t *testing.T) {
			if got := commandAliasPattern(test.command); got != test.want {
				t.Fatalf("commandAliasPattern(%q) = %q, want %q", test.command, got, test.want)
			}
		})
	}
}

func TestZshCommandArgumentsUseRegisteredFlagsAndSemanticHints(t *testing.T) {
	tests := []struct {
		command string
		want    []string
	}{
		{
			command: "run",
			want: []string{
				zshSingleQuote(zshFlagSpec(t, "run", "", "agent", ":agent:(pi claude)")),
				zshSingleQuote(zshFlagSpec(t, "run", "", "session-timeout", ":duration:")),
				zshSingleQuote(zshFlagSpec(t, "run", "", "dangerously-skip-permissions", "")),
				zshSingleQuote(strings.Replace(zshFlagSpec(t, "run", "", "auto-rework", ":boolean:(true false)"), "--auto-rework[", "--auto-rework=-[", 1)),
				zshSingleQuote(zshFlagSpec(t, "run", "", "rework-escalation-from-attempt", ":count:")),
				zshSingleQuote(zshFlagSpec(t, "run", "", "commit-policy", ":policy:(slice none)")),
				zshSingleQuote(zshFlagSpec(t, "run", "", "execution-mode", ":mode:(isolated current)")),
				zshSingleQuote(zshFlagSpec(t, "run", "", "max-rework-attempts", ":count:")),
				zshSingleQuote(zshFlagSpec(t, "run", "", "review-agent", ":agent:(pi claude)")),
				zshSingleQuote(zshFlagSpec(t, "run", "", "max-slices", ":count:")),
			},
		},
		{
			command: "note",
			want: []string{
				zshSingleQuote(zshFlagSpec(t, "note", "", "agent", ":agent:(pi claude)")),
				zshSingleQuote(zshFlagSpec(t, "note", "", "session-timeout", ":duration:")),
			},
		},
		{
			command: "merge",
			want: []string{
				zshSingleQuote(zshFlagSpec(t, "merge", "", "auto-eject", "")),
				zshSingleQuote(zshFlagSpec(t, "merge", "", "verify-command", ":command:")),
			},
		},
		{
			command: "commit",
			want: []string{
				zshSingleQuote(zshFlagSpec(t, "commit", "", "context", "")),
				zshSingleQuote(zshFlagSpec(t, "commit", "", "message", ":message:")),
				zshSingleQuote(zshFlagSpec(t, "commit", "", "proposal-file", ":path:_files")),
				zshSingleQuote(zshFlagSpec(t, "commit", "", "repo-root", ":path:_files")),
			},
		},
		{
			command: "report",
			want: []string{
				zshSingleQuote(zshFlagSpec(t, "report", "", "output", ":path:_files")),
				zshSingleQuote(zshFlagSpec(t, "report", "", "planning-only", "")),
				zshSingleQuote(zshFlagSpec(t, "report", "", "force", "")),
			},
		},
		{
			command: "abandon",
			want: []string{
				zshSingleQuote(zshFlagSpec(t, "abandon", "", "reason", ":text:")),
			},
		},
	}

	for _, test := range tests {
		t.Run(test.command, func(t *testing.T) {
			metadata := commandByName(test.command)
			if metadata == nil {
				t.Fatalf("missing command metadata for %q", test.command)
			}
			arguments := zshCommandArguments(test.command)
			for _, want := range test.want {
				if !strings.Contains(arguments, want) {
					t.Fatalf("zshCommandArguments(%q) missing %q in %q", test.command, want, arguments)
				}
			}

			fs := flag.NewFlagSet(test.command, flag.ContinueOnError)
			fs.SetOutput(io.Discard)
			metadata.registerFlags(fs)
			fs.VisitAll(func(fl *flag.Flag) {
				if !zshArgumentsContainFlag(arguments, "--"+fl.Name) {
					t.Errorf("zshCommandArguments(%q) omitted registered flag --%s", test.command, fl.Name)
				}
			})
		})
	}
}

func TestZshCompletionCoversRegisteredPublicFlagsByContext(t *testing.T) {
	script := buildZshCompletionScript()
	for i := range commandRegistry {
		metadata := &commandRegistry[i]
		var subcommandFlags map[string]bool
		for _, subcommand := range metadata.subcommands {
			if subcommand.registerFlags == nil {
				continue
			}
			if subcommandFlags == nil {
				subcommandFlags = make(map[string]bool)
			}
			for _, name := range subcommand.completionNames() {
				contextName := metadata.name + " " + name
				arguments := zshCommandArguments(metadata.name, name)
				assertRegisteredCompletionFlags(t, contextName, subcommand.registerFlags, subcommand.completion, arguments)
				if arguments != "" && !strings.Contains(script, arguments) {
					t.Errorf("generated completion for %s is not used by the zsh script", contextName)
				}
			}
			fs := flag.NewFlagSet(metadata.name+" "+subcommand.name, flag.ContinueOnError)
			fs.SetOutput(io.Discard)
			subcommand.registerFlags(fs)
			fs.VisitAll(func(fl *flag.Flag) { subcommandFlags[fl.Name] = true })
		}

		if subcommandFlags != nil {
			fs := flag.NewFlagSet(metadata.name, flag.ContinueOnError)
			fs.SetOutput(io.Discard)
			metadata.registerFlags(fs)
			fs.VisitAll(func(fl *flag.Flag) {
				if !subcommandFlags[fl.Name] && metadata.completion.flagExemptions[fl.Name] == "" {
					t.Errorf("%s registered flag --%s has no generated subcommand context or explicit exemption", metadata.name, fl.Name)
				}
			})
			continue
		}
		if metadata.registerFlags == nil {
			continue
		}
		arguments := zshCommandArguments(metadata.name)
		assertRegisteredCompletionFlags(t, metadata.name, metadata.registerFlags, metadata.completion, arguments)
		if arguments != "" && !strings.Contains(script, arguments) {
			t.Errorf("generated completion for %s is not used by the zsh script", metadata.name)
		}
	}
}

func TestCompletionRegistryMetadataIsValid(t *testing.T) {
	validCompleters := map[positionalCompleter]bool{
		completePlanIDs:         true,
		completeRunnablePlanIDs: true,
		completeNoteIDs:         true,
	}
	for i := range commandRegistry {
		metadata := &commandRegistry[i]
		t.Run(metadata.name, func(t *testing.T) {
			assertCompletionContextMetadata(t, metadata.name, metadata.registerFlags, metadata.completion, validCompleters)

			seenNames := make(map[string]string)
			for j := range metadata.subcommands {
				subcommand := &metadata.subcommands[j]
				contextName := metadata.name + " " + subcommand.name
				assertCompletionContextMetadata(t, contextName, subcommand.registerFlags, subcommand.completion, validCompleters)
				for _, name := range subcommand.completionNames() {
					if name == "" || strings.TrimSpace(name) != name || strings.ContainsAny(name, " /|") {
						t.Errorf("%s has invalid completion name %q", contextName, name)
					}
					if owner, exists := seenNames[name]; exists {
						t.Errorf("%s completion name %q is already owned by %s", contextName, name, owner)
					}
					seenNames[name] = contextName
					if got := completionSubcommand(metadata, name); got != subcommand {
						t.Errorf("completionSubcommand(%q, %q) did not resolve %s", metadata.name, name, contextName)
					}
				}
			}
		})
	}
}

func TestCompletionMetadataDeclaresStaticSelectors(t *testing.T) {
	if got := promptCommand.completion.positional.candidates; !reflect.DeepEqual(got, prompts.PromptNames()) {
		t.Fatalf("prompt candidates = %#v, want prompts.PromptNames() %#v", got, prompts.PromptNames())
	}
	if got, want := completionCommand.completion.positional.candidates, []string{"zsh"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("completion shell candidates = %#v, want %#v", got, want)
	}
}

func TestCompletionMetadataPreservesSubcommandFlagScopes(t *testing.T) {
	tests := []struct {
		command        string
		parentFlags    bool
		subcommandFlag map[string]bool
	}{
		{command: "note", parentFlags: true, subcommandFlag: map[string]bool{"create": false, "list": false, "show": false, "edit": false, "archive": false, "reopen": false, "run": false}},
		{command: "workspace", parentFlags: true, subcommandFlag: map[string]bool{"list": false, "prepare": false, "status": false, "clean": true}},
		{command: "edit", parentFlags: true, subcommandFlag: map[string]bool{"remove": false, "skip": false, "move": true, "amend": true}},
	}
	for _, test := range tests {
		t.Run(test.command, func(t *testing.T) {
			metadata := commandByName(test.command)
			if metadata == nil {
				t.Fatalf("missing command metadata for %q", test.command)
			}
			if got := metadata.registerFlags != nil; got != test.parentFlags {
				t.Errorf("parent flag registration = %t, want %t", got, test.parentFlags)
			}
			if len(metadata.subcommands) != len(test.subcommandFlag) {
				t.Fatalf("subcommand count = %d, want %d", len(metadata.subcommands), len(test.subcommandFlag))
			}
			for _, subcommand := range metadata.subcommands {
				want, exists := test.subcommandFlag[subcommand.name]
				if !exists {
					t.Errorf("unexpected subcommand %q", subcommand.name)
					continue
				}
				if got := subcommand.registerFlags != nil; got != want {
					t.Errorf("%s flag registration = %t, want %t", subcommand.name, got, want)
				}
			}
		})
	}
}

func assertCompletionContextMetadata(t *testing.T, contextName string, registerFlags func(*flag.FlagSet), completion completionContext, validCompleters map[positionalCompleter]bool) {
	t.Helper()
	registered := make(map[string]bool)
	booleanFlags := make(map[string]bool)
	if registerFlags != nil {
		fs := flag.NewFlagSet(contextName, flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		registerFlags(fs)
		fs.VisitAll(func(fl *flag.Flag) {
			registered[fl.Name] = true
			booleanFlags[fl.Name] = isBooleanFlag(fl)
		})
	}
	for name, value := range completion.flagValues {
		if !registered[name] {
			t.Errorf("%s completion hint names unregistered flag --%s", contextName, name)
		}
		if strings.TrimSpace(value.label) == "" {
			t.Errorf("%s completion hint for --%s has an empty label", contextName, name)
		}
	}
	for name, reason := range completion.flagExemptions {
		if !registered[name] {
			t.Errorf("%s completion exemption names unregistered flag --%s", contextName, name)
		}
		if strings.TrimSpace(reason) == "" {
			t.Errorf("%s completion exemption for --%s has an empty reason", contextName, name)
		}
	}

	positional := completion.positional
	configured := positional.index != 0 || positional.label != "" || len(positional.candidates) > 0 || positional.completer != "" || positional.repeat || len(positional.disallowAfterFlags) > 0
	if !configured {
		return
	}
	if positional.index < 1 {
		t.Errorf("%s positional index = %d, want a one-based logical index", contextName, positional.index)
	}
	if strings.TrimSpace(positional.label) == "" {
		t.Errorf("%s positional completion has an empty label", contextName)
	}
	if positional.completer != "" && len(positional.candidates) > 0 {
		t.Errorf("%s positional completion declares both dynamic and static sources", contextName)
	}
	if positional.completer != "" && !validCompleters[positional.completer] {
		t.Errorf("%s positional completion has unknown dynamic source %q", contextName, positional.completer)
	}
	for _, flagName := range positional.disallowAfterFlags {
		name := strings.TrimPrefix(flagName, "--")
		if flagName != "--"+name || !registered[name] {
			t.Errorf("%s positional completion disallows unregistered long flag %q", contextName, flagName)
		} else if !booleanFlags[name] {
			t.Errorf("%s positional completion disallows non-boolean flag %q", contextName, flagName)
		}
	}
	seenCandidates := make(map[string]bool)
	for _, candidate := range positional.candidates {
		if candidate == "" || strings.TrimSpace(candidate) != candidate {
			t.Errorf("%s positional completion has invalid static candidate %q", contextName, candidate)
		}
		if seenCandidates[candidate] {
			t.Errorf("%s positional completion repeats static candidate %q", contextName, candidate)
		}
		seenCandidates[candidate] = true
	}
}

func assertRegisteredCompletionFlags(t *testing.T, contextName string, registerFlags func(*flag.FlagSet), completion completionContext, arguments string) {
	t.Helper()
	fs := flag.NewFlagSet(contextName, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	registerFlags(fs)
	fs.VisitAll(func(fl *flag.Flag) {
		if reason := completion.flagExemptions[fl.Name]; reason != "" {
			return
		}
		prefix := "--"
		if len(fl.Name) == 1 {
			prefix = "-"
		}
		if !zshArgumentsContainFlag(arguments, prefix+fl.Name) {
			t.Errorf("generated completion for %s omitted registered flag %s%s", contextName, prefix, fl.Name)
		}
	})
}

func zshArgumentsContainFlag(arguments, flagName string) bool {
	return strings.Contains(arguments, flagName+"[") || strings.Contains(arguments, flagName+"=-[")
}

func TestZshCompletionScriptCompletesReportsWithoutChangingRepo(t *testing.T) {
	reportCase := zshCommandCase(commandByName("report"))
	for _, want := range []string{
		zshFlagSpec(t, "report", "", "output", ":path:_files"),
		zshFlagSpec(t, "report", "", "planning-only", ""),
		zshFlagSpec(t, "report", "", "force", ""),
		"complete plan-ids",
	} {
		if !strings.Contains(reportCase, want) {
			t.Errorf("report completion case missing %q", want)
		}
	}
	repoCase := zshCommandCase(commandByName("repo"))
	for _, want := range []string{
		"'" + zshEntry(t, "list", "repo", "list") + "'",
		"'" + zshEntry(t, "show", "repo", "show") + "'",
		"'" + zshEntry(t, "doctor", "repo", "doctor") + "'",
		zshFlagSpec(t, "repo", "config", "max-rework-attempts", ""),
		zshFlagSpec(t, "repo", "config", "rework-escalation-from-attempt", ""),
		"_describe -t commands 'repo command' subcommands",
	} {
		if !strings.Contains(repoCase, want) {
			t.Fatalf("repo completion case missing %q: %q", want, repoCase)
		}
	}
	for _, notWant := range []string{"--output", "complete plan-ids"} {
		if strings.Contains(repoCase, notWant) {
			t.Fatalf("repo completion unexpectedly contains report completion %q: %q", notWant, repoCase)
		}
	}
}

func TestZshCompletionPreservesSubcommandFlagScopes(t *testing.T) {
	for _, command := range []string{"edit", "workspace"} {
		t.Run(command, func(t *testing.T) {
			metadata := commandByName(command)
			if got := zshParentCompletionContext(metadata, ""); strings.Contains(got, "_arguments") {
				t.Fatalf("parent completion leaked subcommand-scoped flags: %q", got)
			}
		})
	}
	if got := zshParentCompletionContext(commandByName("note"), ""); !strings.Contains(got, zshFlagSpec(t, "note", "", "repo", "")) {
		t.Fatalf("note parent completion omitted shared flags: %q", got)
	}

	for _, test := range []struct {
		command, subcommand, aggregateFlag string
	}{
		{command: "edit", subcommand: "remove", aggregateFlag: "--before"},
		{command: "workspace", subcommand: "prepare", aggregateFlag: "--force"},
	} {
		t.Run(test.command+" "+test.subcommand, func(t *testing.T) {
			context := zshCommandArguments(test.command, test.subcommand) + zshPositionalCompletion(test.command, test.subcommand)
			if strings.Contains(context, test.aggregateFlag) {
				t.Fatalf("%s %s completion leaked aggregate-only flag %s: %q", test.command, test.subcommand, test.aggregateFlag, context)
			}
		})
	}
}

func TestZshBooleanCompletionUsesEqualsOnlyParserSyntax(t *testing.T) {
	tests := []struct {
		name        string
		command     string
		subcommands []string
		register    func(*flag.FlagSet)
		positionals []string
	}{
		{
			name:        "run",
			command:     "run",
			register:    registerRunFlags,
			positionals: []string{"plan-id"},
		},
	}

	want := zshSingleQuote(strings.Replace(zshFlagSpec(t, "run", "", "auto-rework", ":boolean:(true false)"), "--auto-rework[", "--auto-rework=-[", 1))
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			arguments := zshCommandArguments(test.command, test.subcommands...)
			if !strings.Contains(arguments, want) {
				t.Fatalf("generated arguments missing equals-only boolean completion %q in %q", want, arguments)
			}

			args := append([]string{"--auto-rework=false"}, test.positionals...)
			fs, positional, err := (newTestApp(App{Err: io.Discard})).parseArgs(test.name, args, test.register)
			if err != nil {
				t.Fatalf("parse completed boolean argument: %v", err)
			}
			if flagBoolValue(fs, "auto-rework") {
				t.Fatal("completed --auto-rework=false parsed as true")
			}
			if !slices.Equal(positional, test.positionals) {
				t.Fatalf("positionals = %#v, want %#v", positional, test.positionals)
			}
		})
	}
}

func TestZshCompletionValueKinds(t *testing.T) {
	tests := []struct {
		name  string
		value completionFlagValue
		want  string
	}{
		{name: "text", value: completionFlagValue{kind: completionValueText, label: "command"}, want: ":command:"},
		{name: "path", value: completionFlagValue{kind: completionValuePath, label: "path"}, want: ":path:_files"},
		{name: "count", value: completionFlagValue{kind: completionValueCount, label: "count"}, want: ":count:"},
		{name: "boolean", value: completionFlagValue{kind: completionValueBoolean, label: "boolean", values: []string{"true", "false"}}, want: ":boolean:(true false)"},
		{name: "enum", value: completionFlagValue{kind: completionValueEnum, label: "policy", values: []string{"slice", "none"}}, want: ":policy:(slice none)"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := zshCompletionValue(test.value); got != test.want {
				t.Fatalf("zshCompletionValue() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestZshCompletionEscapesArgumentsDescriptions(t *testing.T) {
	got := zshSingleQuote("--owner[" + zshArgumentsDescription("owner's choice ]") + "]")
	want := `'--owner[owner'\''s choice \]]'`
	if got != want {
		t.Fatalf("escaped argument = %q, want %q", got, want)
	}
}

func TestZshCompletionHandlesFlagsBeforePositionals(t *testing.T) {
	zsh, err := exec.LookPath("zsh")
	if err != nil {
		t.Skip("zsh is not installed")
	}

	runCompletion := func(t *testing.T, words string, current int) string {
		t.Helper()
		harness := `compdef() { :; }
_arguments() { :; }
_describe() { :; }
_message() { print -r -- "message:$*"; }
_compadd() { print -r -- "compadd:$*"; }
compadd() { _compadd "$@"; }
_test_tao() { print -r -- example; }
` + buildZshCompletionScript() + "\nwords=(" + words + ")\nCURRENT=" + strconv.Itoa(current) + "\n_tao\n"
		cmd := exec.Command(zsh, "-f")
		cmd.Stdin = strings.NewReader(harness)
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("run zsh completion harness: %v\n%s", err, output)
		}
		return strings.TrimSpace(string(output))
	}

	t.Run("parent value flag", func(t *testing.T) {
		got := runCompletion(t, "_test_tao run --max-slices 1 ''", 5)
		if got != "compadd:-a plan_ids" {
			t.Fatalf("completion output = %q, want dynamic positional candidates", got)
		}
	})
	t.Run("subcommand boolean flag", func(t *testing.T) {
		got := runCompletion(t, "_test_tao workspace clean --force ''", 5)
		if got != "compadd:-a plan_ids" {
			t.Fatalf("completion output = %q, want dynamic positional candidates", got)
		}
	})
	t.Run("inherited parent flag value", func(t *testing.T) {
		got := runCompletion(t, "_test_tao note show --repo ''", 5)
		if got != "" {
			t.Fatalf("completion output = %q, want no positional candidates while completing inherited --repo value", got)
		}
	})
	t.Run("after inherited parent flag value", func(t *testing.T) {
		got := runCompletion(t, "_test_tao note show --repo my-repo ''", 6)
		if got != "compadd:-a plan_ids" {
			t.Fatalf("completion output = %q, want dynamic positional candidates after inherited --repo value", got)
		}
	})
	t.Run("static positional after value flag", func(t *testing.T) {
		got := runCompletion(t, "_test_tao prompt --plan-dir /tmp ''", 5)
		if !strings.Contains(got, "compadd:-- plan slice note-slice") {
			t.Fatalf("completion output = %q, want static prompt candidates", got)
		}
	})
	t.Run("separate flag value", func(t *testing.T) {
		got := runCompletion(t, "_test_tao run --max-slices ''", 4)
		if got != "" {
			t.Fatalf("completion output = %q, want no positional candidates while completing a flag value", got)
		}
	})
	t.Run("inline flag value", func(t *testing.T) {
		got := runCompletion(t, "_test_tao run '--max-slices='", 3)
		if got != "" {
			t.Fatalf("completion output = %q, want no positional candidates while completing an inline flag value", got)
		}
	})
	for _, command := range []string{"merge"} {
		t.Run(command+" all mode", func(t *testing.T) {
			got := runCompletion(t, "_test_tao "+command+" --all ''", 4)
			if got != "" {
				t.Fatalf("completion output = %q, want no positional candidates after --all", got)
			}
		})
	}
}

func TestZshCompletionScriptPreservesSpecialCompleteCommand(t *testing.T) {
	script := buildZshCompletionScript()
	if !strings.Contains(script, "$(${words[1]} complete plan-ids 2>/dev/null)") {
		t.Fatalf("expected completion script to call internal complete plan-ids command")
	}
	if !strings.Contains(script, "$(${words[1]} complete run-plan-ids 2>/dev/null)") {
		t.Fatalf("expected completion script to call internal complete run-plan-ids command")
	}
	if strings.Contains(script, "complet|") || strings.Contains(script, "|complet|") {
		t.Fatalf("expected completion alias pattern to omit ambiguous complet alias")
	}
}

func TestZshCompletionScriptCompletesMergePlanIDs(t *testing.T) {
	mergeCase := zshCommandCase(commandByName("merge"))
	for _, want := range []string{
		zshFlagSpec(t, "merge", "", "all", ""),
		zshFlagSpec(t, "merge", "", "auto-eject", ""),
		zshFlagSpec(t, "merge", "", "dry-run", ""),
		zshFlagSpec(t, "merge", "", "restart", ""),
		zshFlagSpec(t, "merge", "", "force", ""),
		zshFlagSpec(t, "merge", "", "record-only", ""),
		zshFlagSpec(t, "merge", "", "no-squash", ""),
		zshFlagSpec(t, "merge", "", "no-verify", ""),
		zshFlagSpec(t, "merge", "", "verify-command", ":command:"),
		"$(${words[1]} complete plan-ids 2>/dev/null)",
	} {
		if !strings.Contains(mergeCase, want) {
			t.Fatalf("expected merge completion case to contain %q, got %q", want, mergeCase)
		}
	}
	if strings.Contains(mergeCase, "compadd all") {
		t.Fatalf("merge completion must expose --all as a flag, not positional all: %q", mergeCase)
	}
}

func TestCompletionAndCompleteCommands(t *testing.T) {
	var out bytes.Buffer
	app := newTestApp(App{Out: &out, Err: &out})
	if err := app.completion([]string{"zsh"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "compdef _tao") {
		t.Fatalf("expected zsh completion script, got %q", out.String())
	}
	for _, want := range []string{
		zshEntry(t, "repo", "repo", ""),
		"'" + zshEntry(t, "show", "repo", "show") + "'",
		zshEntry(t, "note", "note", ""),
		"'" + zshEntry(t, "create", "note", "create") + "'",
		"'" + zshEntry(t, "c", "note", "create") + "'",
		"complete note-ids",
		zshEntry(t, "approve", "approve", ""),
		zshEntry(t, "stale", "staleness", ""),
		zshEntry(t, "cleanup", "cleanup", ""),
		zshEntry(t, "delete", "delete", ""),
		zshEntry(t, "edit", "edit", ""),
		zshEntry(t, "validate", "validate", ""),
		zshFlagSpec(t, "approve", "", "slice", ""),
		zshFlagSpec(t, "approve", "", "by", ""),
		zshFlagSpec(t, "run", "", "commit-policy", ":policy:(slice none)"),
		zshFlagSpec(t, "prompt", "", "commit-policy", ":policy:(slice none)"),
		zshFlagSpec(t, "run", "", "execution-mode", ""),
		zshFlagSpec(t, "run", "", "continue", ""),
		zshFlagSpec(t, "run", "", "no-review", ""),
		zshFlagSpec(t, "review", "", "run", ""),
		zshFlagSpec(t, "cleanup", "", "dry-run", ""),
		zshFlagSpec(t, "cleanup", "", "force", ""),
		zshFlagSpec(t, "delete", "", "force", ""),
		zshFlagSpec(t, "edit", "move", "before", ""),
		zshFlagSpec(t, "edit", "move", "after", ""),
	} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("expected completion to contain %q, got %q", want, out.String())
		}
	}
	if strings.Contains(out.String(), ":policy:(plan ") {
		t.Fatalf("expected completion to omit plan as an executable commit policy, got %q", out.String())
	}
	if zsh, err := exec.LookPath("zsh"); err == nil {
		cmd := exec.Command(zsh, "-n") //nolint:gosec // G204: zsh path from exec.LookPath in test
		cmd.Stdin = strings.NewReader(out.String())
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("zsh completion syntax failed: %v\n%s", err, output)
		}
	}

	out.Reset()
	repo := fakeRepository{summaries: []plan.PlanSummary{
		{ID: "20260427-1810-example", Status: plan.StatusPlanned, PendingCount: 1},
		{ID: "20260427-1900-done", Status: plan.StatusCompleted},
		{ID: "20260428-0900-duplicate", Status: plan.StatusPlanned, PendingCount: 1},
		{ID: "20260428-1000-duplicate", Status: plan.StatusPlanned, PendingCount: 1},
	}}
	if err := app.complete(context.Background(), repo, []string{"plan-ids"}); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out.String()) != "example\ndone\n20260428-0900-duplicate\n20260428-1000-duplicate" {
		t.Fatalf("unexpected complete output %q", out.String())
	}

	out.Reset()
	if err := app.complete(context.Background(), repo, []string{"run-plan-ids"}); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out.String()) != "example\n20260428-0900-duplicate\n20260428-1000-duplicate" {
		t.Fatalf("unexpected run complete output %q", out.String())
	}
}

func TestCompletionAndCompleteUsageErrors(t *testing.T) {
	var out bytes.Buffer
	app := newTestApp(App{Out: &out, Err: &out})
	if err := app.completion([]string{"bash"}); err == nil {
		t.Fatal("expected completion usage error")
	}
	if err := app.complete(context.Background(), fakeRepository{}, []string{"bad"}); err == nil {
		t.Fatal("expected complete usage error")
	}
	err := app.complete(context.Background(), fakeRepository{err: errors.New("list failed")}, []string{"plan-ids"})
	if err == nil || !strings.Contains(err.Error(), "list failed") {
		t.Fatalf("expected complete repo error, got %v", err)
	}
}
