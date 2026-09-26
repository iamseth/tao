package run

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/iamseth/tao/internal/plan"
)

// Exercise the actual caller strategies, including durable file-backed failure
// settlement, rather than two hand-built copies of the shared strategy.
func TestReviewProposalCorrectionFailureParity(t *testing.T) {
	const initial = "```tao-review-json\n{\"verdict\":\"approve\",\"summary\":\"Approved exact range.\",\"findings\":[],\"commit_message\":\"malformed\"}\n```"
	const valid = "```tao-review-proposal-json\n{\"commit_message\":{\"subject\":\"fix(review): preserve correction evidence\",\"body\":\"What:\\nPreserve exact review evidence.\\n\\nWhy:\\nKeep correction recovery bounded.\"}}\n```"
	for _, tt := range []struct {
		name, output, category string
		recordingFails         bool
		sessionErr             error
	}{
		{name: "invalid proposal", output: "invalid", category: "proposal_invalid"},
		{name: "recording failed", output: valid, category: "proposal_recording_failed", recordingFails: true},
		{name: "session failed", category: "proposal_correction_failed", sessionErr: errors.New("session failed")},
	} {
		t.Run(tt.name, func(t *testing.T) {
			type projection struct {
				Category       string
				Phase          plan.FinalizationFailurePhase
				RecoveryAction string
			}
			want := projection{tt.category, plan.FinalizationFailurePhaseProposalRepair, plan.FinalizationRecoveryRerunReview}
			var historical projection
			for _, arm := range []string{"historical", "fresh"} {
				t.Run(arm, func(t *testing.T) {
					fixture := newPullRequestOrchestrationFixture(t)
					repo := plan.NewFileRepository(fixture.plansRoot)
					detail, err := repo.ResolvePlan(context.Background(), fixture.planDir)
					if err != nil {
						t.Fatal(err)
					}
					recordingCalls := 0
					factory := func(detail *plan.PlanDetail) (PlanMutationRecord, error) {
						record, err := repo.PlanRecord(detail)
						if err != nil {
							return nil, err
						}
						return correctionParityRecord{PlanRecord: record, recordingCalls: &recordingCalls, fail: tt.recordingFails}, nil
					}
					calls := 0
					executor := agentSessionExecutorFunc(func(_ context.Context, request AgentSessionRequest) (AgentSessionResult, error) {
						calls++
						if arm == "fresh" && calls == 1 {
							return AgentSessionResult{Output: initial}, nil
						}
						if !request.CaptureOutput || !strings.Contains(request.Prompt, "COMMIT PROPOSAL CORRECTION mode") || request.Metrics == nil || request.Metrics.Role != plan.AgentRoleReview {
							t.Fatalf("correction request = %#v", request)
						}
						persisted, err := plan.ReadState(fixture.planDir)
						if err != nil {
							t.Fatal(err)
						}
						if failure := persisted.Plan.FinalizationFailure; failure == nil || failure.Category != "proposal_correction_started" {
							t.Fatalf("attempt not consumed before session: %#v", failure)
						}
						return AgentSessionResult{Output: tt.output}, tt.sessionErr
					})
					failedAt := time.Date(2026, 9, 26, 1, 0, 0, 0, time.UTC)
					clock := func() time.Time { return failedAt }
					if arm == "historical" {
						reviewer := struct {
							ReviewCreator
							AgentSessionExecutor
						}{AgentSessionExecutor: executor}
						finalizer := newFinalizer(io.Discard, testRunExecution(ExecutionConfig{}, RunDependencies{CommandRunner: defaultCommandRunner, PlanRecordFactory: factory, ReviewCreator: reviewer, Now: clock}))
						err = finalizer.ensureApprovedReviewProposal(context.Background(), detail, fixture.worktreeRoot, fixture.branch, fixture.head)
					} else {
						var review plan.PlanReview
						review, err = createReviewWithAgentSession(context.Background(), executor, agentOperationOptions{Agent: "pi", CommandRunner: defaultCommandRunner, StartingBranch: fixture.branch, Now: clock}, ReviewRun{PlanDir: fixture.planDir, Detail: detail, RepoRoot: fixture.worktreeRoot}, factory)
						if review.IsApproved() || review.Verdict != plan.ReviewVerdictComment || review.CommitMessage != nil {
							t.Fatalf("unsafe fresh review projection: %#v", review)
						}
					}
					if err == nil {
						t.Fatal("expected correction failure")
					}
					wantCalls := 1
					if arm == "fresh" {
						wantCalls++
					}
					if calls != wantCalls {
						t.Fatalf("session calls = %d, want %d", calls, wantCalls)
					}
					wantRecordingCalls := 0
					if tt.recordingFails {
						wantRecordingCalls = 1
					}
					if recordingCalls != wantRecordingCalls {
						t.Fatalf("recording calls = %d, want %d", recordingCalls, wantRecordingCalls)
					}
					persisted, err := plan.ReadState(fixture.planDir)
					if err != nil {
						t.Fatal(err)
					}
					failure := persisted.Plan.FinalizationFailure
					if failure == nil {
						t.Fatal("missing persisted failure")
					}
					got := projection{failure.Category, failure.Phase, failure.RecoveryAction}
					if got != want {
						t.Fatalf("failure projection = %#v, want %#v", got, want)
					}
					if failure.ReviewBase != fixture.base || failure.ReviewHead != fixture.head || failure.FailedAt != failedAt {
						t.Fatalf("failure evidence = %#v", failure)
					}
					if arm == "historical" {
						historical = got
					} else if got != historical {
						t.Fatalf("fresh = %#v, historical = %#v", got, historical)
					}
				})
			}
		})
	}
}

type correctionParityRecord struct {
	*plan.PlanRecord
	recordingCalls *int
	fail           bool
}

func (r correctionParityRecord) RecordReviewProposalCorrection(expected plan.FinalizationFailure, review plan.PlanReview, agent string) error {
	*r.recordingCalls++
	if r.fail {
		return errors.New("record correction failed")
	}
	return r.PlanRecord.RecordReviewProposalCorrection(expected, review, agent)
}
