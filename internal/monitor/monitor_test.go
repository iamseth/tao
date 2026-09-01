package monitor

import (
	"context"
	"errors"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/internal/runstatus"
	"github.com/iamseth/tao/internal/taodata"
)

type fakeInventory struct {
	entries []taodata.RepoInventoryEntry
	err     error
}

func (f fakeInventory) MetadataInventory() ([]taodata.RepoInventoryEntry, error) {
	return f.entries, f.err
}

type fakePlanLister struct {
	summaries []plan.PlanSummary
	err       error
	calls     int
}

func (f *fakePlanLister) ListPlans(context.Context, plan.PlanFilter) ([]plan.PlanSummary, error) {
	f.calls++
	return f.summaries, f.err
}

type fakeStatusReader struct {
	records map[string]runstatus.Record
	errors  map[string]error
	reads   []string
}

func (f *fakeStatusReader) Read(planID string) (runstatus.Record, error) {
	f.reads = append(f.reads, planID)
	if err := f.errors[planID]; err != nil {
		return runstatus.Record{}, err
	}
	if record, ok := f.records[planID]; ok {
		return record, nil
	}
	return runstatus.Record{}, os.ErrNotExist
}

func TestCollectorBuildsAndOrdersCrossRepositorySnapshot(t *testing.T) {
	now := time.Date(2026, 7, 29, 5, 30, 0, 0, time.UTC)
	activity := func(age time.Duration) *time.Time {
		value := now.Add(-age)
		return &value
	}
	alpha := taodata.RepoInventoryEntry{Repo: taodata.Repo{ID: "repo-alpha", Name: "alpha"}, PlansDir: "/data/alpha/plans", RuntimeStatusDir: "/data/alpha/run-status"}
	beta := taodata.RepoInventoryEntry{Repo: taodata.Repo{ID: "repo-beta", Name: "beta"}, PlansDir: "/data/beta/plans", RuntimeStatusDir: "/data/beta/run-status"}
	broken := taodata.RepoInventoryEntry{Repo: taodata.Repo{ID: "repo-broken"}, MetadataError: errors.New("malformed repository metadata")}
	alphaPlans := &fakePlanLister{summaries: []plan.PlanSummary{
		{ID: "other", Title: "Other", Status: plan.StatusPlanned, PendingCount: 2, LastActivityAt: activity(time.Minute)},
		{ID: "blocked", Title: "Blocked", Status: plan.StatusBlocked, PendingCount: 1, LastActivityAt: activity(5 * time.Minute)},
		{ID: "completed", Status: plan.StatusCompleted, LastActivityAt: activity(time.Second)},
		{ID: "invalid", Status: plan.StatusInvalid, Warnings: []string{"invalid state.json"}},
		{ID: "live", Title: "Live", Status: plan.StatusInProgress, CurrentSliceID: "001-durable", CurrentSlice: &plan.Slice{ID: "001-durable", Title: "Durable"}, PendingCount: 3, LastActivityAt: activity(time.Hour), OriginalCompletedCount: 1, OriginalTotalCount: 3, ReworkCompletedCount: 2, ReworkTotalCount: 2, Warnings: []string{"artifact warning"}},
	}}
	betaPlans := &fakePlanLister{summaries: []plan.PlanSummary{
		{ID: "changes", Status: plan.StatusChangesRequested, LastActivityAt: activity(4 * time.Minute)},
		{ID: "stale", Status: plan.StatusInProgress, LastActivityAt: activity(10 * time.Minute)},
		{ID: "old", Status: plan.StatusInReview, LastActivityAt: activity(30 * time.Minute)},
	}}
	alphaStatus := &fakeStatusReader{records: map[string]runstatus.Record{
		"live": runtimeRecord(alpha.Repo.ID, "live", now.Add(-5*time.Minute), now.Add(-runstatus.StaleThreshold+time.Nanosecond), runstatus.Phase("implement"), &runstatus.SliceDetail{ID: "r201-runtime", Title: "Runtime"}),
	}, errors: map[string]error{}}
	betaStatus := &fakeStatusReader{records: map[string]runstatus.Record{
		"stale": runtimeRecord(beta.Repo.ID, "stale", now.Add(-7*time.Minute), now.Add(-runstatus.StaleThreshold), runstatus.Phase("verify"), nil),
	}, errors: map[string]error{}}

	listers := map[string]*fakePlanLister{alpha.Repo.ID: alphaPlans, beta.Repo.ID: betaPlans}
	readers := map[string]*fakeStatusReader{alpha.Repo.ID: alphaStatus, beta.Repo.ID: betaStatus}
	collector := Collector{
		Inventory: fakeInventory{entries: []taodata.RepoInventoryEntry{beta, broken, alpha}},
		NewPlanLister: func(entry taodata.RepoInventoryEntry) PlanLister {
			return listers[entry.Repo.ID]
		},
		NewStatusReader: func(entry taodata.RepoInventoryEntry) RuntimeStatusReader {
			return readers[entry.Repo.ID]
		},
		Now: func() time.Time { return now },
	}

	snapshot, err := collector.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !snapshot.CollectedAt.Equal(now) {
		t.Fatalf("CollectedAt = %s, want %s", snapshot.CollectedAt, now)
	}
	gotOrder := make([]string, 0, len(snapshot.Rows))
	byPlan := make(map[string]Row)
	for _, row := range snapshot.Rows {
		if row.PlanID == "" {
			gotOrder = append(gotOrder, "repo:"+row.RepositoryID)
		} else {
			gotOrder = append(gotOrder, row.PlanID)
			byPlan[row.PlanID] = row
		}
	}
	wantOrder := []string{"live", "stale", "changes", "blocked", "other", "old", "repo:repo-broken"}
	if !slices.Equal(gotOrder, wantOrder) {
		t.Fatalf("row order = %v, want %v", gotOrder, wantOrder)
	}
	if alphaPlans.calls != 1 || betaPlans.calls != 1 {
		t.Fatalf("ListPlans calls alpha/beta = %d/%d, want one each", alphaPlans.calls, betaPlans.calls)
	}
	if slices.Contains(alphaStatus.reads, "completed") {
		t.Fatalf("completed plan runtime record was read: %v", alphaStatus.reads)
	}
	if slices.Contains(alphaStatus.reads, "invalid") {
		t.Fatalf("invalid warning plan runtime record was read: %v", alphaStatus.reads)
	}

	live := byPlan["live"]
	if live.Liveness != LivenessLive || live.Phase != runstatus.Phase("implement") || live.InvocationDuration != 5*time.Minute {
		t.Fatalf("live runtime detail = %+v", live)
	}
	if live.SliceID != "r201-runtime" || live.SliceTitle != "Runtime" {
		t.Fatalf("live slice detail = %q %q", live.SliceID, live.SliceTitle)
	}
	if live.Left != 3 || live.OriginalCompletedCount != 1 || live.OriginalTotalCount != 3 || live.ReworkCompletedCount != 2 || live.ReworkTotalCount != 2 {
		t.Fatalf("live composition = %+v", live)
	}
	if !slices.Equal(live.Warnings, []string{"artifact warning"}) {
		t.Fatalf("live warnings = %v", live.Warnings)
	}
	if stale := byPlan["stale"]; stale.Liveness != LivenessStale || stale.InvocationDuration != 7*time.Minute {
		t.Fatalf("exact-boundary stale row = %+v", stale)
	}
	if _, ok := byPlan["invalid"]; ok {
		t.Fatalf("default snapshot included invalid plan: %+v", byPlan["invalid"])
	}
	warning := snapshot.Rows[len(snapshot.Rows)-1]
	if warning.Kind != RowKindRepositoryWarning || warning.Status != plan.StatusInvalid || len(warning.Warnings) != 1 {
		t.Fatalf("repository warning row = %+v", warning)
	}
}

