package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os/exec"
	"strings"

	agentpkg "github.com/iamseth/tao/internal/agent"
	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/internal/promptinstall"
	"github.com/iamseth/tao/internal/runtimeconfig"
	"github.com/iamseth/tao/prompts"
)

var promptCommand = commandMetadata{
	name:                  "prompt",
	minPrefix:             "p",
	usageLines:            []string{"prompt (p) <prompt> [--plan-dir DIR] [--commit-policy slice|none] [--execution-mode isolated|current] [--arguments TEXT]"},
	completionDescription: "Render a Tao prompt",
	long:                  "Render one of Tao's built-in prompt templates for the selected agent workflow. Run prompts can include plan context, commit policy, execution mode, and slash-command arguments without starting an agent session.",
	examples: "  tao prompt plan --arguments \"add queue metrics\"\n" +
		"  tao prompt run --plan-dir /path/to/plan --execution-mode current\n" +
		"  tao prompt commit --arguments \"include staged docs\"",
	registerFlags: registerPromptFlags,
	completion: completionContext{flagValues: map[string]completionFlagValue{
		"arguments":      {kind: completionValueText, label: "text"},
		"commit":         {kind: completionValueBoolean, label: "boolean"},
		"commit-policy":  {kind: completionValueEnum, label: "policy", values: []string{"slice", "none"}},
		"execution-mode": {kind: completionValueEnum, label: "mode", values: []string{"isolated", "current"}},
		"plan-dir":       {kind: completionValuePath, label: "path"},
	}},
	repository: repositoryDefault,
	execute: func(c commandContext) error {
		return c.app.prompt(c.ctx, c.repo, c.args)
	},
}

var installPromptsCommand = commandMetadata{
	name:                  "install-prompts",
	minPrefix:             "i",
	usageLines:            []string{"install-prompts (i) [--force] [--check]"},
	completionDescription: "Install agent slash-command wrappers",
	long:                  "Install Tao-managed slash-command wrappers for every supported agent found in PATH. Use --check to report whether wrappers are current and --force to replace unmanaged files when installing.",
	examples: "  tao install-prompts\n" +
		"  tao install-prompts --check\n" +
		"  tao install-prompts --force",
	registerFlags: registerInstallPromptsFlags,
	execute: func(c commandContext) error {
		return c.app.installPrompts(c.args)
	},
}

var doctorCommand = commandMetadata{
	name:                  "doctor",
	minPrefix:             "d",
	usageLines:            []string{"doctor (d) [--verbose|-v]"},
	completionDescription: "Check Tao prompt wrappers and local tools",
	long:                  "Check supported agents and report actionable prompt and tool problems. Use --verbose or -v for the full environment report.",
	examples: "  tao doctor\n" +
		"  tao doctor --verbose\n" +
		"  tao doctor -v",
	registerFlags: registerDoctorFlags,
	execute: func(c commandContext) error {
		return c.app.doctor(c.args)
	},
}

func registerPromptFlags(fs *flag.FlagSet) {
	registerPromptFlagsWithDefaults(fs, runtimeFlagDefaults().RunOptionsPatch)
}

func (d envDefaults) registerPromptFlags(fs *flag.FlagSet) {
	registerPromptFlagsWithDefaults(fs, d.RunOptionsPatch)
}

func registerPromptFlagsWithDefaults(fs *flag.FlagSet, defaults runtimeconfig.RunOptionsPatch) {
	fs.String("plan-dir", "", "plan directory for run prompts")
	fs.Bool("commit", true, "include commit instructions in run prompts")
	fs.String("commit-policy", defaults.CommitPolicy.String(), "run prompt commit policy: slice or none")
	fs.String("execution-mode", defaults.ExecutionModeValue().String(), "run prompt execution mode: isolated or current")
	fs.String("arguments", "", "slash-command arguments to include in the prompt")
	fs.Bool("arguments-stdin", false, "read slash-command arguments from stdin (safe for text containing quotes)")
}

func registerInstallPromptsFlags(fs *flag.FlagSet) {
	fs.Bool("force", false, "overwrite existing non-tao-managed command files")
	fs.Bool("check", false, "check installed wrappers without writing")
}

func registerDoctorFlags(fs *flag.FlagSet) {
	fs.Bool("verbose", false, "show the full environment report")
	fs.Bool("v", false, "show the full environment report")
}

