package workspace

import (
	"fmt"
	"slices"
)

const (
	StrategyWorktree = "worktree"
	StrategyCurrent  = "current"

	DependencyInstallAuto                  = "auto"
	DependencyInstallAutoIfLockfilePresent = "auto-if-lockfile-present"
	DependencyInstallAlways                = "always"
	DependencyInstallNever                 = "never"
	DependencyInstallCommand               = "command"

	BaseBranchDetectAuto   = "auto"
	BaseBranchDetectManual = "manual"

	CleanupManual = "manual"
	CleanupAuto   = "auto"
)

// Config defines workspace defaults for future run execution.
type Config struct {
	Root                          string
	Strategy                      string
	MaxParallelRuns               int
	MaxParallelDependencyInstalls int
	DependencyInstallBehavior     string
	DependencyInstallCommand      string
	BaseBranchDetection           string
	BranchNameTemplate            string
	CleanupBehavior               string
}

// DefaultConfig returns Tao's safe workspace defaults.
func DefaultConfig() Config {
	return Config{
		Root:                          ".tao/workspaces",
		Strategy:                      StrategyWorktree,
		MaxParallelRuns:               1,
		MaxParallelDependencyInstalls: 1,
		DependencyInstallBehavior:     DependencyInstallAutoIfLockfilePresent,
		BaseBranchDetection:           BaseBranchDetectAuto,
		BranchNameTemplate:            "tao/{plan_id}",
		CleanupBehavior:               CleanupManual,
	}
}

// Validate rejects values Tao cannot execute safely.
func (c Config) Validate() error {
	if c.Root == "" {
		return fmt.Errorf("workspace root is required")
	}
	if c.Strategy != StrategyWorktree && c.Strategy != StrategyCurrent {
		return fmt.Errorf("workspace strategy must be %q or %q", StrategyWorktree, StrategyCurrent)
	}
	if c.MaxParallelRuns < 1 {
		return fmt.Errorf("max parallel runs must be at least 1")
	}
	if c.MaxParallelDependencyInstalls < 1 {
		return fmt.Errorf("max parallel dependency installs must be at least 1")
	}
	if !oneOf(c.DependencyInstallBehavior, DependencyInstallAuto, DependencyInstallAutoIfLockfilePresent, DependencyInstallAlways, DependencyInstallNever, DependencyInstallCommand) {
		return fmt.Errorf("dependency install behavior must be %q, %q, %q, %q, or %q", DependencyInstallAuto, DependencyInstallAutoIfLockfilePresent, DependencyInstallAlways, DependencyInstallNever, DependencyInstallCommand)
	}
	if c.DependencyInstallBehavior == DependencyInstallCommand && c.DependencyInstallCommand == "" {
		return fmt.Errorf("dependency install command is required when dependency install behavior is %q", DependencyInstallCommand)
	}
	if !oneOf(c.BaseBranchDetection, BaseBranchDetectAuto, BaseBranchDetectManual) {
		return fmt.Errorf("base branch detection must be %q or %q", BaseBranchDetectAuto, BaseBranchDetectManual)
	}
	if c.BranchNameTemplate == "" {
		return fmt.Errorf("branch name template is required")
	}
	if !oneOf(c.CleanupBehavior, CleanupManual, CleanupAuto) {
		return fmt.Errorf("cleanup behavior must be %q or %q", CleanupManual, CleanupAuto)
	}
	return nil
}

func oneOf(value string, allowed ...string) bool {
	return slices.Contains(allowed, value)
}