func TestCollectorShowInvalidIncludesPlansWithoutRuntimeReads(t *testing.T) {
	entry := taodata.RepoInventoryEntry{Repo: taodata.Repo{ID: "repo", Name: "repo"}}
	broken := taodata.RepoInventoryEntry{Repo: taodata.Repo{ID: "broken", Name: "broken"}, MetadataError: errors.New("repository metadata is invalid")}
	lister := &fakePlanLister{summaries: []plan.PlanSummary{{ID: "invalid", Status: plan.StatusInvalid, Warnings: []string{"invalid state.json"}}}}
	reader := &fakeStatusReader{records: map[string]runstatus.Record{}, errors: map[string]error{}}
	collector := Collector{
		Inventory:       fakeInventory{entries: []taodata.RepoInventoryEntry{entry, broken}},
		NewPlanLister:   func(taodata.RepoInventoryEntry) PlanLister { return lister },
		NewStatusReader: func(taodata.RepoInventoryEntry) RuntimeStatusReader { return reader },
		ShowInvalid:     true,
	}

	snapshot, err := collector.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Rows) != 2 {
		t.Fatalf("rows = %+v, want invalid plan and repository warning", snapshot.Rows)
	}
	var invalid, warning Row
	for _, row := range snapshot.Rows {
		switch row.Kind {
		case RowKindPlan:
			invalid = row
		case RowKindRepositoryWarning:
			warning = row
		}
	}
	if invalid.PlanID != "invalid" || !slices.Equal(invalid.Warnings, []string{"invalid state.json"}) {
		t.Fatalf("invalid plan row = %+v", invalid)
	}
	if warning.RepositoryID != "broken" {
		t.Fatalf("repository warning row = %+v", warning)
	}
	if len(reader.reads) != 0 {
		t.Fatalf("invalid plan triggered runtime reads: %v", reader.reads)
	}
}

func TestCollectorPreservesRuntimeWarningsAndDurableSliceForMissingRecord(t *testing.T) {
	now := time.Date(2026, 7, 29, 6, 0, 0, 0, time.UTC)
	entry := taodata.RepoInventoryEntry{Repo: taodata.Repo{ID: "repo", Name: "repo"}}
	lister := &fakePlanLister{summaries: []plan.PlanSummary{
		{ID: "missing", Status: plan.StatusInProgress, CurrentSliceID: "001-work", CurrentSlice: &plan.Slice{ID: "001-work", Title: "Work"}},
		{ID: "malformed", Status: plan.StatusPlanned, Warnings: []string{"artifact warning"}},
		{ID: "future", Status: plan.StatusInProgress},
	}}
	reader := &fakeStatusReader{
		records: map[string]runstatus.Record{
			"future": runtimeRecord(entry.Repo.ID, "future", now.Add(time.Minute), now, runstatus.Phase("prepare"), nil),
		},
		errors: map[string]error{"malformed": errors.New("decode runtime status: invalid json")},
	}
	collector := Collector{
		Inventory:       fakeInventory{entries: []taodata.RepoInventoryEntry{entry}},
		NewPlanLister:   func(taodata.RepoInventoryEntry) PlanLister { return lister },
		NewStatusReader: func(taodata.RepoInventoryEntry) RuntimeStatusReader { return reader },
		Now:             func() time.Time { return now },
	}

	snapshot, err := collector.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	byPlan := make(map[string]Row)
	for _, row := range snapshot.Rows {
		byPlan[row.PlanID] = row
	}
	missing := byPlan["missing"]
	if missing.Liveness != LivenessMissing || missing.SliceID != "001-work" || missing.SliceTitle != "Work" || len(missing.Warnings) != 0 {
		t.Fatalf("missing runtime row = %+v", missing)
	}
	malformed := byPlan["malformed"]
	if malformed.Liveness != LivenessMissing || !slices.Equal(malformed.Warnings, []string{"artifact warning", "decode runtime status: invalid json"}) {
		t.Fatalf("malformed runtime row = %+v", malformed)
	}
	future := byPlan["future"]
	if future.Liveness != LivenessLive || future.InvocationDuration != 0 {
		t.Fatalf("future-start runtime row = %+v", future)
	}
}

func TestPlanRowDerivesAttentionReasons(t *testing.T) {
	entry := taodata.RepoInventoryEntry{Repo: taodata.Repo{ID: "repo", Name: "repo"}}
	tests := []struct {
		name    string
		summary plan.PlanSummary
		want    AttentionReason
	}{
		{name: "blocked", summary: plan.PlanSummary{Status: plan.StatusBlocked}, want: AttentionBlocked},
		{name: "changes requested", summary: plan.PlanSummary{Status: plan.StatusChangesRequested}, want: AttentionChangesRequested},
		{name: "verification failed", summary: plan.PlanSummary{Status: plan.StatusVerificationFailed}, want: AttentionVerificationFailed},
		{name: "approval required", summary: plan.PlanSummary{Capabilities: plan.RunCapabilities{NeedsApproval: true}}, want: AttentionApprovalRequired},
		{name: "slice completion pending", summary: plan.PlanSummary{SliceCompletionPending: true}, want: AttentionSliceCompletionPending},
		{name: "rework stopped", summary: plan.PlanSummary{UnresolvedReworkStop: true}, want: AttentionReworkStopped},
		{name: "finalization failed", summary: plan.PlanSummary{FinalizationRecovery: &plan.FinalizationRecovery{Phase: plan.FinalizationFailurePhasePullRequest, Category: "publication_failed"}}, want: AttentionFinalizationFailed},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			row := planRow(entry, test.summary)
			if !slices.Equal(row.AttentionReasons, []AttentionReason{test.want}) {
				t.Fatalf("attention reasons = %v, want %v", row.AttentionReasons, test.want)
			}
		})
	}
}

