package run

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/internal/workspace"
)

type prerequisiteResolverFake struct {
	plans map[string]*plan.PlanDetail
	errs  map[string]error
}

func (f prerequisiteResolverFake) GetPlanExact(ctx context.Context, id string) (*plan.PlanDetail, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := f.errs[id]; err != nil {
		return nil, err
	}
	return f.plans[id], nil
}

func prerequisiteDetail(id string, prerequisites ...string) *plan.PlanDetail {
	links := make([]plan.RuntimePrerequisite, 0, len(prerequisites))
	for _, dependency := range prerequisites {
		links = append(links, plan.RuntimePrerequisite{PlanID: dependency, Reason: "required first"})
	}
	return &plan.PlanDetail{State: plan.State{Repo: plan.Repo{Root: "/repo", Branch: "main"}, Plan: plan.PlanState{ID: id, RuntimePrerequisites: links}}}
}

func mergedPrerequisite(id, sha string) *plan.PlanDetail {
	detail := prerequisiteDetail(id)
	detail.Events = []plan.Event{{Type: plan.EventTypePlanMerged, PlanID: id, MergedDefaultSHA: sha}}
	return detail
}

func prerequisiteGitRunner(ancestors map[string]bool, calls *[]string) CommandRunner {
	return func(_ context.Context, _ string, name string, args []string, _ io.Writer, _ io.Writer) error {
		key := name + " " + strings.Join(args, " ")
		*calls = append(*calls, key)
		if name == "git" && len(args) >= 6 && args[2] == "merge-base" && args[3] == "--is-ancestor" && ancestors[args[4]+" "+args[5]] {
			return nil
		}
		return errors.New("exit status 1")
	}
}

func TestCheckRuntimePrerequisitesClassifiesBlockingEvidence(t *testing.T) {
	prOnly := prerequisiteDetail("pr-only")
	prOnly.State.Plan.PullRequest = &plan.PullRequest{HeadSHA: "head"}
	prOnly.State.Plan.Review = &plan.PlanReview{Status: plan.ReviewStatusCompleted, Verdict: plan.ReviewVerdictApprove, Head: "head"}

	tests := []struct {
		name   string
		id     string
		plans  map[string]*plan.PlanDetail
		errs   map[string]error
		status PrerequisiteStatus
		want   string
	}{
		{name: "missing", id: "missing", plans: map[string]*plan.PlanDetail{}, status: PrerequisiteMissing, want: "is missing"},
		{name: "unreadable", id: "broken", plans: map[string]*plan.PlanDetail{}, errs: map[string]error{"broken": errors.New("bad json")}, status: PrerequisiteUnreadable, want: "cannot be read"},
		{name: "unmerged", id: "pending", plans: map[string]*plan.PlanDetail{"pending": prerequisiteDetail("pending")}, status: PrerequisiteUnmerged, want: "no current plan_merged"},
		{name: "pr only", id: "pr-only", plans: map[string]*plan.PlanDetail{"pr-only": prOnly}, status: PrerequisitePullRequest, want: "pull-request completion"},
		{name: "non ancestor", id: "merged", plans: map[string]*plan.PlanDetail{"merged": mergedPrerequisite("merged", "abcdef1234567890")}, status: PrerequisiteNonAncestor, want: "not an ancestor"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dependent := prerequisiteDetail("dependent", tt.id)
			var calls []string
			results, err := checkRuntimePrerequisites(context.Background(), prerequisiteResolverFake{plans: tt.plans, errs: tt.errs}, dependent, "baseline-sha", prerequisiteGitRunner(nil, &calls))
			if err == nil || !strings.Contains(err.Error(), tt.want) || !strings.Contains(err.Error(), "next:") {
				t.Fatalf("error = %v, want bounded actionable %q diagnostic", err, tt.want)
			}
			if len(results) != 1 || results[0].Status != tt.status {
				t.Fatalf("results = %#v, want status %q", results, tt.status)
			}
		})
	}
}

func TestCheckRuntimePrerequisitesDetectsCyclesAndSatisfiedAncestry(t *testing.T) {
	dependent := prerequisiteDetail("dependent", "first")
	first := mergedPrerequisite("first", "first-sha")
	first.State.Plan.RuntimePrerequisites = []plan.RuntimePrerequisite{{PlanID: "second", Reason: "second first"}}
	second := mergedPrerequisite("second", "second-sha")
	second.State.Plan.RuntimePrerequisites = []plan.RuntimePrerequisite{{PlanID: "dependent", Reason: "cycle"}}
	resolver := prerequisiteResolverFake{plans: map[string]*plan.PlanDetail{"first": first, "second": second}}
	var calls []string
	results, err := checkRuntimePrerequisites(context.Background(), resolver, dependent, "baseline-sha", prerequisiteGitRunner(nil, &calls))
	if err == nil || len(results) != 1 || results[0].Status != PrerequisiteCyclic || !strings.Contains(err.Error(), "dependent -> first -> second -> dependent") {
		t.Fatalf("cycle result = %#v, err = %v", results, err)
	}
	if len(calls) != 0 {
		t.Fatalf("git called before cycle resolution completed: %v", calls)
	}

	second.State.Plan.RuntimePrerequisites = nil
	ancestors := map[string]bool{"second-sha baseline-sha": true, "first-sha baseline-sha": true}
	results, err = checkRuntimePrerequisites(context.Background(), resolver, dependent, "baseline-sha", prerequisiteGitRunner(ancestors, &calls))
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 || results[0].PlanID != "second" || results[0].Status != PrerequisiteSatisfied || results[1].PlanID != "first" || results[1].Status != PrerequisiteSatisfied {
		t.Fatalf("satisfied results = %#v", results)
	}
}

