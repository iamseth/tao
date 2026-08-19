package agent

import (
	"os/exec"

	piagent "github.com/iamseth/tao/internal/agent/pi"
	processagent "github.com/iamseth/tao/internal/agent/process"
	"github.com/iamseth/tao/internal/agent/promptfmt"
	"github.com/iamseth/tao/internal/runtimeconfig"
)

// RuntimeDeps carries the transport dependencies a Descriptor needs to build a
// Runtime. The starter receives the executable name, so one shared starter can
// launch every registered runtime.
type ProcessStarter = processagent.ProcessStarter

type Process = processagent.Process

var DefaultProcessStarter ProcessStarter = processagent.DefaultProcessStarter

const DefaultPiNoProgressToolLimit = piagent.DefaultNoProgressToolLimit

type RuntimeDeps struct {
	ProcessStarter ProcessStarter
}

// Descriptor centralizes the per-runtime knowledge that run, prompt-install,
// and CLI doctor use through registry lookups so each runtime is a single
// descriptor entry plus adapter.
type Descriptor struct {
	// Kind is the runtimeconfig.AgentKind this descriptor describes.
	Kind runtimeconfig.AgentKind
	// Label is the short agent name recorded in telemetry and run audits.
	Label string
	// ToolName is the CLI executable the doctor command probes for.
	ToolName string
	// TargetDescription describes the prompt-install target.
	TargetDescription string
	// DoctorDescription describes the prompt-install target in doctor output,
	// matching the CLI doctor prompt description.
	DoctorDescription string
	// UsesExtensionPrompts reports whether this runtime deploys extension prompts
	// via the special-cased extension symlink (Pi) rather than a managed prompt
	// file. It lets prompt-install branch on data instead of agent type.
	UsesExtensionPrompts bool
	// PromptDir resolves the runtime's managed prompt-install directory.
	PromptDir func() (string, error)
	// RenderPrompt renders one managed prompt-install file for this runtime.
	// commandName is the provider-facing name; promptName is Tao's stable
	// logical selector.
	RenderPrompt func(commandName, promptName, template string) (string, error)
	// SupportsBypassPermissions reports whether this runtime honors Tao's
	// --dangerously-skip-permissions request by switching to bypass mode.
	// Runtimes that always run with Tao-managed auto permissions (Pi) leave this
	// false, so the unified executor treats the skip request as a no-op for them.
	SupportsBypassPermissions bool
	// DefaultNoProgressToolLimit is the runtime's watchdog limit. Zero means the
	// runtime does not support Tao's no-progress watchdog.
	DefaultNoProgressToolLimit int
	// AlwaysCollectMetrics requests best-effort metric collection even when the
	// caller is not appending a metrics event.
	AlwaysCollectMetrics bool
	// MetricsWarningPrefix annotates telemetry warnings emitted by the runtime.
	MetricsWarningPrefix string
	// MetricsWarningInformational keeps metrics events enabled when warnings are
	// advisory rather than evidence that metrics are absent.
	MetricsWarningInformational bool
	// MetricsMessage is the event message for durable agent_metrics telemetry.
	MetricsMessage string
	// NewRuntime builds the neutral Runtime for this descriptor from the supplied
	// transport dependencies.
	NewRuntime func(RuntimeDeps) Runtime
}