func TestPlanRowProjectsVerificationRecoveryByClassification(t *testing.T) {
	tests := []struct {
		name     string
		kind     plan.FinalVerificationFailureKind
		action   plan.PlanAction
		wantNext string
	}{
		{name: "code", kind: plan.FinalVerificationFailureKindCode, action: plan.PlanAction{Kind: plan.PlanActionRepairVerification, Command: "tao run --repair-verification plan-a"}, wantNext: "REPAIR VERIFICATION"},
		{name: "tool missing", kind: plan.FinalVerificationFailureKindToolMissing, action: plan.PlanAction{Kind: plan.PlanActionResolveVerification, Instruction: "restore tool"}, wantNext: "RESOLVE VERIFICATION"},
		{name: "timeout", kind: plan.FinalVerificationFailureKindTimeout, action: plan.PlanAction{Kind: plan.PlanActionResolveVerification, Instruction: "resolve timeout"}, wantNext: "RESOLVE VERIFICATION"},
		{name: "cancelled", kind: plan.FinalVerificationFailureKindCancelled, action: plan.PlanAction{Kind: plan.PlanActionResolveVerification, Instruction: "resolve cancellation"}, wantNext: "RESOLVE VERIFICATION"},
		{name: "invalid command", kind: plan.FinalVerificationFailureKindInvalidCommand, action: plan.PlanAction{Kind: plan.PlanActionResolveVerification, Instruction: "correct command"}, wantNext: "RESOLVE VERIFICATION"},
		{name: "legacy unclassified", action: plan.PlanAction{Kind: plan.PlanActionReverify, Command: "tao run --reverify plan-a"}, wantNext: "REVERIFY"},
		{name: "pending verification repair", kind: plan.FinalVerificationFailureKindCode, action: plan.PlanAction{Kind: plan.PlanActionRun, Command: "tao run plan-a"}, wantNext: "RUN"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			summary := plan.PlanSummary{
				ID: "plan-a", Status: plan.StatusVerificationFailed,
				FinalVerificationFailureKind: test.kind,
				NextAction:                   plan.PlanNextAction{Primary: test.action},
			}
			if test.action.Kind == plan.PlanActionRun {
				summary.CurrentSliceID = "vr01-final-verification-failure"
				summary.CurrentSlice = &plan.Slice{
					ID:                 summary.CurrentSliceID,
					VerificationRepair: &plan.VerificationRepairBinding{Command: "make verify", HeadSHA: "failed-head", Fingerprint: "failure"},
				}
				summary.PendingCount = 1
			}
			row := planRow(taodata.RepoInventoryEntry{}, summary)
			if row.FinalVerificationFailureKind != test.kind || row.VerificationRecoveryAction != test.action || !slices.Contains(row.AttentionReasons, AttentionVerificationFailed) || DeriveNextAction(row) != test.wantNext {
				t.Fatalf("verification recovery row = %+v", row)
			}
		})
	}
}

func TestPlanRowProjectsClassifiedPullRequestRecoveryAction(t *testing.T) {
	summary := plan.PlanSummary{
		Status: plan.StatusReviewed,
		FinalizationRecovery: &plan.FinalizationRecovery{
			Phase: plan.FinalizationFailurePhasePullRequest, Category: "head_drift", RecoveryAction: plan.FinalizationRecoveryRestoreBoundary,
		},
		NextAction: plan.PlanNextAction{Primary: plan.PlanAction{Kind: plan.PlanActionRecoverPullRequest, Class: plan.PlanActionClassRecovery}},
	}
	row := planRow(taodata.RepoInventoryEntry{}, summary)
	if row.FinalizationRecoveryAction != plan.FinalizationRecoveryRestoreBoundary || DeriveNextAction(row) != "RESTORE BOUNDARY" {
		t.Fatalf("classified recovery row = %+v", row)
	}
}

func TestPlanRowProjectsDirtyWorktreeRecovery(t *testing.T) {
	summary := plan.PlanSummary{
		Status: plan.StatusReviewed,
		FinalizationRecovery: &plan.FinalizationRecovery{
			Phase: plan.FinalizationFailurePhasePullRequest, Category: "workspace_dirty", RecoveryAction: plan.FinalizationRecoveryRestoreBoundary,
		},
		NextAction: plan.PlanNextAction{Primary: plan.PlanAction{Kind: plan.PlanActionRecoverPullRequest, Class: plan.PlanActionClassRecovery, Instruction: "Restore a clean plan worktree"}},
	}
	row := planRow(taodata.RepoInventoryEntry{}, summary)
	if row.FinalizationCategory != "workspace_dirty" || row.FinalizationRecoveryAction != plan.FinalizationRecoveryRestoreBoundary || DeriveNextAction(row) != "RESTORE BOUNDARY" {
		t.Fatalf("dirty worktree recovery row = %+v", row)
	}
}

func TestPlanRowProjectsWorkspaceMismatchRepair(t *testing.T) {
	summary := plan.PlanSummary{
		Status: plan.StatusReviewed,
		FinalizationRecovery: &plan.FinalizationRecovery{
			Phase: plan.FinalizationFailurePhasePullRequest, Category: "workspace_mismatch", RecoveryAction: plan.FinalizationRecoveryRestoreBoundary,
		},
		NextAction: plan.PlanNextAction{Primary: plan.PlanAction{Kind: plan.PlanActionRecoverPullRequest, Class: plan.PlanActionClassRecovery, Instruction: "Repair or restore the recorded linked worktree"}},
	}
	row := planRow(taodata.RepoInventoryEntry{}, summary)
	if row.FinalizationCategory != "workspace_mismatch" || row.FinalizationRecoveryAction != plan.FinalizationRecoveryRestoreBoundary || DeriveNextAction(row) != "REPAIR WORKTREE" {
		t.Fatalf("workspace mismatch recovery row = %+v", row)
	}
}

