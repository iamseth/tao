package workspace

import (
	"strings"
	"testing"
)

func TestDefaultConfigValues(t *testing.T) {
	config := DefaultConfig()

	if config.Root != ".tao/workspaces" {
		t.Fatalf("unexpected root: %q", config.Root)
	}
	if config.Strategy != StrategyWorktree {
		t.Fatalf("unexpected strategy: %q", config.Strategy)
	}
	if config.MaxParallelRuns != 1 {
		t.Fatalf("unexpected max parallel runs: %d", config.MaxParallelRuns)
	}
	if config.MaxParallelDependencyInstalls != 1 {
		t.Fatalf("unexpected max parallel dependency installs: %d", config.MaxParallelDependencyInstalls)
	}
	if config.DependencyInstallBehavior != DependencyInstallAutoIfLockfilePresent {
		t.Fatalf("unexpected dependency install behavior: %q", config.DependencyInstallBehavior)
	}
	if config.BaseBranchDetection != BaseBranchDetectAuto {
		t.Fatalf("unexpected base branch detection: %q", config.BaseBranchDetection)
	}
	if config.BranchNameTemplate != "tao/{plan_id}" {
		t.Fatalf("unexpected branch name template: %q", config.BranchNameTemplate)
	}
	if config.CleanupBehavior != CleanupManual {
		t.Fatalf("unexpected cleanup behavior: %q", config.CleanupBehavior)
	}
	if err := config.Validate(); err != nil {
		t.Fatalf("default config should validate: %v", err)
	}
}

func TestConfigAllowsCurrentCompatibilityStrategy(t *testing.T) {
	config := DefaultConfig()
	config.Strategy = StrategyCurrent

	if err := config.Validate(); err != nil {
		t.Fatalf("current strategy should validate: %v", err)
	}
}

func TestConfigRejectsInvalidValues(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Config)
		want   string
	}{
		{name: "root", mutate: func(c *Config) { c.Root = "" }, want: "root"},
		{name: "strategy", mutate: func(c *Config) { c.Strategy = "shared" }, want: "strategy"},
		{name: "max runs", mutate: func(c *Config) { c.MaxParallelRuns = 0 }, want: "max parallel runs"},
		{name: "max dependency installs", mutate: func(c *Config) { c.MaxParallelDependencyInstalls = 0 }, want: "dependency installs"},
		{name: "dependency behavior", mutate: func(c *Config) { c.DependencyInstallBehavior = "sometimes" }, want: "dependency install behavior"},
		{name: "dependency command", mutate: func(c *Config) { c.DependencyInstallBehavior = DependencyInstallCommand }, want: "dependency install command"},
		{name: "base branch detection", mutate: func(c *Config) { c.BaseBranchDetection = "guess" }, want: "base branch detection"},
		{name: "branch template", mutate: func(c *Config) { c.BranchNameTemplate = "" }, want: "branch name template"},
		{name: "cleanup behavior", mutate: func(c *Config) { c.CleanupBehavior = "delete" }, want: "cleanup behavior"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := DefaultConfig()
			tt.mutate(&config)
			err := config.Validate()
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("expected error containing %q, got %v", tt.want, err)
			}
		})
	}
}
