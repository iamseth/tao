package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/iamseth/tao/internal/agent"
	"github.com/iamseth/tao/internal/agentsession"
	mergepkg "github.com/iamseth/tao/internal/merge"
	"github.com/iamseth/tao/internal/plan"
	runpkg "github.com/iamseth/tao/internal/run"
	"github.com/iamseth/tao/internal/workspace"
)

func TestNewMergeBatchAgentConfigWiresRepositoryTelemetryStoreAndClock(t *testing.T) {
	root := t.TempDir()
	store := mergepkg.NewBatchStore(filepath.Join(root, "merge-batches"), filepath.Join(root, "merge-batches", "active.json"))
	fixed := time.Date(2026, 8, 10, 21, 0, 0, 0, time.UTC)
	config := newMergeBatchAgentConfig(App{Out: io.Discard, Now: func() time.Time { return fixed }}, "/control", nil, store)
	if config.EventAppender != store || config.ControlRoot != "/control" || config.Now == nil || !config.Now().Equal(fixed) {
		t.Fatalf("batch agent config = %#v", config)
	}
}

type recordingMergeMetricsAppender struct {
	events []plan.Event
	err    error
}

func (a *recordingMergeMetricsAppender) AppendEvent(_ string, event plan.Event) error {
	a.events = append(a.events, event)
	return a.err
}

func TestNewSingleMergeAgentConfigWiresPlanTelemetryBestEffort(t *testing.T) {
	fixed := time.Date(2026, 9, 2, 20, 30, 0, 0, time.UTC)
	detail := cliMergeDetail(t)
	appender := &recordingMergeMetricsAppender{}
	var out bytes.Buffer
	runner := newCLIMergeGitRunner(t, detail.State.Repo.Root)
	config := newSingleMergeAgentConfig(App{Out: &out, Now: func() time.Time { return fixed }}, detail, detail.State.Repo.Root, runner, appender)
	if config.ControlRoot != detail.State.Repo.Root || config.CommandRunner == nil || config.Now == nil || !config.Now().Equal(fixed) || config.Observe == nil {
		t.Fatalf("single merge agent config = %#v", config)
	}
	request := mergepkg.BatchAgentSessionRequest{Operation: mergepkg.BatchAgentOperationSinglePlanResolution, CandidatePlanID: "plan-a"}
	result := mergepkg.BatchAgentSessionResult{Provider: agentsession.Result{AgentLabel: "pi", MetricsUsable: true, Metrics: &agent.Metrics{SessionID: "session-a", OutputTokens: 9}}}
	config.Observe(request, result, nil)
	config.Observe(request, mergepkg.BatchAgentSessionResult{}, nil)
	if len(appender.events) != 1 || appender.events[0].Type != plan.EventTypeAgentMetrics || appender.events[0].PlanID != "plan-a" || appender.events[0].Message != "Captured single-plan conflict resolver agent metrics" {
		t.Fatalf("generic single-plan metrics = %#v", appender.events)
	}

	appender.err = errors.New("disk full")
	config.Observe(request, result, errors.New("provider failed"))
	if !strings.Contains(out.String(), "tao telemetry warning: append agent_metrics event: disk full") {
		t.Fatalf("append warning missing from %q", out.String())
	}
}

func TestNewMergeServiceRunnerWiresDeferredGuardedSinglePlanSessions(t *testing.T) {
	t.Setenv("TAO_AGENT", "invalid-unused-provider")
	detail := cliMergeDetail(t)
	manager := &fakeWorkspaceManager{}
	app := App{
		Out:              io.Discard,
		WorkspaceManager: func(string) (WorkspaceManager, error) { return manager, nil },
	}
	runner, err := app.newMergeServiceRunner(detail)
	if err != nil {
		t.Fatalf("non-conflicting merge configured provider eagerly: %v", err)
	}
	service, ok := runner.(mergepkg.Service)
	if !ok || service.SingleResolver == nil || service.SingleReviewer == nil || service.ProposalGenerator == nil || service.Cleaner != manager {
		t.Fatalf("single-plan merge service wiring = %#v", runner)
	}
}