// descriptors is the ordered source of truth for the registry. The slice order
// determines All()'s deterministic iteration order; lookupByKind indexes it.
var descriptors = []Descriptor{
	{
		Kind:                        runtimeconfig.AgentPi,
		Label:                       "pi",
		ToolName:                    "pi",
		TargetDescription:           "Pi global prompt templates and Tao /tao-commit extension command",
		DoctorDescription:           "Pi prompt templates plus Tao /tao-commit extension command",
		UsesExtensionPrompts:        true,
		PromptDir:                   promptfmt.PiDir,
		RenderPrompt:                promptfmt.ManagedPiTemplate,
		DefaultNoProgressToolLimit:  DefaultPiNoProgressToolLimit,
		AlwaysCollectMetrics:        true,
		MetricsWarningPrefix:        "collect pi session info: ",
		MetricsWarningInformational: true,
		MetricsMessage:              "Captured Pi agent metrics",
		NewRuntime: func(deps RuntimeDeps) Runtime {
			return piRuntime{starter: deps.ProcessStarter}
		},
	},
	{
		Kind:                      runtimeconfig.AgentClaude,
		Label:                     "claude",
		ToolName:                  "claude",
		TargetDescription:         "Claude Markdown slash commands",
		DoctorDescription:         "Claude Markdown slash commands that render tao prompts dynamically",
		UsesExtensionPrompts:      false,
		PromptDir:                 promptfmt.ClaudeDir,
		RenderPrompt:              promptfmt.ManagedClaudeCommand,
		SupportsBypassPermissions: true,
		MetricsMessage:            "Captured Claude agent metrics",
		NewRuntime: func(deps RuntimeDeps) Runtime {
			return claudeRuntime{starter: deps.ProcessStarter}
		},
	},
	{
		Kind:                      runtimeconfig.AgentOpenCode,
		Label:                     "opencode",
		ToolName:                  "opencode",
		TargetDescription:         "OpenCode Markdown commands",
		DoctorDescription:         "OpenCode Markdown commands that render tao prompts dynamically",
		UsesExtensionPrompts:      false,
		PromptDir:                 promptfmt.OpenCodeDir,
		RenderPrompt:              promptfmt.ManagedOpenCodeCommand,
		SupportsBypassPermissions: true,
		MetricsMessage:            "Captured OpenCode agent metrics",
		NewRuntime: func(deps RuntimeDeps) Runtime {
			return openCodeRuntime{starter: deps.ProcessStarter}
		},
	},
	{
		Kind:                      runtimeconfig.AgentCodex,
		Label:                     "codex",
		ToolName:                  "codex",
		TargetDescription:         "Codex Markdown commands",
		DoctorDescription:         "Codex Markdown commands that render tao prompts dynamically",
		UsesExtensionPrompts:      false,
		PromptDir:                 promptfmt.CodexDir,
		RenderPrompt:              promptfmt.ManagedCodexCommand,
		SupportsBypassPermissions: true,
		MetricsMessage:            "Captured Codex agent metrics",
		NewRuntime: func(deps RuntimeDeps) Runtime {
			return codexRuntime{starter: deps.ProcessStarter}
		},
	},
}

// lookupByKind indexes descriptors by their AgentKind for O(1) lookup.
var lookupByKind = func() map[runtimeconfig.AgentKind]Descriptor {
	m := make(map[runtimeconfig.AgentKind]Descriptor, len(descriptors))
	for _, d := range descriptors {
		m[d.Kind] = d
	}
	return m
}()

// Lookup returns the Descriptor for kind. An empty kind resolves to Pi, matching
// the run factory and planning runtime defaults. The returned Descriptor is
// always usable: unknown kinds fall back to the Pi descriptor while reporting
// ok=false, so dispatch sites get a safe default and validation sites can still
// reject the kind.
func Lookup(kind runtimeconfig.AgentKind) (Descriptor, bool) {
	if kind == "" {
		kind = runtimeconfig.AgentPi
	}
	if d, ok := lookupByKind[kind]; ok {
		return d, true
	}
	return lookupByKind[runtimeconfig.AgentPi], false
}

// All returns the registered descriptors in deterministic order (Pi, Claude,
// OpenCode, then Codex).
func All() []Descriptor {
	out := make([]Descriptor, len(descriptors))
	copy(out, descriptors)
	return out
}

// LookPath resolves an executable by name. It is accepted by
// DiscoverInstalled so callers and tests can control executable discovery.
type LookPath func(string) (string, error)

// Installed returns the registered agents whose executables are available in
// PATH, preserving registry order.
func Installed() []Descriptor {
	return DiscoverInstalled(exec.LookPath)
}

// DiscoverInstalled applies lookPath to every registered descriptor. Lookup
// failures mean that agent is unavailable and do not prevent discovery of the
// remaining agents.
func DiscoverInstalled(lookPath LookPath) []Descriptor {
	installed := make([]Descriptor, 0, len(descriptors))
	for _, descriptor := range descriptors {
		if _, err := lookPath(descriptor.ToolName); err == nil {
			installed = append(installed, descriptor)
		}
	}
	return installed
}