func TestPlanRowProjectsIdentityMismatchRepair(t *testing.T) {
	summary := plan.PlanSummary{
		Status: plan.StatusReviewed,
		FinalizationRecovery: &plan.FinalizationRecovery{
			Phase: plan.FinalizationFailurePhasePullRequest, Category: "identity_mismatch", RecoveryAction: plan.FinalizationRecoveryRepairIdentity,
		},
		NextAction: plan.PlanNextAction{Primary: plan.PlanAction{Kind: plan.PlanActionRecoverPullRequest, Class: plan.PlanActionClassRecovery, Instruction: "Repair or clear stale recorded pull-request identity without adopting remote ownership"}},
	}
	row := planRow(taodata.RepoInventoryEntry{}, summary)
	if row.FinalizationCategory != "identity_mismatch" || row.FinalizationRecoveryAction != plan.FinalizationRecoveryRepairIdentity || DeriveNextAction(row) != "REPAIR PR IDENTITY" {
		t.Fatalf("identity mismatch recovery row = %+v", row)
	}
}

func TestPlanRowProjectsPostCorrectionRecoveryActions(t *testing.T) {
	tests := []struct {
		name     string
		category string
		action   string
		want     string
	}{
		{name: "head drift", category: "head_drift", action: plan.FinalizationRecoveryRestoreBoundary, want: "RESTORE BOUNDARY"},
		{name: "workspace mismatch", category: "workspace_mismatch", action: plan.FinalizationRecoveryRestoreBoundary, want: "REPAIR WORKTREE"},
		{name: "dirty worktree", category: "workspace_dirty", action: plan.FinalizationRecoveryRestoreBoundary, want: "RESTORE BOUNDARY"},
		{name: "intent mismatch", category: "intent_mismatch", action: plan.FinalizationRecoveryRepairIntent, want: "REPAIR INTENT"},
		{name: "invalid proposal", category: "proposal_invalid", action: plan.FinalizationRecoveryRerunReview, want: "FRESH REVIEW"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			summary := plan.PlanSummary{
				Status: plan.StatusReviewed,
				FinalizationRecovery: &plan.FinalizationRecovery{
					Phase: plan.FinalizationFailurePhaseProposalRepair, Category: test.category, RecoveryAction: test.action,
				},
				NextAction: plan.PlanNextAction{Primary: plan.PlanAction{Kind: plan.PlanActionRecoverPullRequest, Class: plan.PlanActionClassRecovery}},
			}
			row := planRow(taodata.RepoInventoryEntry{}, summary)
			if row.FinalizationCategory != test.category || row.FinalizationRecoveryAction != test.action || DeriveNextAction(row) != test.want {
				t.Fatalf("post-correction recovery row = %+v, want %q", row, test.want)
			}
		})
	}
}

func TestPlanRowProjectsFailedProposalCorrectionForLegacyEmptyReviewBase(t *testing.T) {
	failedAt := time.Date(2026, 8, 30, 1, 0, 0, 0, time.UTC)
	detail := &plan.PlanDetail{
		State: plan.State{
			Status: plan.StatusReviewed,
			Repo:   plan.Repo{BaseCommit: "plan-base"},
			Plan: plan.PlanState{
				ID: "plan-a", CompletedSlices: []string{"001-a"},
				FinalizationFailure: &plan.FinalizationFailure{
					Phase: plan.FinalizationFailurePhaseProposalRepair, Category: "proposal_invalid", ReviewBase: "plan-base", ReviewHead: "head123",
					FailedAt: failedAt, RecoveryAction: "rerun_review",
				},
			},
		},
		Slices: plan.SlicesFile{Slices: []plan.Slice{{ID: "001-a", Status: plan.StatusCompleted}}},
	}
	plan.SetPersistedReview(detail, plan.PlanReview{Status: plan.ReviewStatusCompleted, Verdict: plan.ReviewVerdictApprove, Head: "head123"})

	row := planRow(taodata.RepoInventoryEntry{}, plan.Summarize(detail, time.Time{}))
	if !slices.Contains(row.AttentionReasons, AttentionFinalizationFailed) {
		t.Fatalf("legacy proposal failure attention = %v", row.AttentionReasons)
	}
	if row.FinalizationPhase != plan.FinalizationFailurePhaseProposalRepair || row.RecommendedAction.Command != "tao review --run plan-a" {
		t.Fatalf("legacy proposal failure row = %+v", row)
	}
	if action := DeriveNextAction(row); action != "FRESH REVIEW" {
		t.Fatalf("legacy proposal failure monitor action = %q", action)
	}
}

func TestPlanRowCarriesBoundedOverviewWithoutAliasingSummary(t *testing.T) {
	entry := taodata.RepoInventoryEntry{Repo: taodata.Repo{ID: "repo", Name: "repo"}}
	summary := plan.PlanSummary{Overview: plan.DecisionOverview{
		Source:          plan.DecisionOverviewSourceStructured,
		Problem:         "Make decisions visible",
		SuccessCriteria: []string{"bounded output"},
		Priority:        &plan.Priority{Impact: plan.PriorityLevelHigh},
		Sequence:        &plan.Sequence{Position: 1, Total: 2, Relationships: []plan.PlanRelation{{PlanID: "next", Type: plan.PlanRelationBefore}}},
	}}
	row := planRow(entry, summary)
	if row.Overview.Source != plan.DecisionOverviewSourceStructured || row.Overview.Problem != "Make decisions visible" || row.Overview.Priority == nil || row.Overview.Sequence == nil {
		t.Fatalf("monitor overview = %+v", row.Overview)
	}

	row.Overview.SuccessCriteria[0] = "changed"
	row.Overview.Priority.Impact = plan.PriorityLevelLow
	row.Overview.Sequence.Relationships[0].PlanID = "changed"
	if summary.Overview.SuccessCriteria[0] != "bounded output" || summary.Overview.Priority.Impact != plan.PriorityLevelHigh || summary.Overview.Sequence.Relationships[0].PlanID != "next" {
		t.Fatalf("monitor row aliases summary overview: summary=%+v row=%+v", summary.Overview, row.Overview)
	}
}