func TestMergeCommandApprovedPlanSuccess(t *testing.T) {
	unsetEnvForTest(t, "TAO_MERGE_VERIFY_COMMAND")
	t.Setenv("TAO_AGENT", "invalid-unused-provider")
	detail := cliMergeDetail(t)
	manager := &fakeWorkspaceManager{
		cleanPlan: workspace.CleanPlan{Branch: "tao/plan-a", Status: workspace.ManagedStatusClean, CanRemove: true, Reason: "workspace is clean"},
		managedPlans: []workspace.ManagedCleanup{
			{Branch: "tao/plan-a", WorktreePath: detail.State.Workspace.Path, Status: workspace.ManagedStatusClean, CanRemove: true, Reason: "merged into main"},
		},
	}
	runner := newCLIMergeGitRunner(t, detail.State.Repo.Root)
	var gotManagerRoot string
	providerCalls := 0
	var out bytes.Buffer
	app := App{
		Out:           &out,
		Err:           &out,
		CommandRunner: runner,
		ProcessStarter: func(context.Context, string, string, []string) (runpkg.Process, error) {
			providerCalls++
			return nil, errors.New("current approved merge must not start a provider")
		},
		Repository: func(plansDir string) Repository {
			return fakeRepository{details: map[string]*plan.PlanDetail{"plan-a": detail}}
		},
		WorkspaceManager: func(repoRoot string) (WorkspaceManager, error) {
			gotManagerRoot = repoRoot
			return manager, nil
		},
	}

	if err := app.Run(context.Background(), []string{"m", "plan-a"}); err != nil {
		t.Fatal(err)
	}
	if gotManagerRoot != detail.State.Repo.Root {
		t.Fatalf("workspace manager root = %q, want %q", gotManagerRoot, detail.State.Repo.Root)
	}
	if providerCalls != 0 {
		t.Fatalf("current approved merge started %d provider sessions", providerCalls)
	}
	if len(manager.cleanedManaged) != 1 || manager.cleanedManaged[0].Branch != "tao/plan-a" {
		t.Fatalf("expected managed cleanup for tao/plan-a, got %#v", manager.cleanedManaged)
	}
	for _, want := range []string{"merge verification skipped: no supported build system detected in " + detail.State.Repo.Root, "Merge completed: plan-a merged into main", "Cleanup completed: worktree/branch tao/plan-a removed"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("expected output to contain %q, got %q", want, out.String())
		}
	}
}

func TestMergeCommandRefusesAbandonedPlanBeforeServiceConstruction(t *testing.T) {
	detail := cliMergeDetail(t)
	detail.State.Status = plan.StatusAbandoned
	detail.Events = []plan.Event{{Type: plan.EventTypePlanAbandoned, Reason: "superseded"}}
	constructed := false
	original := newMergeServiceRunner
	newMergeServiceRunner = func(App, *plan.PlanDetail) (mergeServiceRunner, error) {
		constructed = true
		return &fakeCLIMergeService{}, nil
	}
	t.Cleanup(func() { newMergeServiceRunner = original })
	var out bytes.Buffer
	app := App{
		Out: &out, Err: &out,
		Repository: func(string) Repository {
			return fakeRepository{details: map[string]*plan.PlanDetail{"plan-a": detail}}
		},
	}

	err := app.Run(context.Background(), []string{"merge", "--force", "plan-a"})
	if err == nil || !strings.Contains(err.Error(), "plan plan-a is abandoned: superseded") {
		t.Fatalf("merge error = %v, want abandonment refusal", err)
	}
	if constructed {
		t.Fatal("abandoned merge constructed the merge service")
	}
	if strings.Contains(out.String(), "Merge completed") {
		t.Fatalf("abandoned merge reported completion: %q", out.String())
	}
}

func TestMergeCommandReloadsUnderLockBeforeSquashOrNoSquashMutation(t *testing.T) {
	for _, test := range []struct {
		name string
		args []string
	}{
		{name: "squash"},
		{name: "no squash", args: []string{"--no-squash"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			isolateAbandonBatchData(t)
			fixture := newRunPlanFixture(t, plan.StatusReviewed, nil, []string{"001-a"}, "001-a", plan.StatusCompleted)
			inner := plan.NewFileRepository(fixture.root)
			repo := &mergeResolveRaceRepository{
				inner:    inner,
				resolved: make(chan struct{}),
				release:  make(chan struct{}),
			}
			service := &fakeCLIMergeService{}
			stubMergeServiceRunner(t, service)
			var mergeOut bytes.Buffer
			app := App{Out: &mergeOut, Err: &mergeOut}
			mergeDone := make(chan error, 1)
			go func() {
				mergeDone <- app.merge(context.Background(), repo, append(test.args, fixture.id))
			}()

			<-repo.resolved
			abandonErr := (App{Out: io.Discard, Err: io.Discard}).abandon(context.Background(), inner, []string{"--reason", "superseded concurrently", fixture.id})
			close(repo.release)
			if abandonErr != nil {
				t.Fatal(abandonErr)
			}
			mergeErr := <-mergeDone
			if mergeErr == nil || !strings.Contains(mergeErr.Error(), "plan "+fixture.id+" is abandoned: superseded concurrently") {
				t.Fatalf("merge error = %v, want reloaded abandonment refusal", mergeErr)
			}
			if service.calls != 0 {
				t.Fatalf("merge reached agent or Git mutation service %d times", service.calls)
			}
			if strings.Contains(mergeOut.String(), "Merge completed") {
				t.Fatalf("refused merge reported success: %q", mergeOut.String())
			}
		})
	}
}

