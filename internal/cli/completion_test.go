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
	"strings"
	"testing"

	"github.com/iamseth/tao/internal/plan"
)

func TestCommandAliases(t *testing.T) {
	tests := []struct {
		name string
		want []string
	}{
		{name: "list", want: []string{"l", "li", "lis", "list"}},
		{name: "repo", want: []string{"repo"}},
		{name: "note", want: []string{"n", "no", "not", "note"}},
		{name: "slice-complete", want: []string{"slice-complete"}},
		{name: "version", want: []string{"version"}},
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

func TestZshCommandEntriesUseShortestAliasAndFullCommandInRegistryOrder(t *testing.T) {
	entries := strings.Split(zshCommandEntries(), "\n")
	wantPrefix := []string{
		"    'l:List plans'",
		"    'list:List plans'",
		"    'version:Show build commit'",
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

func TestCommandAliasPatternCompletionSpecialCases(t *testing.T) {
	tests := []struct {
		command string
		want    string
	}{
		{command: "list", want: "l|li|lis|list"},
		{command: "completion", want: "co|com|comp|compl|comple|completi|completio|completion"}, //nolint:misspell // intentional completion-prefix fixture
		{command: "repo", want: "repo"},
		{command: "note", want: "n|no|not|note"},
		{command: "slice-complete", want: "slice-complete"},
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
				"'--active[with --all, enqueue only active runnable plans]'",
				"'--all[enqueue and drain all runnable plans]'",
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
			for _, name := range subcommand.completionNames {
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

func TestZshCompletionScriptCoversQueueSubcommands(t *testing.T) {
	script := buildZshCompletionScript()
	queueStart := strings.Index(script, commandAliasPattern("queue")+")")
	repoStart := strings.Index(script, commandAliasPattern("repo")+")")
	if queueStart < 0 || repoStart <= queueStart {
		t.Fatalf("expected queue completion case before repo case, got queue=%d repo=%d", queueStart, repoStart)
	}
	queueCase := script[queueStart:repoStart]
	for _, want := range []string{
		"'add:Add one or more plans to the durable run queue'",
		"'start:Drain the queue and run queued plans'",
		"'status:Show the durable run queue snapshot'",
		"'stop:Remove a plan from the queue'",
		"'dequeue:Remove a plan from the queue'",
		"case ${words[3]} in",
		"if (( CURRENT >= 4 ))",
		"complete run-plan-ids",
		"--max-parallel[maximum concurrent plan runs",
		"--auto-rework=-[automatically rework plans with requested changes]:boolean:(true false)",
		"--max-rework-attempts[maximum automatic rework cycles (0 disables)]:count:",
		"--all[show complete persisted queue history]",
		"stop|dequeue)",
		"if (( CURRENT == 4 ))",
		"complete plan-ids",
	} {
		if !strings.Contains(queueCase, want) {
			t.Errorf("queue completion case missing %q", want)
		}
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
		{
			name:        "queue start",
			command:     "queue",
			subcommands: []string{"start"},
			register:    registerQueueStartFlags,
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
	script := buildZshCompletionScript()
	mergeStart := strings.Index(script, commandAliasPattern("merge")+")")
	workspaceStart := strings.Index(script, commandAliasPattern("workspace")+")")
	if mergeStart < 0 || workspaceStart <= mergeStart {
		t.Fatalf("expected merge completion case before workspace case, got merge=%d workspace=%d", mergeStart, workspaceStart)
	}
	mergeCase := script[mergeStart:workspaceStart]
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
	for _, want := range []string{"repo:Inspect registered repositories", "compadd list show doctor", "note:Capture and maintain repository notes", "compadd c create l list s show e edit a archive reopen p plan r run", "complete note-ids", "approve:Approve a gated slice", "stale:Check whether a plan is stale against its recorded base commit", "cleanup:Remove merged Tao branches and worktrees", "delete:Delete local plan artifacts", "edit:Edit pending slices in a plan", "validate:Validate plan artifacts and verification commands", "--slice[slice id to approve]", "--by[approver name]", "--commit-policy[automatic commit policy: slice or none]:policy:(slice none)", "--commit-policy[run prompt commit policy: slice or none]:policy:(slice none)", "--execution-mode[execution mode: isolated or current]", "--continue[continue a blocked plan or slice]", "--no-review[disable automatic plan review for this run]", "--run[run a fresh review before displaying the result]", "--dry-run[show branches and worktrees that would be removed]", "--force[also remove unmerged branches and dirty worktrees]", "--force[confirm deletion of local plan artifacts]", "--before[move before slice id]", "--after[move after slice id]"} {
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
