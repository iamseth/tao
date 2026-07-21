package merge

import (
	"context"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/iamseth/tao/internal/workspace"
)

var (
	_ BatchCoordinatorStore      = (*BatchStore)(nil)
	_ BatchCoordinatorWorkspace  = (*BatchWorkspace)(nil)
	_ BatchCoordinatorDiscovery  = BatchCandidateDiscovery{}
	_ BatchCoordinatorPlanner    = Service{}
	_ BatchCoordinatorIntegrator = BatchIntegrator{}
	_ BatchCoordinatorLander     = BatchLander{}
	_ BatchCoordinatorSettler    = BatchSettler{}
)

func TestBatchCoordinatorResultPreservesInvocationOutcomes(t *testing.T) {
	restart := &BatchRestartPlan{BatchID: "batch-a", RemoveWorktree: true, RemoveBranch: true, RemoveRecovery: true}
	result := BatchCoordinatorResult{
		State:        BatchState{ID: "batch-a", Status: BatchStatusPlanned},
		Candidates:   []BatchCandidate{{PlanID: "plan-a"}},
		Blockers:     []BatchBlocker{{PlanID: "plan-b", Stage: "preflight", Reason: "stale review"}},
		Deferred:     []BatchDeferral{{PlanID: "plan-c", Reason: "overlap", OverlapCount: 2}},
		Resumed:      true,
		DryRun:       true,
		Restarted:    restart,
		DefaultMoved: true,
	}
	if result.State.ID != "batch-a" || len(result.Candidates) != 1 || len(result.Blockers) != 1 || len(result.Deferred) != 1 {
		t.Fatalf("coordinator result lost rendered data: %+v", result)
	}
	if !result.Resumed || !result.DryRun || result.Restarted != restart || !result.DefaultMoved {
		t.Fatalf("coordinator result lost invocation outcomes: %+v", result)
	}

	options := BatchCoordinatorOptions{DryRun: true, Restart: true, AutoEject: true, VerifyCommand: "make verify"}
	if !options.DryRun || !options.Restart || !options.AutoEject || options.VerifyCommand != "make verify" {
		t.Fatalf("coordinator options lost caller controls: %+v", options)
	}
}

func TestBatchEjectionResumeTarget(t *testing.T) {
	nonConvergence := &BatchNonConvergence{PlanID: "plan-a", Reason: "not converging"}
	tests := []struct {
		name   string
		state  BatchState
		target string
		ok     bool
	}{
		{
			name: "pending durable ejection",
			state: BatchState{
				Status: BatchStatusBlocked, Candidates: []BatchCandidate{{PlanID: "plan-a"}, {PlanID: "plan-b"}},
				NonConvergence: nonConvergence, Ejection: &BatchEjection{PlanID: "plan-b", Status: batchEjectionPending},
			},
			target: "plan-b", ok: true,
		},
		{
			name: "reintegrating durable ejection",
			state: BatchState{
				Status: BatchStatusIntegrating, Candidates: []BatchCandidate{{PlanID: "plan-a"}, {PlanID: "plan-b"}},
				Ejection: &BatchEjection{PlanID: "plan-a", Status: batchEjectionReintegrating},
			},
			target: "plan-a", ok: true,
		},
		{
			name: "operator ejection",
			state: BatchState{
				Status: BatchStatusBlocked, Candidates: []BatchCandidate{{PlanID: "plan-a"}, {PlanID: "plan-b"}},
				NonConvergence: nonConvergence,
			},
			target: "plan-a", ok: true,
		},
		{
			name: "ordinary blocked resume",
			state: BatchState{
				Status: BatchStatusBlocked, Candidates: []BatchCandidate{{PlanID: "plan-a"}}, NonConvergence: nonConvergence,
			},
		},
		{
			name: "non-blocked attribution",
			state: BatchState{
				Status: BatchStatusReviewing, Candidates: []BatchCandidate{{PlanID: "plan-a"}, {PlanID: "plan-b"}}, NonConvergence: nonConvergence,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			target, ok := BatchEjectionResumeTarget(tt.state)
			if target != tt.target || ok != tt.ok {
				t.Fatalf("BatchEjectionResumeTarget() = (%q, %t), want (%q, %t)", target, ok, tt.target, tt.ok)
			}
		})
	}
}