type mergeResolveRaceRepository struct {
	inner    *plan.FileRepository
	resolved chan struct{}
	release  chan struct{}
	calls    int
}

func (r *mergeResolveRaceRepository) ResolvePlan(ctx context.Context, input string) (*plan.PlanDetail, error) {
	detail, err := r.inner.ResolvePlan(ctx, input)
	if err != nil {
		return nil, err
	}
	r.calls++
	if r.calls == 1 {
		close(r.resolved)
		select {
		case <-r.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return detail, nil
}

func TestMergeCommandRendersTypedFailures(t *testing.T) {
	tests := []struct {
		name string
		err  error
		is   error
		want []string
	}{
		{
			name: "not approved",
			err:  &mergepkg.NotApprovedError{PlanID: "plan-a", Reason: "review status \"completed\" verdict \"changes_requested\""},
			is:   mergepkg.ErrNotApproved,
			want: []string{"Merge refused:", "not approved", "tao review --run plan-a", "--force"},
		},
		{
			name: "review base mismatch",
			err:  &mergepkg.ReviewBaseMismatchError{ReviewBase: "review-base", MergeBase: "merge-base", DefaultBranch: "main", PlanBranch: "tao/plan-a"},
			is:   mergepkg.ErrReviewBaseMismatch,
			want: []string{"Merge refused: review base mismatch", "Review Base: review-base", "Merge Base: merge-base", "tao review --run plan-a", "--force"},
		},
		{
			name: "dirty worktree",
			err:  &mergepkg.DirtyWorktreeError{Status: " M internal/cli/merge.go\n"},
			is:   mergepkg.ErrDirtyWorktree,
			want: []string{"Merge refused: worktree is dirty", "Status:", "M internal/cli/merge.go", "commit, stash, or discard"},
		},
		{
			name: "conflict",
			err:  &mergepkg.MergeConflictError{Phase: "rebase", Files: []string{"internal/cli/merge.go", "README.md"}, Cause: errors.New("conflict")},
			is:   mergepkg.ErrMergeConflict,
			want: []string{"Merge conflict during rebase", "internal/cli/merge.go", "README.md", "Tao aborted", "resolve conflicts"},
		},
		{
			name: "verify failed",
			err:  &mergepkg.VerifyFailedError{Command: "make test", RepoRoot: "/repo", Output: "FAIL\npackage failed", Cause: errors.New("exit 1")},
			is:   mergepkg.ErrVerifyFailed,
			want: []string{"Verification failed after merge", "Command: make test", "Output:", "FAIL", "--no-verify"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			detail := cliMergeDetail(t)
			service := &fakeCLIMergeService{err: tt.err}
			stubMergeServiceRunner(t, service)
			var out bytes.Buffer
			app := App{Out: &out, Err: &out, Repository: func(plansDir string) Repository {
				return fakeRepository{details: map[string]*plan.PlanDetail{"plan-a": detail}}
			}}

			err := app.Run(context.Background(), []string{"merge", "plan-a"})
			if err == nil {
				t.Fatal("expected merge failure")
			}
			if !errors.Is(err, tt.is) {
				t.Fatalf("expected error %v, got %v", tt.is, err)
			}
			if service.calls != 1 || service.detail != detail {
				t.Fatalf("expected fake service to receive detail once, calls=%d detail=%#v", service.calls, service.detail)
			}
			for _, want := range tt.want {
				if !strings.Contains(out.String(), want) {
					t.Fatalf("expected output to contain %q, got %q", want, out.String())
				}
			}
		})
	}
}

func TestRenderMergeAutomaticResolutionAndReviewFailuresAreActionable(t *testing.T) {
	tests := []struct {
		name   string
		detail func(*plan.PlanDetail)
		err    error
		want   []string
	}{
		{
			name: "unsafe resolver edits",
			err:  errors.Join(mergepkg.ErrSingleResolutionRejected, errors.New("resolver left conflict markers in README.md")),
			want: []string{"rejected unsafe or unresolved edits", "restored main", "one attempt", "--force cannot bypass"},
		},
		{
			name: "confinement preflight unavailable",
			err:  errors.Join(mergepkg.ErrSingleResolutionRejected, mergepkg.ErrSingleResolutionPreflight, errors.New("bubblewrap is unavailable")),
			want: []string{"could not start safely", "started no provider", "recorded no one-shot resolution request", "Linux requires bwrap", "tao doctor", "tao merge plan-a"},
		},
		{
			name: "changes requested with rollback warning",
			detail: func(detail *plan.PlanDetail) {
				detail.State.Plan.MergeCommitIntent = &plan.SingleMergeCommitIntent{Resolution: &plan.SingleMergeResolution{Review: &plan.SingleMergeResolutionReview{
					Status: plan.ReviewStatusCompleted, Verdict: plan.ReviewVerdictChangesRequested, Summary: "Fix the merged behavior.",
					Findings: []plan.ReviewFinding{{File: "internal/merge/service.go", Message: "Handle the edge case."}},
				}}}
			},
			err:  errors.Join(mergepkg.ErrSingleReviewNotApproved, errors.New("automatic rollback refused because Git no longer matches the exact resolved transaction")),
			want: []string{"review requested changes", "Fix the merged behavior", "internal/merge/service.go", "Rollback warning", "automatic review is not retried", "--no-verify"},
		},
		{
			name: "exact recovery refusal",
			err:  errors.Join(mergepkg.ErrSingleResolutionDrift, errors.New("source ref changed")),
			want: []string{"recovery refused", "recorded default/source SHAs", "reconcile the transaction manually"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			detail := cliMergeDetail(t)
			if tt.detail != nil {
				tt.detail(detail)
			}
			var out bytes.Buffer
			if err := renderMergeFailure(&out, detail, tt.err); err != nil {
				t.Fatal(err)
			}
			for _, want := range tt.want {
				if !strings.Contains(out.String(), want) {
					t.Fatalf("output missing %q: %q", want, out.String())
				}
			}
			if strings.Contains(out.String(), "merge --all") {
				t.Fatalf("single-plan failure suggested batch workaround: %q", out.String())
			}
		})
	}
}

func TestRenderMergeSuccessNamesIndependentApprovalAfterRecordMerged(t *testing.T) {
	detail := cliMergeDetail(t)
	integrationHead := strings.Repeat("c", 40)
	detail.State.Plan.MergeCommitIntent = &plan.SingleMergeCommitIntent{Resolution: &plan.SingleMergeResolution{
		Phase: plan.SingleMergeResolutionPhaseReviewed, IntegrationHead: integrationHead,
		Review: &plan.SingleMergeResolutionReview{Status: plan.ReviewStatusCompleted, Verdict: plan.ReviewVerdictApprove},
	}}
	record, err := plan.NewPlanRecordWithStore(fakeRepository{}, detail.Dir, detail)
	if err != nil {
		t.Fatal(err)
	}
	if err := record.RecordMerged("tao/plan-a", integrationHead, time.Date(2026, 9, 3, 1, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	if detail.State.Plan.MergeCommitIntent != nil {
		t.Fatal("RecordMerged retained active resolution intent")
	}

	var out bytes.Buffer
	if err := renderMergeSuccess(&out, detail); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Independent review approved the exact conflict-resolved integration") || !strings.Contains(out.String(), "Merge completed") {
		t.Fatalf("success output = %q", out.String())
	}
}

func TestMergeCommandPassesForceNoSquashNoVerifyAndVerifyCommandOptions(t *testing.T) {
	detail := cliMergeDetail(t)
	service := &fakeCLIMergeService{}
	stubMergeServiceRunner(t, service)
	var out bytes.Buffer
	app := App{Out: &out, Err: &out, Repository: func(plansDir string) Repository {
		return fakeRepository{details: map[string]*plan.PlanDetail{"plan-a": detail}}
	}}

	if err := app.Run(context.Background(), []string{"merge", "--force", "--no-squash", "--no-verify", "--verify-command", "custom verify", "plan-a"}); err != nil {
		t.Fatal(err)
	}
	if !service.options.Force || !service.options.NoSquash || !service.options.NoVerify || service.options.VerifyCommand != "custom verify" {
		t.Fatalf("expected force, no-squash, no-verify, and verify-command options, got %#v", service.options)
	}
}

func TestMergeAllCommandPassesBatchOptionsAndRendersInvariant(t *testing.T) {
	batch := &fakeCLIMergeBatchRunner{result: mergeBatchResult{
		DryRun: true,
		State:  mergepkg.BatchState{ID: "batch-a", Status: mergepkg.BatchStatusPlanned, ChosenOrder: []string{"plan-a", "plan-b"}},
		Candidates: []mergepkg.BatchCandidate{
			{PlanID: "plan-a", Branch: "tao/plan-a", SourceTip: "head-a", ReviewBase: "base", ReviewHead: "head-a"},
			{PlanID: "plan-b", Branch: "tao/plan-b", SourceTip: "head-b", ReviewBase: "base", ReviewHead: "head-b"},
		},
		Deferred: []mergepkg.BatchDeferral{{PlanID: "plan-b", Reason: "verification failed"}},
	}}
	stubMergeBatchRunner(t, batch)
	var out bytes.Buffer
	app := App{Out: &out, Err: &out, Repository: func(string) Repository { return fakeRepository{} }}

	if err := app.Run(context.Background(), []string{"merge", "--all", "--dry-run", "--verify-command", "make verify"}); err != nil {
		t.Fatal(err)
	}
	if batch.calls != 1 || !batch.options.DryRun || batch.options.VerifyCommand != "make verify" {
		t.Fatalf("batch options = %#v, calls=%d", batch.options, batch.calls)
	}
	for _, want := range []string{"Candidate snapshot:", "plan-a", "Order: plan-a -> plan-b", "Deferred plan-b: verification failed", "Dry run:", "Default branch has not moved."} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("expected output to contain %q, got %q", want, out.String())
		}
	}
}

func TestMergeAllAutoEjectEnablesSameRunEjectAndReland(t *testing.T) {
	reason := "aggregate review not converging on internal/cli/merge.go (plan plan-a)"
	batch := &fakeCLIMergeBatchRunner{result: mergeBatchResult{
		State: mergepkg.BatchState{
			ID: "batch-a", Status: mergepkg.BatchStatusCompleted,
			Ejection: &mergepkg.BatchEjection{PlanID: "plan-a", Reason: reason, Status: "completed"},
		},
		DefaultMoved: true,
	}}
	stubMergeBatchRunner(t, batch)
	var out bytes.Buffer
	app := App{Out: &out, Err: &out, Repository: func(string) Repository { return fakeRepository{} }}

	if err := app.Run(context.Background(), []string{"merge", "--all", "--auto-eject"}); err != nil {
		t.Fatal(err)
	}
	if batch.calls != 1 || !batch.options.AutoEject {
		t.Fatalf("auto-eject batch options = %#v, calls=%d", batch.options, batch.calls)
	}
	if !strings.Contains(out.String(), "Ejected plan-a: "+reason) {
		t.Fatalf("successful auto-ejection attribution missing from output: %q", out.String())
	}
}

func TestMergeAllRerunTriggeredEjectionRendersAttribution(t *testing.T) {
	reason := "aggregate review not converging on internal/cli/merge.go (plan plan-a)"
	batch := &fakeCLIMergeBatchRunner{result: mergeBatchResult{
		Resumed: true,
		State: mergepkg.BatchState{
			ID: "batch-a", Status: mergepkg.BatchStatusCompleted,
			Ejection: &mergepkg.BatchEjection{PlanID: "plan-a", Reason: reason, Status: "completed"},
		},
		DefaultMoved: true,
	}}
	stubMergeBatchRunner(t, batch)
	var out bytes.Buffer
	app := App{Out: &out, Err: &out, Repository: func(string) Repository { return fakeRepository{} }}

	if err := app.Run(context.Background(), []string{"merge", "--all"}); err != nil {
		t.Fatal(err)
	}
	if batch.calls != 1 || batch.options.AutoEject {
		t.Fatalf("rerun batch options = %#v, calls=%d", batch.options, batch.calls)
	}
	for _, want := range []string{"Resuming merge batch batch-a", "Ejected plan-a: " + reason} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("successful rerun-triggered ejection output missing %q: %q", want, out.String())
		}
	}
}