func TestResolvePrerequisiteBaselineMatchesAutomaticAndManualPreparation(t *testing.T) {
	repoRoot := t.TempDir()
	runRebaseRecoveryGit(t, repoRoot, "init", "-b", "main")
	runRebaseRecoveryGit(t, repoRoot, "config", "user.email", "tao@example.com")
	runRebaseRecoveryGit(t, repoRoot, "config", "user.name", "Tao Test")
	runRebaseRecoveryGit(t, repoRoot, "commit", "--allow-empty", "-m", "base")
	runRebaseRecoveryGit(t, repoRoot, "branch", "release")
	runRebaseRecoveryGit(t, repoRoot, "commit", "--allow-empty", "-m", "prerequisite integrated")
	mainHead := rebaseRecoveryGitOutput(t, repoRoot, "rev-parse", "main")
	releaseHead := rebaseRecoveryGitOutput(t, repoRoot, "rev-parse", "release")

	dependent := prerequisiteDetail("dependent", "required")
	dependent.State.Repo = plan.Repo{Root: repoRoot, Branch: "release"}
	dependent.State.Workspace = &plan.Workspace{Strategy: plan.WorkspaceStrategyWorktree, BaseBranch: "release", HeadSHA: "old-workspace-head"}
	resolver := prerequisiteResolverFake{plans: map[string]*plan.PlanDetail{"required": mergedPrerequisite("required", mainHead)}}
	automatic := ExecutionConfig{ResolvedRunOptions: ResolvedRunOptions{ExecutionMode: ExecutionModeIsolated}}

	baseline, err := resolvePrerequisiteBaseline(context.Background(), dependent, automatic, nil, false)
	if err != nil || baseline != mainHead {
		t.Fatalf("automatic baseline = %q, err = %v, want default-branch head %q", baseline, err, mainHead)
	}
	results, err := checkRuntimePrerequisites(context.Background(), resolver, dependent, baseline, nil)
	if err != nil || len(results) != 1 || results[0].Status != PrerequisiteSatisfied {
		t.Fatalf("automatic prerequisite result = %#v, err = %v, want satisfied against main", results, err)
	}

	manualConfig := workspace.DefaultConfig()
	manualConfig.BaseBranchDetection = workspace.BaseBranchDetectManual
	manual := ExecutionConfig{ResolvedRunOptions: ResolvedRunOptions{ExecutionMode: ExecutionModeIsolated}, WorkspaceConfig: manualConfig}
	baseline, err = resolvePrerequisiteBaseline(context.Background(), dependent, manual, nil, false)
	if err != nil || baseline != releaseHead {
		t.Fatalf("manual baseline = %q, err = %v, want recorded release head %q", baseline, err, releaseHead)
	}
	results, err = checkRuntimePrerequisites(context.Background(), resolver, dependent, baseline, nil)
	if err == nil || len(results) != 1 || results[0].Status != PrerequisiteNonAncestor {
		t.Fatalf("manual prerequisite result = %#v, err = %v, want non-ancestor against release", results, err)
	}
}

func TestResolvePrerequisiteBaselineUsesFreshPreparedWorkspaceHead(t *testing.T) {
	detail := prerequisiteDetail("dependent", "required")
	detail.State.Workspace = &plan.Workspace{Strategy: plan.WorkspaceStrategyWorktree, BaseBranch: "release", HeadSHA: "fresh-head"}
	baseline, err := resolvePrerequisiteBaseline(context.Background(), detail, ExecutionConfig{ResolvedRunOptions: ResolvedRunOptions{ExecutionMode: ExecutionModeIsolated}}, nil, true)
	if err != nil || baseline != "fresh-head" {
		t.Fatalf("baseline = %q, err = %v, want freshly prepared workspace HEAD", baseline, err)
	}
}

func TestCheckRuntimePrerequisitesLegacyPlanHasNoEffects(t *testing.T) {
	results, err := checkRuntimePrerequisites(context.Background(), nil, prerequisiteDetail("legacy"), "", nil)
	if err != nil || results != nil {
		t.Fatalf("legacy result = %#v, err = %v", results, err)
	}
}