func (a App) prompt(ctx context.Context, repo plan.Resolver, args []string) error {
	defaults, err := cliEnvDefaults()
	if err != nil {
		return err
	}
	fs, positional, err := a.parseArgs("prompt", args, defaults.registerPromptFlags)
	if err != nil {
		return err
	}
	planDir := flagStringValue(fs, "plan-dir")
	commit := flagBoolValue(fs, "commit")
	arguments := flagStringValue(fs, "arguments")
	if flagBoolValue(fs, "arguments-stdin") {
		raw, err := io.ReadAll(a.input())
		if err != nil {
			return fmt.Errorf("read prompt arguments from stdin: %w", err)
		}
		arguments = strings.TrimSuffix(string(raw), "\n")
	}
	if err := requirePositionals(positional, 1, "usage: tao prompt <prompt> [--plan-dir DIR] [--commit-policy slice|none] [--execution-mode isolated|current] [--commit=false] [--arguments TEXT]"); err != nil {
		return err
	}
	config, err := runtimeconfig.NewConfigFromStages(runtimeconfig.DefaultRunOptionsPatch(), runtimeconfig.RunOptionsPatch{CommitPolicy: runtimeconfig.CommitPolicy(flagStringValue(fs, "commit-policy")), ExecutionMode: runtimeconfig.ExecutionMode(flagStringValue(fs, "execution-mode"))})
	if err != nil {
		return err
	}
	resolved := config.ResolvedOptions()
	policy := resolved.CommitPolicy
	if !commit {
		policy = runtimeconfig.CommitPolicyNone
	}
	runPacket := ""
	if positional[0] == prompts.PromptRun && planDir != "" {
		if detail, err := repo.ResolvePlan(ctx, planDir); err == nil {
			runPacket, err = plan.RenderRunPacket(detail, plan.RunPacketOptions{CommitPolicy: policy.String(), ExecutionMode: resolved.ExecutionMode.String(), WorkingRoot: ""})
			if err != nil {
				return err
			}
		}
	}
	text, err := prompts.Render(positional[0], prompts.Data{PlanDir: planDir, RunPacket: runPacket, CommitEnable: commit, CommitPolicy: policy.String(), ExecutionMode: resolved.ExecutionMode.String(), Arguments: arguments})
	if err != nil {
		return err
	}
	_, err = fmt.Fprint(a.Out, text)
	return err
}

func (a App) installPrompts(args []string) error {
	defaults, err := cliEnvDefaults()
	if err != nil {
		return err
	}
	fs, positional, err := a.parseArgs("install-prompts", args, registerInstallPromptsFlags)
	if err != nil {
		return err
	}
	if err := requirePositionals(positional, 0, "usage: tao install-prompts [--force] [--check]"); err != nil {
		return err
	}
	_ = defaults // Prompt management intentionally does not use the selected runtime.
	installed := agentpkg.Installed()
	if len(installed) == 0 {
		return writeln(a.Out, "no supported agents found in PATH; no prompts installed or checked")
	}
	check := flagBoolValue(fs, "check")
	results, err := promptInstallResults(installed, check, flagBoolValue(fs, "force"))
	if err != nil {
		return err
	}
	for _, result := range results {
		if check {
			if err := writef(a.Out, "[%s] %s %s\n", result.Agent, result.Status, result.Path); err != nil {
				return err
			}
		} else if err := writef(a.Out, "[%s] installed %s\n", result.Agent, result.Path); err != nil {
			return err
		}
	}
	return nil
}

func (a App) doctor(args []string) error {
	fs, positional, err := a.parseArgs("doctor", args, registerDoctorFlags)
	if err != nil {
		return err
	}
	if err := requirePositionals(positional, 0, "usage: tao doctor [--verbose|-v]"); err != nil {
		return err
	}
	report, err := collectDoctorReport()
	if err != nil {
		return err
	}
	if flagBoolValue(fs, "verbose") || flagBoolValue(fs, "v") {
		return renderVerboseDoctor(a.Out, report)
	}
	return renderCompactDoctor(a.Out, report)
}

type doctorReport struct {
	selectedAgent runtimeconfig.AgentKind
	agents        []agentpkg.Descriptor
	prompts       []promptinstall.Result
	tools         []doctorToolResultCategory
}