func TestMergeAllDefaultStopsWithAttributedNonConvergenceOffer(t *testing.T) {
	reason := "aggregate review not converging on internal/cli/merge.go (plan plan-a)"
	candidates := []mergepkg.BatchCandidate{{PlanID: "plan-a"}, {PlanID: "plan-b"}}
	batch := &fakeCLIMergeBatchRunner{
		result: mergeBatchResult{
			State: mergepkg.BatchState{
				ID: "batch-a", Status: mergepkg.BatchStatusBlocked, BlockedReason: reason,
				Candidates:     candidates,
				NonConvergence: &mergepkg.BatchNonConvergence{Files: []string{"internal/cli/merge.go"}, PlanID: "plan-a", Reason: reason},
			},
			Candidates: candidates,
		},
		err: errors.New(reason),
	}
	stubMergeBatchRunner(t, batch)
	var out bytes.Buffer
	app := App{Out: &out, Err: &out, Repository: func(string) Repository { return fakeRepository{} }}

	err := app.Run(context.Background(), []string{"merge", "--all"})
	if err == nil || !strings.Contains(err.Error(), "not converging") {
		t.Fatalf("expected non-convergence error, got %v", err)
	}
	if batch.options.AutoEject {
		t.Fatal("default merge unexpectedly enabled auto-eject")
	}
	for _, want := range []string{
		"Aggregate review is not converging:",
		"Plan: plan-a",
		"File: internal/cli/merge.go",
		"Batch remains blocked",
		"Rerun `tao merge --all` to eject plan-a",
		"Use `--auto-eject`",
		"Default branch has not moved.",
	} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("expected output to contain %q, got %q", want, out.String())
		}
	}
}

