package run

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/internal/workspace"
)

const maxPrerequisitePlans = 128

type PrerequisiteStatus string

const (
	PrerequisiteSatisfied   PrerequisiteStatus = "satisfied"
	PrerequisiteMissing     PrerequisiteStatus = "missing"
	PrerequisiteUnreadable  PrerequisiteStatus = "unreadable"
	PrerequisiteUnmerged    PrerequisiteStatus = "unmerged"
	PrerequisitePullRequest PrerequisiteStatus = "pr_only"
	PrerequisiteNonAncestor PrerequisiteStatus = "non_ancestor"
	PrerequisiteCyclic      PrerequisiteStatus = "cyclic"
)

// PrerequisiteResult is the bounded, deterministic projection of one runtime
// prerequisite gate. Blocking results include the safest useful next command.
type PrerequisiteResult struct {
	PlanID      string
	Status      PrerequisiteStatus
	MergedSHA   string
	Baseline    string
	NextCommand string
	Cycle       []string
}

func (r PrerequisiteResult) blockingError() error {
	var reason string
	switch r.Status {
	case PrerequisiteMissing:
		reason = "is missing from this repository's plan store"
	case PrerequisiteUnreadable:
		reason = "cannot be read safely"
	case PrerequisiteUnmerged:
		reason = "has no current plan_merged evidence"
	case PrerequisitePullRequest:
		reason = "has pull-request completion but no plan_merged evidence"
	case PrerequisiteNonAncestor:
		reason = fmt.Sprintf("was integrated at %s, which is not an ancestor of execution baseline %s", shortRevision(r.MergedSHA), r.Baseline)
	case PrerequisiteCyclic:
		reason = "forms a runtime prerequisite cycle: " + strings.Join(r.Cycle, " -> ")
	default:
		return nil
	}
	message := fmt.Sprintf("runtime prerequisite %s %s", r.PlanID, reason)
	if r.NextCommand != "" {
		message += "; next: " + r.NextCommand
	}
	return cannotStartf("%s", message)
}

// checkRuntimePrerequisites resolves exact same-store plans, recursively
// rejects cycles, and proves every current merge commit is in the selected
// execution baseline. It performs no plan or workspace mutation.
func checkRuntimePrerequisites(ctx context.Context, resolver plan.ExactPlanResolver, detail *plan.PlanDetail, baseline string, runner CommandRunner) ([]PrerequisiteResult, error) {
	if detail == nil || len(detail.State.Plan.RuntimePrerequisites) == 0 {
		return nil, nil
	}
	if resolver == nil {
		result := PrerequisiteResult{PlanID: detail.State.Plan.RuntimePrerequisites[0].PlanID, Status: PrerequisiteUnreadable, NextCommand: "tao show " + detail.State.Plan.RuntimePrerequisites[0].PlanID}
		return []PrerequisiteResult{result}, result.blockingError()
	}
	if runner == nil {
		runner = defaultCommandRunner
	}
	baseline = strings.TrimSpace(baseline)
	if baseline == "" {
		result := PrerequisiteResult{PlanID: detail.State.Plan.RuntimePrerequisites[0].PlanID, Status: PrerequisiteUnreadable, NextCommand: "tao show " + detail.State.Plan.RuntimePrerequisites[0].PlanID}
		return []PrerequisiteResult{result}, cannotStartf("resolve execution baseline for runtime prerequisites")
	}
	results := make([]PrerequisiteResult, 0, len(detail.State.Plan.RuntimePrerequisites))
	visiting := map[string]int{detail.State.Plan.ID: 0}
	path := []string{detail.State.Plan.ID}
	checked := make(map[string]bool)
	resolved := 0

	var walk func([]plan.RuntimePrerequisite) error
	walk = func(prerequisites []plan.RuntimePrerequisite) error {
		for _, prerequisite := range prerequisites {
			id := prerequisite.PlanID
			if at, ok := visiting[id]; ok {
				cycle := append([]string(nil), path[at:]...)
				cycle = append(cycle, id)
				result := PrerequisiteResult{PlanID: id, Status: PrerequisiteCyclic, NextCommand: "tao show " + id, Cycle: cycle}
				results = append(results, result)
				return result.blockingError()
			}
			if checked[id] {
				continue
			}
			resolved++
			if resolved > maxPrerequisitePlans {
				result := PrerequisiteResult{PlanID: id, Status: PrerequisiteUnreadable, NextCommand: "tao show " + id}
				results = append(results, result)
				return result.blockingError()
			}
			dependency, err := resolver.GetPlanExact(ctx, id)
			if err != nil || dependency == nil {
				status := PrerequisiteUnreadable
				if errors.Is(err, plan.ErrNotFound) || (err == nil && dependency == nil) {
					status = PrerequisiteMissing
				}
				result := PrerequisiteResult{PlanID: id, Status: status, NextCommand: "tao show " + id}
				results = append(results, result)
				return result.blockingError()
			}
			visiting[id] = len(path)
			path = append(path, id)
			if err := walk(dependency.State.Plan.RuntimePrerequisites); err != nil {
				return err
			}
			path = path[:len(path)-1]
			delete(visiting, id)

			mergedSHA := currentPlanMergedSHA(dependency.Events)
			if mergedSHA == "" {
				status := PrerequisiteUnmerged
				if plan.PlanIsPullRequestComplete(dependency) {
					status = PrerequisitePullRequest
				}
				next := plan.DeriveNextAction(dependency).Primary.Command
				if next == "" {
					next = "tao show " + id
				}
				result := PrerequisiteResult{PlanID: id, Status: status, NextCommand: next}
				results = append(results, result)
				return result.blockingError()
			}
			ancestor, err := gitClient(runExecution{Dependencies: RunDependencies{CommandRunner: runner}}, detail.State.Repo.Root).IsAncestor(ctx, mergedSHA, baseline)
			if err != nil || !ancestor {
				result := PrerequisiteResult{PlanID: id, Status: PrerequisiteNonAncestor, MergedSHA: mergedSHA, Baseline: baseline, NextCommand: "tao show " + id}
				results = append(results, result)
				return result.blockingError()
			}
			checked[id] = true
			results = append(results, PrerequisiteResult{PlanID: id, Status: PrerequisiteSatisfied, MergedSHA: mergedSHA, Baseline: baseline})
		}
		return nil
	}
	if err := walk(detail.State.Plan.RuntimePrerequisites); err != nil {
		return results, err
	}
	return results, nil
}

