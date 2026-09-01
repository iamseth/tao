package cli

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"io"
	"os"
	"os/exec"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/prompts"
)

func TestCommandAliases(t *testing.T) {
	tests := []struct {
		name string
		want []string
	}{
		{name: "list", want: []string{"l", "li", "lis", "list"}},
		{name: "repo", want: []string{"repo"}},
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
	if !strings.Contains(script, "'update:Update Tao to the latest stable release'") {
		t.Fatalf("completion script is missing update command: %q", script)
	}
	if strings.Contains(script, "'up:Update Tao") {
		t.Fatalf("completion script contains an unrequested update alias: %q", script)
	}
}

func TestZshCommandEntriesUseShortestAliasAndFullCommandInRegistryOrder(t *testing.T) {
	entries := strings.Split(zshCommandEntries(), "\n")
	wantPrefix := []string{
		"    'l:List plans'",
		"    'list:List plans'",
		"    'version:Show build commit'",
		"    'update:Update Tao to the latest stable release'",
		"    'init:Register this repository in Tao data home'",
		"    'lo:Show or follow agent run log'",
		"    'log:Show or follow agent run log'",
		"    'r:Run pending slices with the selected agent'",
		"    'run:Run pending slices with the selected agent'",
	}
	if len(entries) < len(wantPrefix) {
		t.Fatalf("zshCommandEntries produced %d entries, want at least %d", len(entries), len(wantPrefix))
	}
	for i, want := range wantPrefix {
		if entries[i] != want {
			t.Fatalf("entry %d = %q, want %q", i, entries[i], want)
		}
	}

	for _, notWant := range []string{"    'li:List plans'", "    'ru:Run pending slices with the selected agent'", "    'v:Show build commit'"} {
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
	want, err := os.ReadFile("testdata/zsh_completion.golden")
	if err != nil {
		t.Fatal(err)
	}
	got := []byte(buildZshCompletionScript())
	if !bytes.Equal(got, want) {
		t.Fatalf("generated zsh completion differs from golden: got %d bytes, want %d; regenerate testdata/zsh_completion.golden", len(got), len(want))
	}
}

func TestCommandAliasPatternCompletionSpecialCases(t *testing.T) {
	tests := []struct {
		command string
		want    string
	}{
		{command: "list", want: "l|li|lis|list"},
		{command: "completion", want: "co|com|comp|compl|comple|completi|completio|completion"}, //nolint:misspell // intentional completion-prefix fixture
		{command: "repo", want: "repo"},
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
				"'--auto-rework=-[automatically rework plans with requested changes]:boolean:(true false)'",
				"'--commit-policy[automatic commit policy: slice or none]:policy:(slice none)'",
				"'--execution-mode[execution mode: isolated or current]:mode:(isolated current)'",
				"'--max-rework-attempts[maximum automatic rework cycles (0 disables)]:count:'",
				"'--max-slices[maximum slices to run; use 0 for all]:count:'",
			},
		},
		{
			command: "merge",
			want: []string{
				"'--auto-eject[automatically eject an attributed non-converging plan and reland the rest]'",
				"'--verify-command[override the post-merge build/test verification command]:command:'",
			},
		},
		{
			command: "commit",
			want: []string{
				"'--context[print bounded safety-filtered commit context as JSON]'",
				"'--message[explicit full canonical commit message]:message:'",
				"'--proposal-file[JSON file containing a structured commit proposal]:path:_files'",
				"'--repo-root[repository path]:path:_files'",
			},
		},
		{
			command: "report",
			want: []string{
				"'--output[output Markdown path, or - for stdout (required)]:path:_files'",
				"'--planning-only[export a synthesized planning record without execution history]'",
				"'--force[safely replace an existing output file]'",
			},
		},
		{
			command: "abandon",
			want: []string{
				"'--reason[required reason for abandoning the plan]:text:'",
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
		{command: "edit", parentFlags: true, subcommandFlag: map[string]bool{"remove": false, "skip": false, "move": true}},
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
		"--output[output Markdown path, or - for stdout (required)]:path:_files",
		"--planning-only[export a synthesized planning record without execution history]",
		"--force[safely replace an existing output file]",
		"complete plan-ids",
	} {
		if !strings.Contains(reportCase, want) {
			t.Errorf("report completion case missing %q", want)
		}
	}
	repoCase := zshCommandCase(commandByName("repo"))
	for _, want := range []string{
		"'list:List registered repositories and health summaries'",
		"'show:Show details for one registered repository'",
		"'doctor:Check registered repositories for health problems'",
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
	if got := zshParentCompletionContext(commandByName("note"), ""); !strings.Contains(got, "--repo[registered repository ID prefix or exact name]") {
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

	const want = "'--auto-rework=-[automatically rework plans with requested changes]:boolean:(true false)'"
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			arguments := zshCommandArguments(test.command, test.subcommands...)
			if !strings.Contains(arguments, want) {
				t.Fatalf("generated arguments missing equals-only boolean completion %q in %q", want, arguments)
			}

			args := append([]string{"--auto-rework=false"}, test.positionals...)
			fs, positional, err := (App{Err: io.Discard}).parseArgs(test.name, args, test.register)
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
	for _, want := range []string{"--all[merge every reviewed and approved plan in one atomic batch]", "--auto-eject[automatically eject an attributed non-converging plan and reland the rest]", "--dry-run[preview batch candidates and order without durable changes]", "--restart[discard safe pre-landing batch recovery state and start again]", "--force[bypass approval, review-base, and dirty-worktree gates (single-plan only)]", "--record-only[record an external merge and run cleanup (single-plan only)]", "--no-squash[preserve plan commits with rebase-plus-fast-forward (single-plan only)]", "--no-verify[skip post-merge build/test verification (single-plan only)]", "--verify-command[override the post-merge build/test verification command]:command:", "$(${words[1]} complete plan-ids 2>/dev/null)"} {
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
	app := App{Out: &out, Err: &out}
	if err := app.completion([]string{"zsh"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "compdef _tao") {
		t.Fatalf("expected zsh completion script, got %q", out.String())
	}
	for _, want := range []string{"repo:Inspect registered repositories", "'show:Show details for one registered repository'", "note:Capture and maintain repository notes", "'create:Create a note from arguments or standard input'", "'c:Create a note from arguments or standard input'", "complete note-ids", "approve:Approve a gated slice", "stale:Check whether a plan is stale against its recorded base commit", "cleanup:Remove merged Tao branches and worktrees", "delete:Delete local plan artifacts", "edit:Edit pending slices in a plan", "validate:Validate plan artifacts and verification commands", "--slice[slice id to approve]", "--by[approver name]", "--commit-policy[automatic commit policy: slice or none]:policy:(slice none)", "--commit-policy[run prompt commit policy: slice or none]:policy:(slice none)", "--execution-mode[execution mode: isolated or current]", "--continue[continue a blocked slice at its preserved execution boundary]", "--no-review[disable automatic plan review for this run]", "--run[run a fresh review before displaying the result]", "--dry-run[show branches and worktrees that would be removed]", "--force[also remove unmerged branches and dirty worktrees]", "--force[confirm deletion of local plan artifacts]", "--before[move before slice id]", "--after[move after slice id]"} {
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
	app := App{Out: &out, Err: &out}
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
