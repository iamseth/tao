package workspace

import (
	"strings"
	"testing"

	"github.com/iamseth/tao/internal/plan"
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

func TestResolvePlanBranchMapsEveryChangeType(t *testing.T) {
	for _, changeType := range plan.SupportedChangeTypes() {
		detail := &plan.PlanDetail{State: plan.State{Plan: plan.PlanState{ID: "20260812-183359-native-pr-format", ChangeType: changeType}}}
		identity, err := ResolvePlanBranch(detail, DefaultConfig())
		if err != nil {
			t.Fatalf("ResolvePlanBranch(%q): %v", changeType, err)
		}
		want := changeType.Category() + "/native-pr-format"
		if identity.Name != want || !identity.RequireNew {
			t.Errorf("ResolvePlanBranch(%q) = %#v, want name %q requiring a new branch", changeType, identity, want)
		}
	}
}

func TestResolvePlanBranchPreservesLegacyAndRecordedBranches(t *testing.T) {
	legacy := &plan.PlanDetail{State: plan.State{Plan: plan.PlanState{ID: "20260812-183359-legacy-plan"}}}
	identity, err := ResolvePlanBranch(legacy, DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	if identity.Name != "tao/20260812-183359-legacy-plan" || identity.RequireNew {
		t.Fatalf("legacy identity = %#v", identity)
	}

	legacy.State.Plan.ChangeType = plan.ChangeTypeFeat
	legacy.State.Workspace = &plan.Workspace{Branch: "tao/original-plan-branch"}
	identity, err = ResolvePlanBranch(legacy, DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	if identity.Name != "tao/original-plan-branch" || identity.RequireNew {
		t.Fatalf("recorded identity = %#v", identity)
	}
}

func TestResolvePlanBranchRejectsTypedPlanWithoutSafeTimestampedSlug(t *testing.T) {
	for _, id := range []string{"plan-a", "20260812-183359-", "20260812-183359-unsafe_slug"} {
		detail := &plan.PlanDetail{State: plan.State{Plan: plan.PlanState{ID: id, ChangeType: plan.ChangeTypeFix}}}
		if _, err := ResolvePlanBranch(detail, DefaultConfig()); err == nil || !strings.Contains(err.Error(), "safe non-empty timestamped slug") {
			t.Errorf("ResolvePlanBranch(%q) error = %v", id, err)
		}
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