func TestPlanRowPreservesUnrankedLegacyOverview(t *testing.T) {
	row := planRow(taodata.RepoInventoryEntry{}, plan.PlanSummary{Overview: plan.DecisionOverview{
		Source:  plan.DecisionOverviewSourcePlanningBrief,
		Problem: "Legacy goal",
	}})
	if row.Overview.Priority != nil || row.Overview.Sequence != nil || row.Overview.Disposition != "" {
		t.Fatalf("legacy monitor row inferred rank: %+v", row.Overview)
	}
}

func TestDeriveNextActionIsPureAndExhaustive(t *testing.T) {
	tests := []struct {
		name string
		row  Row
		want string
	}{
		{name: "repository warning", row: Row{Kind: RowKindRepositoryWarning}, want: "INSPECT"},
		{name: "completed", row: Row{Status: plan.StatusCompleted}, want: "DONE"},
		{name: "completed with PR recovery", row: Row{Status: plan.StatusCompleted, AttentionReasons: []AttentionReason{AttentionFinalizationFailed}, FinalizationPhase: plan.FinalizationFailurePhasePullRequest}, want: "FINALIZE PR"},
		{name: "live", row: Row{Liveness: LivenessLive}, want: "MONITOR"},
		{name: "stalled owned", row: Row{Liveness: LivenessStale, RunLockPresent: true, RunLockProcessAlive: true}, want: "MONITOR"},
		{name: "approval", row: Row{AttentionReasons: []AttentionReason{AttentionApprovalRequired}}, want: "APPROVE"},
		{name: "blocked", row: Row{Status: plan.StatusBlocked}, want: "CONTINUE"},
		{name: "changes", row: Row{Status: plan.StatusChangesRequested}, want: "REWORK"},
		{name: "code verification failure", row: Row{Status: plan.StatusVerificationFailed, AttentionReasons: []AttentionReason{AttentionVerificationFailed}, VerificationRecoveryAction: plan.PlanAction{Kind: plan.PlanActionRepairVerification}}, want: "REPAIR VERIFICATION"},
		{name: "external verification failure", row: Row{Status: plan.StatusVerificationFailed, AttentionReasons: []AttentionReason{AttentionVerificationFailed}, VerificationRecoveryAction: plan.PlanAction{Kind: plan.PlanActionResolveVerification}}, want: "RESOLVE VERIFICATION"},
		{name: "legacy verification failure", row: Row{Status: plan.StatusVerificationFailed, AttentionReasons: []AttentionReason{AttentionVerificationFailed}, VerificationRecoveryAction: plan.PlanAction{Kind: plan.PlanActionReverify}}, want: "REVERIFY"},
		{name: "pending verification repair", row: Row{PlanID: "plan-a", Status: plan.StatusVerificationFailed, AttentionReasons: []AttentionReason{AttentionVerificationFailed}, VerificationRecoveryAction: plan.PlanAction{Kind: plan.PlanActionRun, Command: "tao run plan-a"}}, want: "RUN"},
		{name: "unrecognized verification run", row: Row{PlanID: "plan-a", Status: plan.StatusVerificationFailed, AttentionReasons: []AttentionReason{AttentionVerificationFailed}, VerificationRecoveryAction: plan.PlanAction{Kind: plan.PlanActionRun, Command: "tao run --continue plan-a"}}, want: "RESOLVE VERIFICATION"},
		{name: "review", row: Row{Status: plan.StatusInReview}, want: "REVIEW"},
		{name: "reviewed", row: Row{Status: plan.StatusReviewed}, want: "MERGE"},
		{name: "pending intent with approval", row: Row{Status: plan.StatusReviewed, RecommendedAction: plan.PlanAction{Kind: plan.PlanActionRecoverPullRequest}}, want: "FINALIZE PR"},
		{name: "pending intent after comment", row: Row{Status: plan.StatusReviewed, RecommendedAction: plan.PlanAction{Kind: plan.PlanActionReview, Class: plan.PlanActionClassRecovery}}, want: "FRESH REVIEW"},
		{name: "pending intent after changes", row: Row{Status: plan.StatusChangesRequested, RecommendedAction: plan.PlanAction{Kind: plan.PlanActionRework}}, want: "REWORK"},
		{name: "stopped rework with pending intent", row: Row{Status: plan.StatusChangesRequested, RecommendedAction: plan.PlanAction{Kind: plan.PlanActionRestartRework}}, want: "RESTART REWORK"},
		{name: "legacy proposal recovery outranks reviewed", row: Row{Status: plan.StatusReviewed, AttentionReasons: []AttentionReason{AttentionFinalizationFailed}, FinalizationPhase: plan.FinalizationFailurePhaseProposalRepair}, want: "REPAIR PROPOSAL"},
		{name: "proposal head drift restores boundary", row: Row{Status: plan.StatusReviewed, AttentionReasons: []AttentionReason{AttentionFinalizationFailed}, FinalizationPhase: plan.FinalizationFailurePhaseProposalRepair, FinalizationRecoveryAction: plan.FinalizationRecoveryRestoreBoundary}, want: "RESTORE BOUNDARY"},
		{name: "proposal intent mismatch repairs intent", row: Row{Status: plan.StatusReviewed, AttentionReasons: []AttentionReason{AttentionFinalizationFailed}, FinalizationPhase: plan.FinalizationFailurePhaseProposalRepair, FinalizationRecoveryAction: plan.FinalizationRecoveryRepairIntent}, want: "REPAIR INTENT"},
		{name: "proposal replacement requests fresh review", row: Row{Status: plan.StatusReviewed, AttentionReasons: []AttentionReason{AttentionFinalizationFailed}, FinalizationPhase: plan.FinalizationFailurePhaseProposalRepair, FinalizationRecoveryAction: plan.FinalizationRecoveryRerunReview}, want: "FRESH REVIEW"},
		{name: "PR recovery outranks reviewed", row: Row{Status: plan.StatusReviewed, AttentionReasons: []AttentionReason{AttentionFinalizationFailed}, FinalizationPhase: plan.FinalizationFailurePhasePullRequest}, want: "FINALIZE PR"},
		{name: "head drift restores boundary", row: Row{Status: plan.StatusReviewed, AttentionReasons: []AttentionReason{AttentionFinalizationFailed}, FinalizationPhase: plan.FinalizationFailurePhasePullRequest, FinalizationRecoveryAction: plan.FinalizationRecoveryRestoreBoundary}, want: "RESTORE BOUNDARY"},
		{name: "workspace mismatch repairs worktree", row: Row{Status: plan.StatusReviewed, AttentionReasons: []AttentionReason{AttentionFinalizationFailed}, FinalizationPhase: plan.FinalizationFailurePhasePullRequest, FinalizationCategory: "workspace_mismatch", FinalizationRecoveryAction: plan.FinalizationRecoveryRestoreBoundary}, want: "REPAIR WORKTREE"},
		{name: "review mismatch requests fresh review", row: Row{Status: plan.StatusReviewed, AttentionReasons: []AttentionReason{AttentionFinalizationFailed}, FinalizationPhase: plan.FinalizationFailurePhasePullRequest, FinalizationRecoveryAction: plan.FinalizationRecoveryRerunReview}, want: "FRESH REVIEW"},
		{name: "non-approval failure with changes requests rework", row: Row{Status: plan.StatusChangesRequested, AttentionReasons: []AttentionReason{AttentionFinalizationFailed}, FinalizationPhase: plan.FinalizationFailurePhasePullRequest, FinalizationRecoveryAction: plan.FinalizationRecoveryRerunReview, RecommendedAction: plan.PlanAction{Kind: plan.PlanActionRework}}, want: "REWORK"},
		{name: "intent mismatch requires repair", row: Row{Status: plan.StatusReviewed, AttentionReasons: []AttentionReason{AttentionFinalizationFailed}, FinalizationPhase: plan.FinalizationFailurePhasePullRequest, FinalizationRecoveryAction: plan.FinalizationRecoveryRepairIntent}, want: "REPAIR INTENT"},
		{name: "identity mismatch repairs identity", row: Row{Status: plan.StatusReviewed, AttentionReasons: []AttentionReason{AttentionFinalizationFailed}, FinalizationPhase: plan.FinalizationFailurePhasePullRequest, FinalizationRecoveryAction: plan.FinalizationRecoveryRepairIdentity}, want: "REPAIR PR IDENTITY"},
		{name: "attention", row: Row{AttentionReasons: []AttentionReason{AttentionRunCrashed}}, want: "RESOLVE"},
		{name: "ready", row: Row{Overview: plan.DecisionOverview{Disposition: plan.DecisionDispositionReady}}, want: "RUN"},
		{name: "legacy unranked", row: Row{}, want: "RUN"},
		{name: "conditional", row: Row{Overview: plan.DecisionOverview{Disposition: plan.DecisionDispositionConditional}}, want: "CHECK"},
		{name: "deferred", row: Row{Overview: plan.DecisionOverview{Disposition: plan.DecisionDispositionDeferred}}, want: "WAIT"},
		{name: "obsolete", row: Row{Overview: plan.DecisionOverview{Disposition: plan.DecisionDispositionObsolete}}, want: "SKIP"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			before := test.row
			if got := DeriveNextAction(test.row); got != test.want {
				t.Fatalf("DeriveNextAction() = %q, want %q", got, test.want)
			}
			if test.row.NextAction != before.NextAction {
				t.Fatal("DeriveNextAction mutated its input")
			}
		})
	}
}