func TestBatchCoordinatorPlansInitializesAndStartsNewBatch(t *testing.T) {
	now := time.Date(2026, 7, 18, 1, 2, 3, 4, time.FixedZone("offset", 2*60*60))
	candidateA := BatchCandidate{PlanID: "plan-a"}
	candidateB := BatchCandidate{PlanID: "plan-b"}
	store := &coordinatorStore{}
	workspaceOwner := &coordinatorWorkspace{}
	coordinator := NewBatchCoordinator(BatchCoordinatorSeams{
		Store: store, Workspace: workspaceOwner,
		Discovery: coordinatorDiscovery{result: BatchPreflightResult{
			Candidates: []BatchCandidate{candidateB, candidateA}, RepoRoot: "/repo", DefaultBranch: "main", DefaultStartSHA: "base",
		}},
		Planner: coordinatorPlanner{result: BatchPlanningResult{
			Ordered:  []BatchCandidate{candidateA},
			Deferred: []BatchDeferral{{PlanID: "plan-b", Reason: "overlap", OverlapCount: 2}},
		}},
		Integrator: &coordinatorIntegrator{},
		Now:        func() time.Time { return now },
	})

	result, err := coordinator.Run(context.Background(), BatchCoordinatorOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if result.State.ID != "20260717-230203.000000004" || result.State.CreatedAt != "2026-07-17T23:02:03.000000004Z" || result.State.UpdatedAt != result.State.CreatedAt {
		t.Fatalf("batch identity or timestamps changed: %#v", result.State)
	}
	if !reflect.DeepEqual(result.State.ChosenOrder, []string{"plan-a"}) || result.State.LogSequence != 1 {
		t.Fatalf("initialized state = %#v", result.State)
	}
	if result.State.Candidates[0].Deferred == nil || result.State.Candidates[0].Deferred.PlanID != "plan-b" {
		t.Fatalf("planning deferral was not copied to candidate: %#v", result.State.Candidates)
	}
	if store.initialized.ID != result.State.ID || workspaceOwner.started != result.State.ID || workspaceOwner.statused != "" {
		t.Fatalf("setup calls: initialized=%q started=%q statused=%q", store.initialized.ID, workspaceOwner.started, workspaceOwner.statused)
	}
	workspaceOwner.requireOwnershipReleased(t)
}

func TestBatchCoordinatorReportsDiscoveryAndPlanningBlockersWithoutDurableState(t *testing.T) {
	tests := []struct {
		name      string
		preflight BatchPreflightResult
		planning  BatchPlanningResult
		want      string
	}{
		{
			name: "preflight", preflight: BatchPreflightResult{
				Candidates: []BatchCandidate{{PlanID: "plan-a"}}, Blockers: []BatchBlocker{{PlanID: "plan-a", Stage: "preflight", Reason: "stale"}},
			}, want: "preflight blocked",
		},
		{
			name: "planning", preflight: BatchPreflightResult{Candidates: []BatchCandidate{{PlanID: "plan-a"}}},
			planning: BatchPlanningResult{Blockers: []BatchBlocker{{PlanID: "plan-a", Stage: "planning", Reason: "cycle"}}}, want: "planning blocked",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &coordinatorStore{}
			result, err := NewBatchCoordinator(BatchCoordinatorSeams{
				Store: store, Workspace: &coordinatorWorkspace{}, Discovery: coordinatorDiscovery{result: tt.preflight}, Planner: coordinatorPlanner{result: tt.planning},
			}).Run(context.Background(), BatchCoordinatorOptions{})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Run() error = %v, want %q", err, tt.want)
			}
			if len(result.Candidates) != len(tt.preflight.Candidates) || len(result.Blockers) == 0 {
				t.Fatalf("blocker result lost characterized data: %#v", result)
			}
			if store.initialized.ID != "" {
				t.Fatalf("blocked batch initialized durable state: %#v", store.initialized)
			}
		})
	}
}