func TestMergeBatchResultCharacterizesBlockersNewBatchAndOrdinaryResume(t *testing.T) {
	candidate := mergepkg.BatchCandidate{PlanID: "plan-a", Branch: "tao/plan-a", SourceTip: "head-a", ReviewBase: "base", ReviewHead: "head-a"}
	tests := []struct {
		name    string
		result  mergeBatchResult
		want    []string
		notWant []string
	}{
		{
			name: "preflight blockers remain renderable with the candidate snapshot",
			result: mergeBatchResult{
				Candidates: []mergepkg.BatchCandidate{candidate},
				Blockers:   []mergepkg.BatchBlocker{{PlanID: "plan-a", Stage: "preflight", Reason: "review head is stale"}},
			},
			want: []string{"Candidate snapshot:", "Blocked [preflight] plan-a: review head is stale", "Default branch has not moved."},
		},
		{
			name: "new batch",
			result: mergeBatchResult{
				State:      mergepkg.BatchState{ID: "batch-new", Status: mergepkg.BatchStatusReviewing, ChosenOrder: []string{"plan-a"}},
				Candidates: []mergepkg.BatchCandidate{candidate},
			},
			want:    []string{"Candidate snapshot:", "Order: plan-a", "Default branch has not moved."},
			notWant: []string{"Resuming merge batch"},
		},
		{
			name: "ordinary resume",
			result: mergeBatchResult{
				State:      mergepkg.BatchState{ID: "batch-active", Status: mergepkg.BatchStatusIntegrating, ChosenOrder: []string{"plan-a"}},
				Candidates: []mergepkg.BatchCandidate{candidate}, Resumed: true,
			},
			want: []string{"Resuming merge batch batch-active from integrating", "Candidate snapshot:", "Order: plan-a", "Default branch has not moved."},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out bytes.Buffer
			if err := renderMergeBatchResult(&out, tt.result); err != nil {
				t.Fatal(err)
			}
			for _, want := range tt.want {
				if !strings.Contains(out.String(), want) {
					t.Fatalf("expected %q in %q", want, out.String())
				}
			}
			for _, notWant := range tt.notWant {
				if strings.Contains(out.String(), notWant) {
					t.Fatalf("did not expect %q in %q", notWant, out.String())
				}
			}
		})
	}
}