func TestVerificationFailureHasAttentionUrgency(t *testing.T) {
	if got := urgency(Row{Status: plan.StatusVerificationFailed}); got != 2 {
		t.Fatalf("verification-failed urgency = %d, want 2", got)
	}
}

func TestResolveRelationshipsClassifiesHealthAndCycles(t *testing.T) {
	rows := []Row{
		{Kind: RowKindPlan, RepositoryID: "repo", PlanID: "done", Status: plan.StatusCompleted},
		{Kind: RowKindPlan, RepositoryID: "repo", PlanID: "open", Status: plan.StatusPlanned},
		{Kind: RowKindPlan, RepositoryID: "repo", PlanID: "health", Overview: plan.DecisionOverview{Sequence: &plan.Sequence{Relationships: []plan.PlanRelation{
			{PlanID: "done", Type: plan.PlanRelationAfter},
			{PlanID: "open", Type: plan.PlanRelationAfter},
			{PlanID: "missing", Type: plan.PlanRelationAfter},
			{PlanID: "open", Type: plan.PlanRelationBefore},
		}}}},
		{Kind: RowKindPlan, RepositoryID: "repo", PlanID: "cycle-a", Overview: plan.DecisionOverview{Sequence: &plan.Sequence{Relationships: []plan.PlanRelation{{PlanID: "cycle-b", Type: plan.PlanRelationAfter}}}}},
		{Kind: RowKindPlan, RepositoryID: "repo", PlanID: "cycle-b", Overview: plan.DecisionOverview{Sequence: &plan.Sequence{Relationships: []plan.PlanRelation{{PlanID: "cycle-a", Type: plan.PlanRelationAfter}}}}},
		{Kind: RowKindPlan, RepositoryID: "other", PlanID: "missing", Status: plan.StatusCompleted},
	}
	resolveRelationships(rows, rows)
	got := rows[2].Relationships
	want := []RelationshipState{RelationshipComplete, RelationshipDuplicate, RelationshipMissing, RelationshipDuplicate}
	if len(got) != len(want) {
		t.Fatalf("relationships = %+v", got)
	}
	for index := range want {
		if got[index].State != want[index] {
			t.Errorf("relationship %d state = %q, want %q", index, got[index].State, want[index])
		}
	}
	if rows[3].Relationships[0].State != RelationshipCyclic || rows[4].Relationships[0].State != RelationshipCyclic {
		t.Fatalf("cycle health = %+v / %+v", rows[3].Relationships, rows[4].Relationships)
	}
	if len(rows[2].RelationshipWarnings) != 3 || len(rows[3].RelationshipWarnings) != 1 || len(rows[4].RelationshipWarnings) != 1 {
		t.Fatalf("relationship warnings = %v / %v / %v", rows[2].RelationshipWarnings, rows[3].RelationshipWarnings, rows[4].RelationshipWarnings)
	}
}

func TestPlanRowCarriesActionMetadataFromInventoryAndCapabilities(t *testing.T) {
	entry := taodata.RepoInventoryEntry{
		Repo:     taodata.Repo{ID: "repo", Name: "repo", Root: "/repos/repo"},
		PlansDir: "/data/repo/plans",
	}
	row := planRow(entry, plan.PlanSummary{
		ID:     "plan-a",
		Status: plan.StatusPlanned,
		Capabilities: plan.RunCapabilities{
			NeedsApproval:   true,
			ApprovalSliceID: "003-gate",
			ApprovalReason:  "owner sign-off",
		},
	})
	if row.RepositoryRoot != "/repos/repo" || row.PlanDir != "/data/repo/plans/plan-a" {
		t.Fatalf("action paths = repo %q plan %q", row.RepositoryRoot, row.PlanDir)
	}
	if row.ApprovalSliceID != "003-gate" || row.ApprovalReason != "owner sign-off" {
		t.Fatalf("approval metadata = %q %q", row.ApprovalSliceID, row.ApprovalReason)
	}
}