func TestBatchCoordinatorResumesWithOrdinaryEjectionAndPostLandingValidation(t *testing.T) {
	tests := []struct {
		name          string
		state         BatchState
		landingIntent bool
		wantValidate  string
	}{
		{name: "ordinary", state: coordinatorActiveState(BatchStatusIntegrating), wantValidate: "ordinary"},
		{
			name: "ejection", state: func() BatchState {
				state := coordinatorActiveState(BatchStatusBlocked)
				state.NonConvergence = &BatchNonConvergence{PlanID: "plan-a", Reason: "recurring findings"}
				return state
			}(), wantValidate: "ejection:plan-a",
		},
		{
			name: "post landing", state: func() BatchState {
				state := coordinatorActiveState(BatchStatusReadyToLand)
				state.Landing = &BatchLanding{IntegrationHead: "integrated"}
				return state
			}(), landingIntent: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &coordinatorStore{activeID: tt.state.ID, loaded: tt.state}
			workspaceOwner := &coordinatorWorkspace{landingIntent: tt.landingIntent}
			result, err := NewBatchCoordinator(BatchCoordinatorSeams{
				Store: store, Workspace: workspaceOwner, Integrator: &coordinatorIntegrator{}, Lander: &coordinatorLander{},
			}).Run(context.Background(), BatchCoordinatorOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if !result.Resumed || result.State.ID != tt.state.ID || !reflect.DeepEqual(result.Candidates, tt.state.Candidates) {
				t.Fatalf("resume result = %#v", result)
			}
			if workspaceOwner.validated != tt.wantValidate || workspaceOwner.statused != tt.state.ID || workspaceOwner.started != "" {
				t.Fatalf("resume calls: validated=%q statused=%q started=%q", workspaceOwner.validated, workspaceOwner.statused, workspaceOwner.started)
			}
			workspaceOwner.requireOwnershipReleased(t)
		})
	}
}

func TestBatchCoordinatorRestartRefusesDurableLandingIntent(t *testing.T) {
	state := coordinatorActiveState(BatchStatusReadyToLand)
	state.Landing = &BatchLanding{IntegrationHead: "integrated"}
	workspaceOwner := &coordinatorWorkspace{landingIntent: true}
	result, err := NewBatchCoordinator(BatchCoordinatorSeams{
		Store: &coordinatorStore{activeID: state.ID, loaded: state}, Workspace: workspaceOwner,
	}).Run(context.Background(), BatchCoordinatorOptions{Restart: true})
	if err == nil || !strings.Contains(err.Error(), "durable landing intent") {
		t.Fatalf("Run() error = %v", err)
	}
	if result.State.ID != state.ID || result.Restarted != nil || workspaceOwner.restarted {
		t.Fatalf("restart refusal result or mutations = %#v, restarted=%t", result, workspaceOwner.restarted)
	}
}

func TestBatchCoordinatorDryRunCleansWorkspaceAndReleasesOwnership(t *testing.T) {
	stateCandidate := BatchCandidate{PlanID: "plan-a"}
	workspaceOwner := &coordinatorWorkspace{}
	store := &coordinatorStore{}
	integrateErr := errors.New("simulation failed")
	integrator := &coordinatorIntegrator{result: BatchIntegrateResult{State: BatchState{ID: "simulated"}}, err: integrateErr}
	result, err := NewBatchCoordinator(BatchCoordinatorSeams{
		Store: store, Workspace: workspaceOwner,
		Discovery:  coordinatorDiscovery{result: BatchPreflightResult{Candidates: []BatchCandidate{stateCandidate}, RepoRoot: "/repo", DefaultBranch: "main", DefaultStartSHA: "base"}},
		Planner:    coordinatorPlanner{result: BatchPlanningResult{Ordered: []BatchCandidate{stateCandidate}}},
		Integrator: integrator,
		Now:        func() time.Time { return time.Date(2026, 7, 18, 0, 0, 0, 0, time.UTC) },
	}).Run(context.Background(), BatchCoordinatorOptions{DryRun: true, VerifyCommand: "make verify"})
	if !errors.Is(err, integrateErr) {
		t.Fatalf("Run() error = %v, want simulation failure", err)
	}
	if !result.DryRun || result.State.ID != "simulated" || integrator.options.VerifyCommand != "make verify" || !integrator.options.DryRun {
		t.Fatalf("dry-run result or options = %#v / %#v", result, integrator.options)
	}
	if workspaceOwner.removed == "" || workspaceOwner.removed != workspaceOwner.started {
		t.Fatalf("disposable workspace not removed: started=%q removed=%q", workspaceOwner.started, workspaceOwner.removed)
	}
	if store.initialized.ID != "" {
		t.Fatalf("dry run initialized durable state: %#v", store.initialized)
	}
	workspaceOwner.requireOwnershipReleased(t)
}