func TestMergeAllDoesNotOfferUnavailableOperatorEjection(t *testing.T) {
	tests := []struct {
		name  string
		state mergepkg.BatchState
	}{
		{
			name: "only candidate",
			state: mergepkg.BatchState{
				ID: "batch-one", Status: mergepkg.BatchStatusBlocked,
				Candidates:     []mergepkg.BatchCandidate{{PlanID: "plan-a"}},
				NonConvergence: &mergepkg.BatchNonConvergence{PlanID: "plan-a", Reason: "not converging"},
			},
		},
		{
			name: "prior ejection",
			state: mergepkg.BatchState{
				ID: "batch-ejected", Status: mergepkg.BatchStatusBlocked,
				Candidates:     []mergepkg.BatchCandidate{{PlanID: "plan-a"}, {PlanID: "plan-b"}, {PlanID: "plan-c"}},
				NonConvergence: &mergepkg.BatchNonConvergence{PlanID: "plan-b", Reason: "still not converging"},
				Ejection:       &mergepkg.BatchEjection{PlanID: "plan-a", Status: "completed"},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out bytes.Buffer
			if err := renderMergeBatchResult(&out, mergeBatchResult{Resumed: true, State: tt.state, Candidates: tt.state.Candidates}); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(out.String(), "Rerun `tao merge --all` to eject") || strings.Contains(out.String(), "Use `--auto-eject`") {
				t.Fatalf("unavailable ejection was advertised: %q", out.String())
			}
			if !strings.Contains(out.String(), "not eligible for operator ejection") || !strings.Contains(out.String(), "review the findings manually") {
				t.Fatalf("manual-only guidance missing: %q", out.String())
			}
		})
	}
}