type doctorToolResultCategory struct {
	name    string
	results []doctorToolResult
}

type doctorToolResult struct {
	tool   doctorTool
	status string
	found  string
}

func collectDoctorReport() (doctorReport, error) {
	defaults, err := cliEnvDefaults()
	if err != nil {
		return doctorReport{}, err
	}
	report := doctorReport{selectedAgent: defaults.Agent, agents: agentpkg.Installed()}
	report.prompts, err = promptinstall.CheckDiscovered(report.agents)
	if err != nil {
		return doctorReport{}, err
	}

	required := doctorToolResultCategory{name: "required"}
	for _, descriptor := range report.agents {
		required.results = append(required.results, collectDoctorTool(doctorAgentTool(descriptor.Kind)))
	}
	report.tools = append(report.tools, required)
	for _, category := range doctorSharedToolCategories() {
		collected := doctorToolResultCategory{name: category.name}
		for _, tool := range category.tools {
			collected.results = append(collected.results, collectDoctorTool(tool))
		}
		report.tools = append(report.tools, collected)
	}
	return report, nil
}

func collectDoctorTool(tool doctorTool) doctorToolResult {
	status, found := doctorToolStatus(tool)
	return doctorToolResult{tool: tool, status: status, found: found}
}

func renderCompactDoctor(out io.Writer, report doctorReport) error {
	agents := make([]string, 0, len(report.agents))
	for _, descriptor := range report.agents {
		agents = append(agents, descriptor.Label)
	}
	if len(agents) == 0 {
		agents = append(agents, "none")
	}
	if err := writef(out, "agents: %s\n", strings.Join(agents, ", ")); err != nil {
		return err
	}
	for _, descriptor := range report.agents {
		var problems []promptinstall.Result
		for _, result := range promptResultsForAgent(report.prompts, descriptor.Kind) {
			if result.Status != "current" {
				problems = append(problems, result)
			}
		}
		if len(problems) == 0 {
			continue
		}
		if err := writef(out, "\nprompts (%s):\n", descriptor.Label); err != nil {
			return err
		}
		nameWidth := doctorPromptNameWidth(problems)
		for _, result := range problems {
			if err := writef(out, "  %-*s %s %s\n", nameWidth, result.Name, doctorStatusLabel(result.Status, 11), result.Path); err != nil {
				return err
			}
		}
	}
	for _, category := range report.tools {
		var missing []doctorToolResult
		for _, result := range category.results {
			if result.status != "ok" {
				missing = append(missing, result)
			}
		}
		if len(missing) == 0 {
			continue
		}
		if err := writef(out, "\ntools %s:\n", category.name); err != nil {
			return err
		}
		for _, result := range missing {
			if err := writeDoctorToolResult(out, result); err != nil {
				return err
			}
		}
	}
	return nil
}

func renderVerboseDoctor(out io.Writer, report doctorReport) error {
	if err := writef(out, "selected runtime agent: %s\n", report.selectedAgent); err != nil {
		return err
	}
	if len(report.agents) == 0 {
		if err := writeln(out, "supported agents: none found in PATH"); err != nil {
			return err
		}
	}
	for _, descriptor := range report.agents {
		if err := writef(out, "\nprompts (%s):\n", descriptor.Label); err != nil {
			return err
		}
		if err := writef(out, "  %s\n", descriptor.DoctorDescription); err != nil {
			return err
		}
		agentResults := promptResultsForAgent(report.prompts, descriptor.Kind)
		nameWidth := doctorPromptNameWidth(agentResults)
		for _, result := range agentResults {
			if err := writef(out, "  %-*s %s %s\n", nameWidth, result.Name, doctorStatusLabel(result.Status, 11), result.Path); err != nil {
				return err
			}
		}
	}
	for _, category := range report.tools {
		if err := writef(out, "\ntools %s:\n", category.name); err != nil {
			return err
		}
		if category.name == "required" && len(report.agents) == 0 {
			if err := writeln(out, "  no supported agent executables found"); err != nil {
				return err
			}
		}
		for _, result := range category.results {
			if err := writeDoctorToolResult(out, result); err != nil {
				return err
			}
		}
	}
	return nil
}

