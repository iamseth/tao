package workspace

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/iamseth/tao/internal/configtypes"
	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/internal/runtimeconfig"
)

const (
	StrategyWorktree = string(configtypes.ExecutionModeIsolated)
	StrategyCurrent  = string(configtypes.ExecutionModeCurrent)

	planBranchTemplate = "tao/{plan_id}"
)

// Config defines workspace defaults for future run execution.
type Config struct {
	Root     string
	Strategy runtimeconfig.ExecutionMode
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
func ResolvePlanBranch(detail *plan.PlanDetail, _ Config) (PlanBranchIdentity, error) {
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
	changeType := detail.State.Plan.ChangeType
	if changeType == "" {
		return PlanBranchIdentity{Name: strings.ReplaceAll(planBranchTemplate, "{plan_id}", planID)}, nil
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
		Root:     ".tao/workspaces",
		Strategy: runtimeconfig.ExecutionModeIsolated,
	}
}

func recordedStrategy(value string) string {
	mode, err := configtypes.NormalizeRecordedExecutionMode(value)
	if err != nil {
		return value
	}
	return mode.String()
}

// Validate rejects values Tao cannot execute safely.
func (c Config) Validate() error {
	c.Strategy = runtimeconfig.ExecutionMode(recordedStrategy(c.Strategy.String()))
	if c.Root == "" {
		return fmt.Errorf("workspace root is required")
	}
	if c.Strategy != runtimeconfig.ExecutionModeIsolated && c.Strategy != runtimeconfig.ExecutionModeCurrent {
		return fmt.Errorf("workspace strategy must be %q or %q", StrategyWorktree, StrategyCurrent)
	}
	return nil
}