func TestMergeAllFlagCompatibilityAndPositionals(t *testing.T) {
	app := App{Out: io.Discard, Err: io.Discard, Repository: func(string) Repository { return fakeRepository{} }}
	for _, args := range [][]string{
		{"merge", "--all", "plan-a"},
		{"merge", "--dry-run", "plan-a"},
		{"merge", "--restart", "plan-a"},
		{"merge", "--auto-eject", "plan-a"},
		{"merge", "--all", "--force"},
		{"merge", "--all", "--auto-eject", "--force"},
		{"merge", "--all", "--record-only"},
		{"merge", "--all", "--no-squash"},
		{"merge", "--all", "--no-verify"},
	} {
		if err := app.Run(context.Background(), args); err == nil {
			t.Fatalf("expected %v to fail", args)
		}
	}
}

func TestMergeAllResumeRendersLandedSettlementRecovery(t *testing.T) {
	batch := &fakeCLIMergeBatchRunner{result: mergeBatchResult{
		Resumed:      true,
		DefaultMoved: true,
		State: mergepkg.BatchState{
			ID: "batch-a", Status: mergepkg.BatchStatusCompleted,
			Settlement: []mergepkg.BatchSettlement{
				{PlanID: "plan-a", MergeEvidenceRecorded: true, Completed: true, WorkspaceCleaned: true, BranchCleaned: true},
				{PlanID: "plan-b", MergeEvidenceRecorded: true, Completed: true, RequiresAttention: true, Error: "worktree is dirty"},
			},
		},
		Candidates: []mergepkg.BatchCandidate{{PlanID: "plan-a"}, {PlanID: "plan-b"}},
	}}
	stubMergeBatchRunner(t, batch)
	var out bytes.Buffer
	app := App{Out: &out, Err: &out, Repository: func(string) Repository { return fakeRepository{} }}
	if err := app.Run(context.Background(), []string{"merge", "--all"}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Resuming merge batch batch-a from completed", "Settlement plan-a: cleaned", "Settlement plan-b: requires attention: worktree is dirty", "Merge batch settlement completed", "Default branch moved"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("expected %q in %q", want, out.String())
		}
	}
}

func TestMergeAllZeroCandidatesIsSuccessfulNoOp(t *testing.T) {
	batch := &fakeCLIMergeBatchRunner{}
	stubMergeBatchRunner(t, batch)
	var out bytes.Buffer
	app := App{Out: &out, Err: &out, Repository: func(string) Repository { return fakeRepository{} }}
	if err := app.Run(context.Background(), []string{"merge", "--all"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "no reviewed and approved plans; nothing to do") || !strings.Contains(out.String(), "Default branch has not moved") {
		t.Fatalf("unexpected no-op output %q", out.String())
	}
}

func TestMergeCommandHelpDocumentsVerifyCommand(t *testing.T) {
	var out bytes.Buffer
	app := App{Out: &out, Err: &out}

	if err := app.Run(context.Background(), []string{"merge", "--help"}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"--all", "--dry-run", "--restart", "--auto-eject", "eject-and-reland", "--verify-command", "override the post-merge build/test verification command", "one automatic resolver attempt", "independent fresh-session review", "--force cannot bypass these safety and review gates", "--no-verify skips only command verification", "--no-squash rebase conflicts remain manual", "bounded agent resolution", "aggregate approval before one fast-forward", "Batch mode rejects --force, --record-only, --no-squash, and --no-verify", "single-plan only", "Usage:\n  tao merge (m) [--force] [--record-only] [--no-squash] [--no-verify] [--verify-command CMD]", "merge (m) --all [--dry-run] [--restart] [--auto-eject] [--verify-command CMD]"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("expected help to contain %q, got %q", want, out.String())
		}
	}
}

type fakeCLIMergeBatchRunner struct {
	result  mergeBatchResult
	err     error
	calls   int
	options mergeBatchOptions
}

func (f *fakeCLIMergeBatchRunner) Run(_ context.Context, options mergeBatchOptions) (mergeBatchResult, error) {
	f.calls++
	f.options = options
	return f.result, f.err
}

func stubMergeBatchRunner(t *testing.T, runner mergeBatchRunner) {
	t.Helper()
	original := newMergeBatchRunner
	newMergeBatchRunner = func(context.Context, App, mergepkg.BatchPlanRepository) (mergeBatchRunner, error) { return runner, nil }
	t.Cleanup(func() { newMergeBatchRunner = original })
}

type fakeCLIMergeService struct {
	err     error
	calls   int
	detail  *plan.PlanDetail
	options mergepkg.Options
}

func (f *fakeCLIMergeService) Merge(ctx context.Context, detail *plan.PlanDetail, options mergepkg.Options) error {
	_ = ctx
	f.calls++
	f.detail = detail
	f.options = options
	return f.err
}