func TestBatchCoordinatorResumesBlockedPhaseBeforeDispatch(t *testing.T) {
	now := time.Date(2026, 7, 18, 2, 0, 0, 0, time.UTC)
	state := coordinatorActiveState(BatchStatusBlocked)
	state.BlockedReason = "agent session interrupted"
	state.BlockKind = BatchBlockKindResumable
	state.ResumeStatus = BatchStatusResolving
	store := &coordinatorStore{activeID: state.ID, loaded: state}
	resolver := &coordinatorResolver{}

	result, err := NewBatchCoordinator(BatchCoordinatorSeams{
		Store: store, Workspace: &coordinatorWorkspace{}, Resolver: resolver, Now: func() time.Time { return now },
	}).Run(context.Background(), BatchCoordinatorOptions{VerifyCommand: "make verify"})
	if err != nil {
		t.Fatal(err)
	}
	if store.transitioned.Status != BatchStatusResolving || store.transitioned.BlockKind != "" || store.transitioned.UpdatedAt != now.Format(time.RFC3339Nano) {
		t.Fatalf("blocked resume was not durable before dispatch: %+v", store.transitioned)
	}
	if result.State.Status != BatchStatusResolving || len(resolver.options) != 1 || resolver.options[0].VerifyCommand != "make verify" {
		t.Fatalf("resolving dispatch = %+v, options=%+v", result.State, resolver.options)
	}
}