func TestCollectorCompletedWindowIsOptInWithActivityFallback(t *testing.T) {
	now := time.Date(2026, 8, 10, 15, 0, 0, 0, time.UTC)
	recent := now.Add(-30 * time.Minute)
	old := now.Add(-2 * time.Hour)
	fallback := now.Add(-45 * time.Minute)
	entry := taodata.RepoInventoryEntry{Repo: taodata.Repo{ID: "repo", Name: "repo"}, PlansDir: "/data/repo/plans"}
	lister := &fakePlanLister{summaries: []plan.PlanSummary{
		{ID: "active", Status: plan.StatusPlanned, Overview: plan.DecisionOverview{Sequence: &plan.Sequence{Relationships: []plan.PlanRelation{{PlanID: "old", Type: plan.PlanRelationAfter}}}}},
		{ID: "recent", Status: plan.StatusCompleted, CompletedAt: &recent, LastActivityAt: &old},
		{ID: "fallback", Status: plan.StatusCompleted, LastActivityAt: &fallback},
		{ID: "old", Status: plan.StatusCompleted, CompletedAt: &old},
		{ID: "unknown", Status: plan.StatusCompleted},
	}}
	reader := &fakeStatusReader{}
	collector := Collector{
		Inventory:       fakeInventory{entries: []taodata.RepoInventoryEntry{entry}},
		NewPlanLister:   func(taodata.RepoInventoryEntry) PlanLister { return lister },
		NewStatusReader: func(taodata.RepoInventoryEntry) RuntimeStatusReader { return reader },
		ReadRunLock:     func(string) (plan.RunLock, error) { return plan.RunLock{}, os.ErrNotExist },
		Now:             func() time.Time { return now },
	}

	defaultSnapshot, err := collector.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := planIDs(defaultSnapshot.Rows); !slices.Equal(got, []string{"active"}) {
		t.Fatalf("default plan ids = %v, want only active", got)
	}
	active := rowsByPlan(defaultSnapshot.Rows)["active"]
	if len(active.Relationships) != 1 || active.Relationships[0].State != RelationshipComplete || len(active.RelationshipWarnings) != 0 {
		t.Fatalf("relationship to completed plan outside window = %+v, warnings %v", active.Relationships, active.RelationshipWarnings)
	}

	collector.IncludeCompletedWithin = time.Hour
	windowSnapshot, err := collector.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := planIDs(windowSnapshot.Rows); !slices.Equal(got, []string{"fallback", "recent", "active"}) {
		t.Fatalf("window plan ids = %v, want recent completed plans plus active", got)
	}
}

func TestCollectorIncludesLegacyCompletedPlanWithPendingPullRequestIntent(t *testing.T) {
	now := time.Date(2026, 8, 31, 18, 0, 0, 0, time.UTC)
	detail := &plan.PlanDetail{
		State: plan.State{
			Status: plan.StatusCompleted,
			Plan: plan.PlanState{
				ID: "legacy-completed", CompletedSlices: []string{"001-work"},
				PullRequestIntent: &plan.PullRequest{Branch: "fix/legacy", HeadSHA: "head123"},
			},
			Workspace: &plan.Workspace{Path: "/worktrees/legacy", Branch: "fix/legacy", HeadSHA: "head123"},
		},
		Slices: plan.SlicesFile{Slices: []plan.Slice{{ID: "001-work", Status: plan.StatusCompleted}}},
	}
	plan.SetPersistedReview(detail, plan.PlanReview{Status: plan.ReviewStatusCompleted, Verdict: plan.ReviewVerdictApprove, Head: "head123"})
	summary := plan.Summarize(detail, now)
	if summary.Status != plan.StatusCompleted || summary.FinalizationRecovery != nil {
		t.Fatalf("legacy summary status = %q, recovery = %+v", summary.Status, summary.FinalizationRecovery)
	}
	if action := summary.NextAction.Primary; action.Kind != plan.PlanActionRecoverPullRequest || action.Class != plan.PlanActionClassRecovery {
		t.Fatalf("legacy summary action = %+v, want pull-request recovery", action)
	}

	entry := taodata.RepoInventoryEntry{Repo: taodata.Repo{ID: "repo", Name: "repo"}, PlansDir: "/data/repo/plans"}
	lister := &fakePlanLister{summaries: []plan.PlanSummary{summary}}
	collector := Collector{
		Inventory:       fakeInventory{entries: []taodata.RepoInventoryEntry{entry}},
		NewPlanLister:   func(taodata.RepoInventoryEntry) PlanLister { return lister },
		NewStatusReader: func(taodata.RepoInventoryEntry) RuntimeStatusReader { return &fakeStatusReader{} },
		ReadRunLock:     func(string) (plan.RunLock, error) { return plan.RunLock{}, os.ErrNotExist },
		Now:             func() time.Time { return now },
	}

	snapshot, err := collector.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Rows) != 1 {
		t.Fatalf("rows = %+v, want intent-only pull-request recovery", snapshot.Rows)
	}
	row := snapshot.Rows[0]
	if row.Status != plan.StatusCompleted || slices.Contains(row.AttentionReasons, AttentionFinalizationFailed) || row.NextAction != "FINALIZE PR" {
		t.Fatalf("legacy intent-only row = %+v", row)
	}
}