func stubMergeServiceRunner(t *testing.T, service mergeServiceRunner) {
	t.Helper()
	original := newMergeServiceRunner
	newMergeServiceRunner = func(a App, detail *plan.PlanDetail) (mergeServiceRunner, error) {
		return service, nil
	}
	t.Cleanup(func() { newMergeServiceRunner = original })
}

func unsetEnvForTest(t *testing.T, key string) {
	t.Helper()
	value, ok := os.LookupEnv(key)
	if err := os.Unsetenv(key); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if ok {
			if err := os.Setenv(key, value); err != nil {
				t.Fatal(err)
			}
			return
		}
		if err := os.Unsetenv(key); err != nil {
			t.Fatal(err)
		}
	})
}

func cliMergeDetail(t *testing.T) *plan.PlanDetail {
	t.Helper()
	repoRoot := t.TempDir()
	planDir := t.TempDir()
	return &plan.PlanDetail{
		Dir: planDir,
		State: plan.State{
			Status: plan.StatusCompleted,
			Repo:   plan.Repo{Name: "tao", Root: repoRoot, Branch: "main"},
			Plan: plan.PlanState{
				ID:              "plan-a",
				Title:           "Plan A",
				CompletedSlices: []string{"001-a"},
				Review: &plan.PlanReview{
					Status: plan.ReviewStatusCompleted, Verdict: plan.ReviewVerdictApprove, Base: "base123", Head: "head123",
					CommitMessage: &plan.ReviewCommitMessage{Subject: "feat(merge): use approved review message", Body: "What:\nCreate the reviewed squash.\n\nWhy:\nAvoid another model session."},
				},
			},
			Workspace: &plan.Workspace{Strategy: plan.WorkspaceStrategyWorktree, Root: repoRoot, Path: repoRoot + "/.tao/workspaces/plan-a", Branch: "tao/plan-a", BaseBranch: "main"},
		},
		Slices: plan.SlicesFile{PlanID: "plan-a", Slices: []plan.Slice{{ID: "001-a", Status: plan.StatusCompleted}}},
	}
}

func newCLIMergeGitRunner(t *testing.T, repoRoot string) CommandRunner {
	t.Helper()
	var revParseMainCalls int
	return func(ctx context.Context, cwd string, name string, args []string, stdout io.Writer, stderr io.Writer) error {
		_ = ctx
		_ = stderr
		if cwd != "" {
			t.Fatalf("git cwd = %q, want empty cwd with git -C", cwd)
		}
		if name != "git" {
			t.Fatalf("unexpected command %q %v", name, args)
		}
		if len(args) < 3 || args[0] != "-C" {
			t.Fatalf("unexpected git args %#v", args)
		}
		dir := args[1]
		command := strings.Join(args[2:], " ")
		// The pre-merge gate also checks the isolated plan worktree's cleanliness
		// through a worktree-bound git client (git -C <worktree> status). Accept
		// that probe and report a clean worktree.
		if dir == repoRoot+"/.tao/workspaces/plan-a" {
			if command != "status --porcelain" {
				t.Fatalf("unexpected plan-worktree git command %q", command)
			}
			return nil
		}
		if dir != repoRoot {
			t.Fatalf("unexpected git args %#v, want -C %q or the plan worktree", args, repoRoot)
		}
		switch command {
		case "status --porcelain", "status --porcelain=v1 -z --untracked-files=all":
			return nil
		case "symbolic-ref --quiet --short refs/remotes/origin/HEAD":
			_, _ = io.WriteString(stdout, "origin/main\n")
			return nil
		case "merge-base main tao/plan-a", "merge-base pre123 head123":
			_, _ = io.WriteString(stdout, "base123\n")
			return nil
		case "merge-base --is-ancestor tao/plan-a main", "merge-base --is-ancestor head123 main":
			return errors.New("not ancestor")
		// External-merge detection resolves the live plan branch tip and each
		// recorded head snapshot to discard snapshots the branch moved past.
		case "rev-parse tao/plan-a", "rev-parse head123":
			_, _ = io.WriteString(stdout, "head123\n")
			return nil
		case "rev-parse main":
			revParseMainCalls++
			if revParseMainCalls <= 2 {
				_, _ = io.WriteString(stdout, "pre123\n")
			} else {
				_, _ = io.WriteString(stdout, "merged123\n")
			}
			return nil
		case "merge-base --is-ancestor main tao/plan-a", "checkout main", "merge --squash tao/plan-a":
			return nil
		default:
			if strings.HasPrefix(command, "commit -m feat(merge): use approved review message\n\nWhat:\nCreate the reviewed squash.\n\nWhy:\nAvoid another model session.\n\nTao-Plan: plan-a\nTao-Source-Head: head123") {
				return nil
			}
			t.Fatalf("unexpected git command %q", command)
			return nil
		}
	}
}
