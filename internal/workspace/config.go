package workspace

import (
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/iamseth/tao/internal/plan"
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

// PlanBranchIdentity is the branch Tao expects for a plan. RequireNew is set
// only for a new typed plan that has not durably recorded branch ownership.
type PlanBranchIdentity struct {
	Name       string
	RequireNew bool
}

var safePlanBranchSlug = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

// ResolvePlanBranch preserves any durably recorded branch, keeps the legacy
// tao/<plan-id> convention for untyped plans, and derives repository-native
// branches for new typed plans.
func ResolvePlanBranch(detail *plan.PlanDetail, config Config) (PlanBranchIdentity, error) {
	if detail == nil {
		return PlanBranchIdentity{}, fmt.Errorf("plan detail is nil")
	}
	planID, err := requirePlanID(detail.State.Plan.ID)
	if err != nil {
		return PlanBranchIdentity{}, err
	}
	if detail.State.Workspace != nil {
		if recorded := strings.TrimSpace(detail.State.Workspace.Branch); recorded != "" {
			return PlanBranchIdentity{Name: recorded}, nil
		}
	}
	if config == (Config{}) {
		config = DefaultConfig()
	}
	changeType := detail.State.Plan.ChangeType
	if changeType == "" {
		return PlanBranchIdentity{Name: strings.ReplaceAll(config.BranchNameTemplate, "{plan_id}", planID)}, nil
	}
	if err := plan.ValidateChangeType(changeType); err != nil {
		return PlanBranchIdentity{}, err
	}
	slug, ok := plan.PlanSlug(planID)
	if !ok || !safePlanBranchSlug.MatchString(slug) {
		return PlanBranchIdentity{}, fmt.Errorf("typed plan id %q must contain a safe non-empty timestamped slug", planID)
	}
	return PlanBranchIdentity{Name: changeType.Category() + "/" + slug, RequireNew: true}, nil
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