func doctorPromptNameWidth(results []promptinstall.Result) int {
	width := 0
	for _, result := range results {
		if len(result.Name) > width {
			width = len(result.Name)
		}
	}
	return width
}

func doctorStatusLabel(status string, width int) string {
	label := pad(doctorStatusSymbol(status)+" "+status, width)
	if status == "current" || status == "ok" {
		return colorGreen(label)
	}
	return label
}

func doctorStatusSymbol(status string) string {
	switch status {
	case "current", "ok":
		return "✓"
	case "warning", "missing", "outdated":
		return "⚠"
	default:
		return "•"
	}
}

type doctorToolCategory struct {
	name  string
	tools []doctorTool
}

type doctorTool struct {
	name        string
	executables []string
}

func doctorSharedToolCategories() []doctorToolCategory {
	return []doctorToolCategory{
		{name: "dev", tools: []doctorTool{
			{name: "git", executables: []string{"git"}},
			{name: "go", executables: []string{"go"}},
			{name: "make", executables: []string{"make"}},
		}},
		{name: "recommended", tools: []doctorTool{
			{name: "rg", executables: []string{"rg"}},
			{name: "fd", executables: []string{"fd", "fdfind"}},
			{name: "ast-grep", executables: []string{"ast-grep"}},
			{name: "jq", executables: []string{"jq"}},
			{name: "sqlite3", executables: []string{"sqlite3"}},
			{name: "curl", executables: []string{"curl"}},
			{name: "AWS CLI (aws)", executables: []string{"aws"}},
			{name: "gh", executables: []string{"gh"}},
			{name: "delta", executables: []string{"delta"}},
			{name: "bat", executables: []string{"bat"}},
		}},
	}
}

func promptResultsForAgent(results []promptinstall.Result, agent runtimeconfig.AgentKind) []promptinstall.Result {
	var matched []promptinstall.Result
	for _, result := range results {
		if result.Agent == agent {
			matched = append(matched, result)
		}
	}
	return matched
}

func writeDoctorToolResult(out io.Writer, result doctorToolResult) error {
	found := result.found
	if found != "" {
		found = " (" + found + ")"
	}
	return writef(out, "  %s %s%s\n", doctorStatusLabel(result.status, 9), result.tool.name, found)
}

func doctorAgentTool(agent runtimeconfig.AgentKind) doctorTool {
	descriptor, ok := agentpkg.Lookup(agent)
	if !ok {
		return doctorTool{name: string(agent), executables: nil}
	}
	return doctorTool{name: descriptor.ToolName, executables: []string{descriptor.ToolName}}
}

func doctorToolStatus(tool doctorTool) (string, string) {
	for _, executable := range tool.executables {
		if _, err := exec.LookPath(executable); err == nil {
			return "ok", executable
		}
	}
	return "warning", "missing"
}

type PromptFreshnessChecker func() ([]promptinstall.Result, error)

var defaultPromptFreshnessCheck PromptFreshnessChecker = installedPromptFreshness

func (a App) warnIfStalePrompts() {
	check := a.PromptFreshnessCheck
	if check == nil {
		check = defaultPromptFreshnessCheck
	}
	results, err := check()
	if err != nil {
		return
	}
	var stale []string
	seen := make(map[runtimeconfig.AgentKind]bool)
	for _, result := range results {
		if result.Status != "stale" || seen[result.Agent] {
			continue
		}
		seen[result.Agent] = true
		stale = append(stale, string(result.Agent))
	}
	if len(stale) == 0 || a.Err == nil {
		return
	}
	_, _ = fmt.Fprintf(a.Err, "warning: stale Tao-managed prompts for %s; run tao install-prompts\n", strings.Join(stale, ", "))
}

func installedPromptFreshness() ([]promptinstall.Result, error) {
	var results []promptinstall.Result
	for _, descriptor := range agentpkg.Installed() {
		checked, err := promptinstall.CheckDiscovered([]agentpkg.Descriptor{descriptor})
		if err != nil {
			continue
		}
		results = append(results, checked...)
	}
	return results, nil
}

func promptInstallResults(installed []agentpkg.Descriptor, check bool, force bool) ([]promptinstall.Result, error) {
	if check {
		return promptinstall.CheckDiscovered(installed)
	}
	return promptinstall.InstallDiscovered(installed, force)
}