func TestBatchCoordinatorRefusesExplicitTerminalBlockRegardlessOfReason(t *testing.T) {
	state := coordinatorActiveState(BatchStatusBlocked)
	state.BlockedReason = "operator decision needed before continuing"
	state.BlockKind = BatchBlockKindTerminal
	state.ResumeStatus = BatchStatusResolving
	state.Review = &BatchReview{Verdict: "approve"}
	store := &coordinatorStore{activeID: state.ID, loaded: state}
	resolver := &coordinatorResolver{}
	lander := &coordinatorLander{}

	result, err := NewBatchCoordinator(BatchCoordinatorSeams{
		Store: store, Workspace: &coordinatorWorkspace{}, Resolver: resolver, Lander: lander,
	}).Run(context.Background(), BatchCoordinatorOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if result.State.Status != BatchStatusBlocked || store.transitioned.ID != "" || len(resolver.options) != 0 || lander.calls != 0 {
		t.Fatalf("terminal block resumed: result=%+v transitioned=%+v resolve=%d land=%d", result.State, store.transitioned, len(resolver.options), lander.calls)
	}
}

func TestBatchCoordinatorPreservesInterruptedEjectionIntent(t *testing.T) {
	state := coordinatorActiveState(BatchStatusBlocked)
	state.NonConvergence = &BatchNonConvergence{PlanID: "plan-a", Reason: "not converging in shared.go"}
	interrupted := state
	interrupted.Ejection = &BatchEjection{PlanID: "plan-a", Reason: state.NonConvergence.Reason, Status: batchEjectionPending}
	integrator := &coordinatorIntegrator{ejectResult: BatchIntegrateResult{State: interrupted}, ejectErr: errors.New("persisted before reset")}
	workspaceOwner := &coordinatorWorkspace{}

	result, err := NewBatchCoordinator(BatchCoordinatorSeams{
		Store: &coordinatorStore{activeID: state.ID, loaded: state}, Workspace: workspaceOwner, Integrator: integrator,
	}).Run(context.Background(), BatchCoordinatorOptions{VerifyCommand: "make verify"})
	if err == nil || !strings.Contains(err.Error(), "persisted before reset") {
		t.Fatalf("Run() error = %v", err)
	}
	if result.State.Ejection == nil || result.State.Ejection.Status != batchEjectionPending || integrator.ejected != 1 || integrator.integrated != 0 {
		t.Fatalf("interrupted ejection result=%+v calls=(eject %d, integrate %d)", result.State, integrator.ejected, integrator.integrated)
	}
	if integrator.ejectOptions.VerifyCommand != "make verify" || workspaceOwner.validated != "ejection:plan-a" {
		t.Fatalf("ejection options or validation changed: %+v validated=%q", integrator.ejectOptions, workspaceOwner.validated)
	}
}

func TestBatchCoordinatorPreservesResolvingAndReviewingInterruptions(t *testing.T) {
	interruptErr := errors.New("agent interrupted")
	tests := []struct {
		name     string
		status   BatchStatus
		resolver *coordinatorResolver
		reviewer *coordinatorReviewer
	}{
		{
			name: "resolving", status: BatchStatusResolving,
			resolver: &coordinatorResolver{errs: []error{interruptErr}},
		},
		{
			name: "reviewing", status: BatchStatusReviewing,
			reviewer: &coordinatorReviewer{errs: []error{interruptErr}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state := coordinatorActiveState(tt.status)
			result, err := NewBatchCoordinator(BatchCoordinatorSeams{
				Store: &coordinatorStore{activeID: state.ID, loaded: state}, Workspace: &coordinatorWorkspace{}, Resolver: tt.resolver, Reviewer: tt.reviewer,
			}).Run(context.Background(), BatchCoordinatorOptions{})
			if !errors.Is(err, interruptErr) || result.State.Status != tt.status {
				t.Fatalf("Run() result status=%s error=%v", result.State.Status, err)
			}
		})
	}
}

func TestBatchCoordinatorReentersResolvingAndReviewing(t *testing.T) {
	calls := []string{}
	state := coordinatorActiveState(BatchStatusReviewing)
	resolving := state
	resolving.Status = BatchStatusResolving
	reviewing := state
	blocked := state
	blocked.Status = BatchStatusBlocked
	resolver := &coordinatorResolver{results: []BatchResolveResult{{State: reviewing}}, calls: &calls}
	reviewer := &coordinatorReviewer{results: []BatchReviewResult{
		{State: resolving, ReenterPhases: true},
		{State: blocked},
	}, calls: &calls}

	result, err := NewBatchCoordinator(BatchCoordinatorSeams{
		Store: &coordinatorStore{activeID: state.ID, loaded: state}, Workspace: &coordinatorWorkspace{}, Resolver: resolver, Reviewer: reviewer,
	}).Run(context.Background(), BatchCoordinatorOptions{VerifyCommand: "make verify", AutoEject: true})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(calls, []string{"review", "resolve", "review"}) || result.State.Status != BatchStatusBlocked {
		t.Fatalf("phase re-entry calls=%v state=%s", calls, result.State.Status)
	}
	if len(reviewer.options) != 2 || !reviewer.options[0].AutoEject || reviewer.options[0].VerifyCommand != "make verify" || resolver.options[0].VerifyCommand != "make verify" {
		t.Fatalf("phase options changed: review=%+v resolve=%+v", reviewer.options, resolver.options)
	}
}

func TestBatchCoordinatorResumesApprovedBlockedStateIntoLanding(t *testing.T) {
	state := coordinatorActiveState(BatchStatusBlocked)
	state.BlockedReason = "default branch temporarily unavailable"
	state.Review = &BatchReview{Verdict: "approve"}
	ready := state
	ready.Status = BatchStatusReadyToLand
	ready.BlockedReason = ""
	lander := &coordinatorLander{result: BatchLandResult{State: ready}}
	store := &coordinatorStore{activeID: state.ID, loaded: state}

	result, err := NewBatchCoordinator(BatchCoordinatorSeams{
		Store: store, Workspace: &coordinatorWorkspace{}, Lander: lander,
	}).Run(context.Background(), BatchCoordinatorOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if store.transitioned.Status != BatchStatusReadyToLand || lander.calls != 1 || result.State.Status != BatchStatusReadyToLand {
		t.Fatalf("approved blocked dispatch state=%+v transitioned=%s land=%d", result.State, store.transitioned.Status, lander.calls)
	}
}

func TestBatchCoordinatorReturnsDurableLandingIntentBeforeSettlement(t *testing.T) {
	state := coordinatorActiveState(BatchStatusReadyToLand)
	intent := state
	intent.Landing = &BatchLanding{DefaultParentSHA: "base", IntegrationHead: "integrated"}
	lander := &coordinatorLander{result: BatchLandResult{State: intent}, err: errors.New("stopped after landing intent")}
	settler := &coordinatorSettler{}

	result, err := NewBatchCoordinator(BatchCoordinatorSeams{
		Store: &coordinatorStore{activeID: state.ID, loaded: state}, Workspace: &coordinatorWorkspace{}, Lander: lander, Settler: settler,
	}).Run(context.Background(), BatchCoordinatorOptions{})
	if err == nil || !strings.Contains(err.Error(), "landing intent") {
		t.Fatalf("Run() error = %v", err)
	}
	if result.State.Landing == nil || result.DefaultMoved || lander.calls != 1 || settler.calls != 0 {
		t.Fatalf("landing interruption result=%+v land=%d settle=%d", result, lander.calls, settler.calls)
	}
}

func TestBatchCoordinatorRecoversLandedEvidenceBeforeSettlement(t *testing.T) {
	state := coordinatorActiveState(BatchStatusLanded)
	state.IntegrationHead = "integrated"
	state.LandedSHA = "integrated"
	state.Landing = &BatchLanding{DefaultParentSHA: "base", IntegrationHead: "integrated", LandedDefaultSHA: "integrated"}
	completed := state
	completed.Status = BatchStatusCompleted
	lander := &coordinatorLander{result: BatchLandResult{State: state}}
	settler := &coordinatorSettler{result: BatchSettleResult{State: completed}}

	result, err := NewBatchCoordinator(BatchCoordinatorSeams{
		Store: &coordinatorStore{activeID: state.ID, loaded: state}, Workspace: &coordinatorWorkspace{}, Lander: lander, Settler: settler,
	}).Run(context.Background(), BatchCoordinatorOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if lander.calls != 1 || settler.calls != 1 || result.State.Status != BatchStatusCompleted || !result.DefaultMoved {
		t.Fatalf("landed recovery result=%+v land=%d settle=%d", result, lander.calls, settler.calls)
	}
}

func TestBatchCoordinatorRetriesSettlementWithoutLandingAgain(t *testing.T) {
	state := coordinatorActiveState(BatchStatusSettling)
	state.LandedSHA = "integrated"
	state.Landing = &BatchLanding{IntegrationHead: "integrated", LandedDefaultSHA: "integrated"}
	partial := state
	partial.Settlement = []BatchSettlement{{PlanID: "plan-a", MergeEvidenceRecorded: true}}
	settler := &coordinatorSettler{result: BatchSettleResult{State: partial}, err: errors.New("cleanup interrupted")}
	lander := &coordinatorLander{}

	result, err := NewBatchCoordinator(BatchCoordinatorSeams{
		Store: &coordinatorStore{activeID: state.ID, loaded: state}, Workspace: &coordinatorWorkspace{}, Lander: lander, Settler: settler,
	}).Run(context.Background(), BatchCoordinatorOptions{})
	if err == nil || !strings.Contains(err.Error(), "cleanup interrupted") {
		t.Fatalf("Run() error = %v", err)
	}
	if lander.calls != 0 || settler.calls != 1 || len(result.State.Settlement) != 1 || !result.DefaultMoved {
		t.Fatalf("settlement retry result=%+v land=%d settle=%d", result, lander.calls, settler.calls)
	}
}

type coordinatorStore struct {
	activeID     string
	loaded       BatchState
	initialized  BatchState
	transitioned BatchState
}

func (s *coordinatorStore) ActiveID() (string, error)       { return s.activeID, nil }
func (s *coordinatorStore) Load(string) (BatchState, error) { return s.loaded, nil }
func (s *coordinatorStore) Initialize(state BatchState, _ string) (BatchState, error) {
	state.LogSequence = 1
	s.initialized = state
	return state, nil
}
func (s *coordinatorStore) Transition(state BatchState, _ string) (BatchState, error) {
	s.transitioned = state
	return state, nil
}

type coordinatorDiscovery struct {
	result BatchPreflightResult
	err    error
}

func (d coordinatorDiscovery) Discover(context.Context) (BatchPreflightResult, error) {
	return d.result, d.err
}

type coordinatorPlanner struct {
	result BatchPlanningResult
	err    error
}

func (p coordinatorPlanner) PlanBatchCandidatesWithGit(context.Context, []BatchCandidate) (BatchPlanningResult, error) {
	return p.result, p.err
}

type coordinatorIntegrator struct {
	result       BatchIntegrateResult
	err          error
	ejectResult  BatchIntegrateResult
	ejectErr     error
	options      BatchIntegrateOptions
	ejectOptions BatchEjectOptions
	integrated   int
	ejected      int
}

func (i *coordinatorIntegrator) Integrate(_ context.Context, state BatchState, _ string, options BatchIntegrateOptions) (BatchIntegrateResult, error) {
	i.integrated++
	i.options = options
	if i.result.State.ID == "" {
		i.result.State = state
	}
	return i.result, i.err
}
func (i *coordinatorIntegrator) Eject(_ context.Context, state BatchState, _ string, options BatchEjectOptions) (BatchIntegrateResult, error) {
	i.ejected++
	i.ejectOptions = options
	if i.ejectResult.State.ID == "" {
		i.ejectResult.State = state
	}
	return i.ejectResult, i.ejectErr
}

type coordinatorResolver struct {
	results []BatchResolveResult
	errs    []error
	options []BatchResolveOptions
	calls   *[]string
}

func (r *coordinatorResolver) Resolve(_ context.Context, state BatchState, _ string, options BatchResolveOptions) (BatchResolveResult, error) {
	if r.calls != nil {
		*r.calls = append(*r.calls, "resolve")
	}
	r.options = append(r.options, options)
	index := len(r.options) - 1
	result := BatchResolveResult{State: state}
	if index < len(r.results) {
		result = r.results[index]
	}
	if index < len(r.errs) {
		return result, r.errs[index]
	}
	return result, nil
}

type coordinatorReviewer struct {
	results []BatchReviewResult
	errs    []error
	options []BatchReviewOptions
	calls   *[]string
}

func (r *coordinatorReviewer) Review(_ context.Context, state BatchState, _ string, options BatchReviewOptions) (BatchReviewResult, error) {
	if r.calls != nil {
		*r.calls = append(*r.calls, "review")
	}
	r.options = append(r.options, options)
	index := len(r.options) - 1
	result := BatchReviewResult{State: state}
	if index < len(r.results) {
		result = r.results[index]
	}
	if index < len(r.errs) {
		return result, r.errs[index]
	}
	return result, nil
}

type coordinatorLander struct {
	result BatchLandResult
	err    error
	calls  int
	root   string
}

func (l *coordinatorLander) Land(_ context.Context, state BatchState, root string) (BatchLandResult, error) {
	l.calls++
	l.root = root
	if l.result.State.ID == "" {
		l.result.State = state
	}
	return l.result, l.err
}

type coordinatorSettler struct {
	result BatchSettleResult
	err    error
	calls  int
}

func (s *coordinatorSettler) Settle(_ context.Context, state BatchState) (BatchSettleResult, error) {
	s.calls++
	if s.result.State.ID == "" {
		s.result.State = state
	}
	return s.result, s.err
}

type coordinatorWorkspace struct {
	landingIntent bool
	validated     string
	started       string
	statused      string
	removed       string
	restarted     bool
	ownershipFile *os.File
}

func (w *coordinatorWorkspace) DefaultReachedLandingIntent(context.Context, BatchState) (bool, error) {
	return w.landingIntent, nil
}
func (w *coordinatorWorkspace) Restart(_ context.Context, state BatchState) (BatchRestartPlan, error) {
	w.restarted = true
	return BatchRestartPlan{BatchID: state.ID, RemoveRecovery: true}, nil
}
func (w *coordinatorWorkspace) ValidateResume(context.Context, BatchState) error {
	w.validated = "ordinary"
	return nil
}
func (w *coordinatorWorkspace) ValidateEjectionResume(_ context.Context, _ BatchState, planID string) error {
	w.validated = "ejection:" + planID
	return nil
}
func (w *coordinatorWorkspace) AcquireOwnership(BatchState, time.Time) (*BatchOwnership, error) {
	file, err := os.CreateTemp("", "tao-coordinator-ownership-*")
	if err != nil {
		return nil, err
	}
	w.ownershipFile = file
	return &BatchOwnership{file: file}, nil
}
func (w *coordinatorWorkspace) Status(_ context.Context, batchID string) (workspace.IntegrationWorkspace, error) {
	w.statused = batchID
	return workspace.IntegrationWorkspace{BatchID: batchID, Path: "/integration/" + batchID}, nil
}
func (w *coordinatorWorkspace) Start(_ context.Context, state BatchState) (workspace.IntegrationWorkspace, error) {
	w.started = state.ID
	return workspace.IntegrationWorkspace{BatchID: state.ID, Path: "/integration/" + state.ID}, nil
}
func (w *coordinatorWorkspace) RemoveIntegration(_ context.Context, batchID string) error {
	w.removed = batchID
	return nil
}
func (w *coordinatorWorkspace) requireOwnershipReleased(t *testing.T) {
	t.Helper()
	if w.ownershipFile == nil {
		t.Fatal("ownership was not acquired")
	}
	if _, err := w.ownershipFile.WriteString("still open"); err == nil {
		t.Fatal("ownership handle was not released")
	}
	_ = os.Remove(w.ownershipFile.Name())
}

func coordinatorActiveState(status BatchStatus) BatchState {
	return BatchState{
		Schema: BatchStateSchema, ID: "batch-active", Status: status, RepoRoot: "/repo", DefaultBranch: "main", DefaultStartSHA: "base",
		Candidates: []BatchCandidate{{PlanID: "plan-a"}, {PlanID: "plan-b"}}, ChosenOrder: []string{"plan-a", "plan-b"},
	}
}

func TestBatchOperatorEjectAvailable(t *testing.T) {
	nonConvergence := &BatchNonConvergence{PlanID: "plan-a", Reason: "not converging"}
	tests := []struct {
		name      string
		state     BatchState
		available bool
	}{
		{name: "attributed candidate with remainder", state: BatchState{Candidates: []BatchCandidate{{PlanID: "plan-a"}, {PlanID: "plan-b"}}, NonConvergence: nonConvergence}, available: true},
		{name: "one candidate", state: BatchState{Candidates: []BatchCandidate{{PlanID: "plan-a"}}, NonConvergence: nonConvergence}},
		{name: "prior ejection", state: BatchState{Candidates: []BatchCandidate{{PlanID: "plan-a"}, {PlanID: "plan-b"}, {PlanID: "plan-c"}}, NonConvergence: nonConvergence, Ejection: &BatchEjection{PlanID: "plan-c", Status: batchEjectionCompleted}}},
		{name: "unknown candidate", state: BatchState{Candidates: []BatchCandidate{{PlanID: "plan-a"}, {PlanID: "plan-b"}}, NonConvergence: &BatchNonConvergence{PlanID: "plan-c", Reason: "not converging"}}},
		{name: "missing plan", state: BatchState{Candidates: []BatchCandidate{{PlanID: "plan-a"}, {PlanID: "plan-b"}}, NonConvergence: &BatchNonConvergence{Reason: "not converging"}}},
		{name: "missing reason", state: BatchState{Candidates: []BatchCandidate{{PlanID: "plan-a"}, {PlanID: "plan-b"}}, NonConvergence: &BatchNonConvergence{PlanID: "plan-a"}}},
		{name: "whitespace reason", state: BatchState{Candidates: []BatchCandidate{{PlanID: "plan-a"}, {PlanID: "plan-b"}}, NonConvergence: &BatchNonConvergence{PlanID: "plan-a", Reason: "  "}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := BatchOperatorEjectAvailable(tt.state); got != tt.available {
				t.Fatalf("BatchOperatorEjectAvailable() = %t, want %t", got, tt.available)
			}
		})
	}
}
