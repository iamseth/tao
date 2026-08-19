package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/iamseth/tao/internal/runtimeconfig"
)

func TestLookupResolvesKnownAndDefaultKinds(t *testing.T) {
	for _, tc := range []struct {
		name     string
		kind     runtimeconfig.AgentKind
		wantKind runtimeconfig.AgentKind
		wantOK   bool
	}{
		{name: "pi", kind: runtimeconfig.AgentPi, wantKind: runtimeconfig.AgentPi, wantOK: true},
		{name: "claude", kind: runtimeconfig.AgentClaude, wantKind: runtimeconfig.AgentClaude, wantOK: true},
		{name: "empty defaults to pi", kind: "", wantKind: runtimeconfig.AgentPi, wantOK: true},
		{name: "unknown falls back to pi", kind: "gemini", wantKind: runtimeconfig.AgentPi, wantOK: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := Lookup(tc.kind)
			if ok != tc.wantOK {
				t.Fatalf("Lookup(%q) ok = %v, want %v", tc.kind, ok, tc.wantOK)
			}
			if got.Kind != tc.wantKind {
				t.Fatalf("Lookup(%q) kind = %q, want %q", tc.kind, got.Kind, tc.wantKind)
			}
		})
	}
}

func TestLookupDescriptorFields(t *testing.T) {
	pi, ok := Lookup(runtimeconfig.AgentPi)
	if !ok {
		t.Fatal("expected pi descriptor to be registered")
	}
	if pi.Label != "pi" || pi.ToolName != "pi" {
		t.Fatalf("pi label/tool = %q/%q, want pi/pi", pi.Label, pi.ToolName)
	}
	if pi.TargetDescription != "Pi global prompt templates and Tao /tao-commit extension command" {
		t.Fatalf("pi target description = %q", pi.TargetDescription)
	}
	if pi.DoctorDescription != "Pi prompt templates plus Tao /tao-commit extension command" {
		t.Fatalf("pi doctor description = %q", pi.DoctorDescription)
	}
	if !pi.UsesExtensionPrompts {
		t.Fatal("pi should use extension prompts")
	}

	claude, ok := Lookup(runtimeconfig.AgentClaude)
	if !ok {
		t.Fatal("expected claude descriptor to be registered")
	}
	if claude.Label != "claude" || claude.ToolName != "claude" {
		t.Fatalf("claude label/tool = %q/%q, want claude/claude", claude.Label, claude.ToolName)
	}
	if claude.TargetDescription != "Claude Markdown slash commands" {
		t.Fatalf("claude target description = %q", claude.TargetDescription)
	}
	if claude.DoctorDescription != "Claude Markdown slash commands that render tao prompts dynamically" {
		t.Fatalf("claude doctor description = %q", claude.DoctorDescription)
	}
	if claude.UsesExtensionPrompts {
		t.Fatal("claude should not use extension prompts")
	}
}

func TestAllDescriptorsBuildRuntime(t *testing.T) {
	deps := RuntimeDeps{
		ProcessStarter: func(context.Context, string, string, []string) (Process, error) {
			t.Fatal("NewRuntime must not start a process")
			return nil, nil
		},
	}

	for _, descriptor := range All() {
		t.Run(string(descriptor.Kind), func(t *testing.T) {
			if descriptor.NewRuntime == nil {
				t.Fatal("NewRuntime is nil")
			}
			if runtime := descriptor.NewRuntime(deps); runtime == nil {
				t.Fatal("NewRuntime returned nil")
			}
		})
	}
}

func TestDescriptorNewRuntimeBuildsMatchingRuntime(t *testing.T) {
	// The starters are unused by the type assertion, so a zero RuntimeDeps is
	// enough to confirm each descriptor builds its own runtime adapter.
	var deps RuntimeDeps

	pi, _ := Lookup(runtimeconfig.AgentPi)
	if _, ok := pi.NewRuntime(deps).(piRuntime); !ok {
		t.Fatalf("pi NewRuntime = %T, want piRuntime", pi.NewRuntime(deps))
	}

	claude, _ := Lookup(runtimeconfig.AgentClaude)
	if _, ok := claude.NewRuntime(deps).(claudeRuntime); !ok {
		t.Fatalf("claude NewRuntime = %T, want claudeRuntime", claude.NewRuntime(deps))
	}
}

func TestAllKindsMatchRuntimeConfigRoster(t *testing.T) {
	all := All()
	if len(all) != len(runtimeconfig.AgentKinds) {
		t.Fatalf("All() len = %d, want runtimeconfig.AgentKinds len %d", len(all), len(runtimeconfig.AgentKinds))
	}
	for i, descriptor := range all {
		if descriptor.Kind != runtimeconfig.AgentKinds[i] {
			t.Fatalf("All()[%d].Kind = %q, want runtimeconfig.AgentKinds[%d] %q", i, descriptor.Kind, i, runtimeconfig.AgentKinds[i])
		}
	}
}

func TestDiscoverInstalledUsesRegistryOrderAndIgnoresLookupFailures(t *testing.T) {
	available := map[string]bool{"claude": true}
	var lookedUp []string
	installed := DiscoverInstalled(func(name string) (string, error) {
		lookedUp = append(lookedUp, name)
		if available[name] {
			return "/bin/" + name, nil
		}
		return "", fmt.Errorf("lookup %s: %w", name, errors.ErrUnsupported)
	})

	if got := descriptorKinds(installed); got != "claude" {
		t.Fatalf("installed kinds = %q, want claude", got)
	}
	if got := fmt.Sprint(lookedUp); got != "[pi claude]" {
		t.Fatalf("lookup order = %s, want registry order", got)
	}
}

func TestDiscoverInstalledReturnsEmptyWhenNoExecutablesResolve(t *testing.T) {
	installed := DiscoverInstalled(func(string) (string, error) {
		return "", errors.ErrUnsupported
	})
	if len(installed) != 0 {
		t.Fatalf("installed = %v, want none", descriptorKinds(installed))
	}
}

func descriptorKinds(descriptors []Descriptor) string {
	kinds := make([]string, 0, len(descriptors))
	for _, descriptor := range descriptors {
		kinds = append(kinds, string(descriptor.Kind))
	}
	return strings.Join(kinds, ",")
}

func TestAllReturnsDeterministicOrder(t *testing.T) {
	all := All()
	if len(all) != 2 {
		t.Fatalf("All() len = %d, want 2", len(all))
	}
	if all[0].Kind != runtimeconfig.AgentPi || all[1].Kind != runtimeconfig.AgentClaude {
		t.Fatalf("All() order = %q, %q, want pi, claude", all[0].Kind, all[1].Kind)
	}

	// All() must return a copy: mutating the result must not affect the registry.
	all[0].Label = "mutated"
	if fresh := All(); fresh[0].Label != "pi" {
		t.Fatalf("All() leaked mutation: label = %q", fresh[0].Label)
	}
}