func TestCollectorIncludesLegacyCompletedPlanWithCurrentFinalizationFailure(t *testing.T) {
	now := time.Date(2026, 8, 31, 18, 0, 0, 0, time.UTC)
	detail := &plan.PlanDetail{
		State: plan.State{
			Status: plan.StatusCompleted,
			Plan: plan.PlanState{
				ID: "legacy-completed", CompletedSlices: []string{"001-work"},
				FinalizationFailure: &plan.FinalizationFailure{
					Phase: plan.FinalizationFailurePhasePullRequest, Category: "publication_failed", Branch: "fix/legacy", HeadSHA: "head123",
					FailedAt: now.Add(-time.Minute), RecoveryAction: plan.FinalizationRecoveryResumePullRequest,
				},
			},
			Workspace: &plan.Workspace{Branch: "fix/legacy", HeadSHA: "head123"},
		},
		Slices: plan.SlicesFile{Slices: []plan.Slice{{ID: "001-work", Status: plan.StatusCompleted}}},
	}
	plan.SetPersistedReview(detail, plan.PlanReview{Status: plan.ReviewStatusCompleted, Verdict: plan.ReviewVerdictApprove, Head: "head123"})
	summary := plan.Summarize(detail, now)
	if summary.Status != plan.StatusCompleted || summary.FinalizationRecovery == nil {
		t.Fatalf("legacy summary status = %q, recovery = %+v", summary.Status, summary.FinalizationRecovery)
	}

	entry := taodata.RepoInventoryEntry{Repo: taodata.Repo{ID: "repo", Name: "repo"}, PlansDir: "/data/repo/plans"}
	lister := &fakePlanLister{summaries: []plan.PlanSummary{summary}}
	collector := Collector{
		Inventory:       fakeInventory{entries: []taodata.RepoInventoryEntry{entry}},
		NewPlanLister:   func(taodata.RepoInventoryEntry) PlanLister { return lister },
		NewStatusReader: func(taodata.RepoInventoryEntry) RuntimeStatusReader { return &fakeStatusReader{} },
		ReadRunLock:     func(string) (plan.RunLock, error) { return plan.RunLock{}, os.ErrNotExist },
		Now:             func() time.Time { return now },
	}

	snapshot, err := collector.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Rows) != 1 {
		t.Fatalf("rows = %+v, want current legacy finalization recovery", snapshot.Rows)
	}
	row := snapshot.Rows[0]
	if row.Status != plan.StatusCompleted || !slices.Contains(row.AttentionReasons, AttentionFinalizationFailed) || row.NextAction != "FINALIZE PR" {
		t.Fatalf("legacy finalization row = %+v", row)
	}
}

func TestCollectorMarksDeadLockedStaleOrMissingRunsCrashed(t *testing.T) {
	now := time.Date(2026, 8, 10, 16, 0, 0, 0, time.UTC)
	entry := taodata.RepoInventoryEntry{Repo: taodata.Repo{ID: "repo", Name: "repo"}, PlansDir: "/data/repo/plans"}
	lister := &fakePlanLister{summaries: []plan.PlanSummary{
		{ID: "missing", Status: plan.StatusInProgress},
		{ID: "stale", Status: plan.StatusInProgress},
		{ID: "alive", Status: plan.StatusInProgress},
		{ID: "no-lock", Status: plan.StatusInProgress},
		{ID: "live", Status: plan.StatusInProgress},
	}}
	status := &fakeStatusReader{records: map[string]runstatus.Record{
		"stale":   runtimeRecord("repo", "stale", now.Add(-time.Minute), now.Add(-runstatus.StaleThreshold), "run", nil),
		"alive":   runtimeRecord("repo", "alive", now.Add(-time.Minute), now.Add(-runstatus.StaleThreshold), "run", nil),
		"no-lock": runtimeRecord("repo", "no-lock", now.Add(-time.Minute), now.Add(-runstatus.StaleThreshold), "run", nil),
		"live":    runtimeRecord("repo", "live", now.Add(-time.Minute), now, "run", nil),
	}}
	var lockReads []string
	collector := Collector{
		Inventory:       fakeInventory{entries: []taodata.RepoInventoryEntry{entry}},
		NewPlanLister:   func(taodata.RepoInventoryEntry) PlanLister { return lister },
		NewStatusReader: func(taodata.RepoInventoryEntry) RuntimeStatusReader { return status },
		ReadRunLock: func(planDir string) (plan.RunLock, error) {
			lockReads = append(lockReads, planDir)
			if planDir == "/data/repo/plans/no-lock" {
				return plan.RunLock{}, os.ErrNotExist
			}
			return plan.RunLock{PID: 999999, ProcessAlive: planDir == "/data/repo/plans/alive"}, nil
		},
		Now: func() time.Time { return now },
	}

	snapshot, err := collector.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	byPlan := rowsByPlan(snapshot.Rows)
	for _, id := range []string{"missing", "stale"} {
		if !slices.Contains(byPlan[id].AttentionReasons, AttentionRunCrashed) {
			t.Errorf("%s attention reasons = %v, want run_crashed", id, byPlan[id].AttentionReasons)
		}
		if !byPlan[id].RunLockPresent || byPlan[id].RunLockProcessAlive {
			t.Errorf("%s run lock observation = present %t alive %t, want present dead", id, byPlan[id].RunLockPresent, byPlan[id].RunLockProcessAlive)
		}
	}
	if alive := byPlan["alive"]; !alive.RunLockPresent || !alive.RunLockProcessAlive {
		t.Errorf("alive run lock observation = present %t alive %t, want present live", alive.RunLockPresent, alive.RunLockProcessAlive)
	}
	for _, id := range []string{"alive", "no-lock", "live"} {
		if slices.Contains(byPlan[id].AttentionReasons, AttentionRunCrashed) {
			t.Fatalf("%s plan marked crashed: %+v", id, byPlan[id])
		}
	}
	if noLock := byPlan["no-lock"]; noLock.RunLockPresent || noLock.RunLockProcessAlive {
		t.Errorf("missing run lock observation = present %t alive %t, want both false", noLock.RunLockPresent, noLock.RunLockProcessAlive)
	}
	if want := []string{"/data/repo/plans/missing", "/data/repo/plans/stale", "/data/repo/plans/alive", "/data/repo/plans/no-lock"}; !slices.Equal(lockReads, want) {
		t.Fatalf("run lock reads = %v, want %v", lockReads, want)
	}
}

func planIDs(rows []Row) []string {
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		if row.PlanID != "" {
			ids = append(ids, row.PlanID)
		}
	}
	return ids
}

func rowsByPlan(rows []Row) map[string]Row {
	result := make(map[string]Row, len(rows))
	for _, row := range rows {
		result[row.PlanID] = row
	}
	return result
}

func runtimeRecord(repoID, planID string, startedAt, heartbeatAt time.Time, phase runstatus.Phase, detail *runstatus.SliceDetail) runstatus.Record {
	return runstatus.Record{
		Schema:              runstatus.Schema,
		RepoID:              repoID,
		PlanID:              planID,
		Phase:               phase,
		Slice:               detail,
		InvocationStartedAt: startedAt,
		HeartbeatAt:         heartbeatAt,
	}
}