func resolvePrerequisiteBaseline(ctx context.Context, detail *plan.PlanDetail, config ExecutionConfig, runner CommandRunner, prepared bool) (string, error) {
	if detail == nil {
		return "", fmt.Errorf("resolve prerequisite baseline: plan detail is nil")
	}
	if runner == nil {
		runner = defaultCommandRunner
	}
	if config.ExecutionMode == ExecutionModeIsolated && detail.State.Workspace != nil && (prepared || detail.State.Plan.CurrentSlice != nil) {
		if head := strings.TrimSpace(detail.State.Workspace.HeadSHA); head != "" {
			return head, nil
		}
	}
	if config.ExecutionMode == ExecutionModeCurrent {
		return resolvePrerequisiteRevision(ctx, detail, runner, "HEAD")
	}
	branch, err := resolvePreparationBaseBranch(ctx, detail, config, runner)
	if err != nil {
		return "", err
	}
	return resolvePrerequisiteRevision(ctx, detail, runner, branch)
}

// resolvePreparationBaseBranch delegates to the workspace manager so
// prerequisite gates and blocked-restart authority select the same branch that
// workspace preparation will use.
func resolvePreparationBaseBranch(ctx context.Context, detail *plan.PlanDetail, config ExecutionConfig, runner CommandRunner) (string, error) {
	if detail == nil {
		return "", fmt.Errorf("resolve prerequisite baseline branch: plan detail is nil")
	}
	recorded := strings.TrimSpace(detail.State.Repo.Branch)
	if detail.State.Workspace != nil && strings.TrimSpace(detail.State.Workspace.BaseBranch) != "" {
		recorded = strings.TrimSpace(detail.State.Workspace.BaseBranch)
	}
	manager, err := workspace.NewManager(workspace.Options{RepoRoot: detail.State.Repo.Root, Config: config.WorkspaceConfig, Runner: runner})
	if err != nil {
		return "", fmt.Errorf("resolve prerequisite baseline branch: %w", err)
	}
	branch, err := manager.ResolveBaseBranch(ctx, workspace.PrepareOptions{BaseBranch: recorded, PreferDefaultBranch: true})
	if err != nil {
		return "", fmt.Errorf("resolve prerequisite baseline branch: %w", err)
	}
	if strings.TrimSpace(branch) == "" {
		return "", fmt.Errorf("resolve prerequisite baseline branch: branch is empty")
	}
	return strings.TrimSpace(branch), nil
}

func resolvePrerequisiteRevision(ctx context.Context, detail *plan.PlanDetail, runner CommandRunner, revision string) (string, error) {
	sha, err := gitClient(runExecution{Dependencies: RunDependencies{CommandRunner: runner}}, detail.State.Repo.Root).RevParse(ctx, revision)
	if err != nil {
		return "", fmt.Errorf("resolve prerequisite baseline %q: %w", revision, err)
	}
	if strings.TrimSpace(sha) == "" {
		return "", fmt.Errorf("resolve prerequisite baseline %q: empty revision", revision)
	}
	return strings.TrimSpace(sha), nil
}

func currentPlanMergedSHA(events []plan.Event) string {
	for i := len(events) - 1; i >= 0; i-- {
		switch events[i].Type {
		case plan.EventTypePlanReopened:
			return ""
		case plan.EventTypePlanMerged:
			return strings.TrimSpace(events[i].MergedDefaultSHA)
		}
	}
	return ""
}

func shortRevision(revision string) string {
	if len(revision) > 12 {
		return revision[:12]
	}
	return revision
}
