package rework

import (
	"context"
	"errors"
	"fmt"
	"math"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/iamseth/tao/internal/forge"
	"github.com/iamseth/tao/internal/plan"
)

type driverRecord struct {
	detail    *plan.PlanDetail
	stopErr   error
	reopenErr error
}

func (r *driverRecord) Detail() *plan.PlanDetail { return r.detail }

func (r *driverRecord) Reopen(slices []plan.Slice, now time.Time) error {
	_, err := plan.Reopen(r.detail, slices, now)
	return err
}

func (r *driverRecord) RecordAutomaticReworkStop(evidence plan.AutomaticReworkStop) error {
	if r.stopErr != nil {
		return r.stopErr
	}
	if err := evidence.Validate(); err != nil {
		return err
	}
	r.detail.Events = append(r.detail.Events, plan.Event{
		Type: plan.EventTypeReworkStopped, Timestamp: evidence.StoppedAt, PlanID: r.detail.State.Plan.ID,
		Round: evidence.Round, Attempts: evidence.Attempts, Fingerprint: evidence.Fingerprint,
		Reason: evidence.Reason, Message: evidence.Reason,
	})
	return nil
}

func (r *driverRecord) ReopenAutomatic(newSlices []plan.Slice, evidence plan.AutomaticReworkRound) error {
	if r.reopenErr != nil {
		return r.reopenErr
	}
	if err := r.Reopen(newSlices, evidence.ReopenedAt); err != nil {
		return err
	}
	r.detail.Events = append(r.detail.Events, plan.Event{
		Type: plan.EventTypeReworkRound, Timestamp: evidence.ReopenedAt, PlanID: r.detail.State.Plan.ID,
		Round: evidence.Round, Attempts: evidence.Attempts, Fingerprint: evidence.Fingerprint,
		Message: fmt.Sprintf("Automatic rework round %d (attempt %d of %d)", evidence.Round, evidence.Attempts, evidence.MaxAttempts),
	})
	return nil
}

func (r *driverRecord) ReopenFromPullRequest(newSlices []plan.Slice, consumedThreadIDs []string, now time.Time) error {
	for _, threadID := range consumedThreadIDs {
		if slices.Contains(r.detail.State.Plan.PRFeedbackConsumedThreadIDs, threadID) {
			return fmt.Errorf("pull request feedback thread %q was already consumed", threadID)
		}
	}
	if _, err := plan.Reopen(r.detail, newSlices, now); err != nil {
		return err
	}
	r.detail.State.Plan.PRFeedbackConsumedThreadIDs = append(r.detail.State.Plan.PRFeedbackConsumedThreadIDs, consumedThreadIDs...)
	return nil
}

func TestDriverRunRejectsAbandonedPlanBeforeRestartDecisionOrExecution(t *testing.T) {
	detail := &plan.PlanDetail{
		State:  plan.State{Status: plan.StatusAbandoned, Plan: plan.PlanState{ID: "plan-a"}},
		Events: []plan.Event{{Type: plan.EventTypePlanAbandoned, Reason: "superseded by safer work"}},
	}
	executed := false
	recordBound := false
	driver := Driver{
		Resolve: fixedDriverResolver(detail),
		Record: func(*plan.PlanDetail) (AutomaticRecord, error) {
			recordBound = true
			return &driverRecord{detail: detail}, nil
		},
	}

	err := driver.Run(context.Background(), "plan-a", RunOptions{
		Enabled: true, MaxAttempts: 5, AllowRestart: true,
		Execute: func(context.Context) error {
			executed = true
			return nil
		},
	})
	if err == nil || !strings.Contains(err.Error(), "plan plan-a is abandoned: superseded by safer work") {
		t.Fatalf("Run error = %v", err)
	}
	if executed || recordBound {
		t.Fatalf("abandoned automatic rework had side effects: executed=%v record=%v", executed, recordBound)
	}
}

func TestDriverDecideReturnsZeroForNonActionablePlan(t *testing.T) {
	driver := Driver{Resolve: func(context.Context, string) (*plan.PlanDetail, error) {
		return &plan.PlanDetail{State: plan.State{Status: plan.StatusReviewed}}, nil
	}}

	got, err := driver.Decide(context.Background(), "plan", 0, 0, "", 5, plan.AgentBudgetThresholds{})
	if err != nil {
		t.Fatalf("Decide returned error: %v", err)
	}
	if !reflect.DeepEqual(got, Decision{}) {
		t.Fatalf("Decide = %+v, want zero decision", got)
	}
}

func TestDriverDecideStopsAtCapUsingRoundBaseline(t *testing.T) {
	detail := actionableDriverDetail(5)
	driver := Driver{Resolve: fixedDriverResolver(detail), Record: fixedAutomaticRecordFactory}

	got, err := driver.Decide(context.Background(), "plan", 2, 1, "", 3, plan.AgentBudgetThresholds{})
	if err != nil {
		t.Fatalf("Decide returned error: %v", err)
	}
	if got.StopKind != StopKindCapExhausted || got.StopReason != "automatic rework cap exhausted after 3 cycles" {
		t.Fatalf("stop = (%q, %q)", got.StopKind, got.StopReason)
	}
	if got.Round != 5 {
		t.Fatalf("round = %d, want 5", got.Round)
	}
	if !reflect.DeepEqual(got.Findings, ReviewFindings(detail)) {
		t.Fatalf("blocking findings = %+v, want %+v", got.Findings, ReviewFindings(detail))
	}
}

func TestDriverDecideFailsClosedWhenStopCannotSettle(t *testing.T) {
	detail := actionableDriverDetail(1)
	settlementErr := errors.New("injected stop settlement failure")
	driver := Driver{
		Resolve: fixedDriverResolver(detail),
		Record: func(detail *plan.PlanDetail) (AutomaticRecord, error) {
			return &driverRecord{detail: detail, stopErr: settlementErr}, nil
		},
	}

	got, err := driver.Decide(context.Background(), "plan", 0, 1, "", 1, plan.AgentBudgetThresholds{})
	if err == nil {
		t.Fatal("Decide unexpectedly published an unsettled stop")
	}
	for _, want := range []string{"automatic rework mutation failed", settlementErr.Error(), "Automatic rework stopped: attempt cap reached"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("Decide error %q does not contain %q", err, want)
		}
	}
	if !reflect.DeepEqual(got, Decision{}) {
		t.Fatalf("Decide = %+v, want zero decision", got)
	}
	if hasDriverEvent(detail.Events, plan.EventTypeReworkStopped) {
		t.Fatal("failed settlement left authoritative stop evidence")
	}
}

func TestDriverDecideFailsClosedWhenAutomaticReopenCannotSettle(t *testing.T) {
	detail := actionableDriverDetail(0)
	beforeStatus := detail.State.Status
	beforeSlices := slices.Clone(detail.Slices.Slices)
	settlementErr := errors.New("injected automatic reopen settlement failure")
	driver := Driver{
		Resolve: fixedDriverResolver(detail),
		Record: func(detail *plan.PlanDetail) (AutomaticRecord, error) {
			return &driverRecord{detail: detail, reopenErr: settlementErr}, nil
		},
	}

	got, err := driver.Decide(context.Background(), "plan", 0, 0, "", 5, plan.AgentBudgetThresholds{})
	if err == nil {
		t.Fatal("Decide unexpectedly published an unsettled rework round")
	}
	for _, want := range []string{"automatic rework mutation failed", "record automatic rework round", settlementErr.Error()} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("Decide error %q does not contain %q", err, want)
		}
	}
	if !reflect.DeepEqual(got, Decision{}) {
		t.Fatalf("Decide = %+v, want zero decision", got)
	}
	if detail.State.Status != beforeStatus || !reflect.DeepEqual(detail.Slices.Slices, beforeSlices) {
		t.Fatalf("failed settlement mutated plan detail: status=%q slices=%+v", detail.State.Status, detail.Slices.Slices)
	}
	if hasDriverEvent(detail.Events, plan.EventTypeReworkRound) {
		t.Fatal("failed settlement left authoritative round evidence")
	}
}

func TestDriverDecideStopsOnEquivalentConsecutiveFindings(t *testing.T) {
	detail := actionableDriverDetail(1)
	finding := ReviewFindings(detail)[0]
	fingerprint := ReworkFindingsFingerprint([]plan.ReviewFinding{{
		Severity:   " MAJOR ",
		File:       "./" + finding.File,
		Line:       finding.Line,
		Message:    strings.ToUpper(finding.Message),
		Suggestion: "  ",
	}})
	driver := Driver{Resolve: fixedDriverResolver(detail), Record: fixedAutomaticRecordFactory}

	got, err := driver.Decide(context.Background(), "plan", 1, 0, fingerprint, 5, plan.AgentBudgetThresholds{})
	if err != nil {
		t.Fatalf("Decide returned error: %v", err)
	}
	if got.StopKind != StopKindFindingsStalled || got.StopReason != "automatic rework stalled on equivalent consecutive findings" {
		t.Fatalf("stop = (%q, %q)", got.StopKind, got.StopReason)
	}
	if got.Fingerprint != fingerprint {
		t.Fatalf("fingerprint = %q, want %q", got.Fingerprint, fingerprint)
	}
	if !reflect.DeepEqual(got.Findings, ReviewFindings(detail)) {
		t.Fatalf("blocking findings = %+v, want %+v", got.Findings, ReviewFindings(detail))
	}
}

func TestDriverDecideContinuesForDistinctSameFileFindings(t *testing.T) {
	detail := actionableDriverDetail(1)
	detail.State.Plan.Review.Findings[0] = plan.ReviewFinding{
		Severity: "major", File: "store/file.go", Line: 42,
		Message: "Warp leaves the write transaction open", Suggestion: "close the transaction after writing",
	}
	previous := ReworkFindingsFingerprint([]plan.ReviewFinding{{
		Severity: "major", File: "store/file.go", Line: 42,
		Message: "Warp drops the recovered record", Suggestion: "retain the recovered record",
	}})
	driver := Driver{
		Resolve: fixedDriverResolver(detail),
		Record:  func(detail *plan.PlanDetail) (AutomaticRecord, error) { return &driverRecord{detail: detail}, nil },
	}

	got, err := driver.Decide(context.Background(), "plan", 1, 0, previous, 5, plan.AgentBudgetThresholds{})
	if err != nil {
		t.Fatalf("Decide returned error: %v", err)
	}
	if !got.Reworked || got.StopReason != "" || got.Fingerprint == previous {
		t.Fatalf("distinct same-file decision = %+v, want another rework round", got)
	}
}

func TestDriverDecideReopensWithRecurringFileAdvisory(t *testing.T) {
	detail, previous := recurringDriverDetail()
	beforeSlices := len(detail.Slices.Slices)
	recordCalled := false
	driver := Driver{
		Resolve: fixedDriverResolver(detail),
		Record: func(detail *plan.PlanDetail) (AutomaticRecord, error) {
			recordCalled = true
			return &driverRecord{detail: detail}, nil
		},
		Now: func() time.Time { return time.Date(2026, 7, 29, 22, 0, 0, 0, time.UTC) },
	}

	got, err := driver.Decide(context.Background(), "plan", 0, 0, previous, 5, plan.AgentBudgetThresholds{})
	if err != nil {
		t.Fatalf("Decide returned error: %v", err)
	}
	want := []Advisory{{Kind: AdvisoryKindFileRecurrence, Location: "store/file.go", Rounds: []int{1, 2, 3}}}
	if !got.Reworked || got.Round != 3 || got.StopKind != StopKindNone || got.StopReason != "" || !reflect.DeepEqual(got.Advisories, want) {
		t.Fatalf("recurring-file decision = %+v, want advisory %#v", got, want)
	}
	if got.Fingerprint == previous || len(got.RecurringFiles) != 0 || len(got.RecurringFileRounds) != 0 || len(got.Findings) != 0 {
		t.Fatalf("advisory leaked into stop evidence: %+v", got)
	}
	if !recordCalled || detail.State.Status != plan.StatusInProgress || len(detail.Slices.Slices) != beforeSlices+1 {
		t.Fatalf("advisory did not cross ordinary reopen boundary: status=%q slices=%+v", detail.State.Status, detail.Slices.Slices)
	}
	requireDriverReopenedRound(t, detail, got, 3)

}

func TestDriverDecideUsesWindowWideFileRecurrenceCaseStudies(t *testing.T) {
	tests := []struct {
		name         string
		files        []string
		wantRound    int
		wantAdvisory bool
		wantFile     string
		wantAt       []int
	}{
		{
			name: "workflow recovery continues at round seven",
			files: []string{
				"internal/run/run.go", "internal/workspace/resolve.go", "internal/run/run.go",
				"internal/workspace/resolve.go", "internal/run/run.go", "internal/run/run.go",
				"internal/workspace/resolve.go",
			},
			wantRound: 7, wantAdvisory: true, wantFile: "internal/workspace/resolve.go", wantAt: []int{2, 4, 7},
		},
		{
			name: "verification repair continues at round four after finding moves",
			files: []string{
				"internal/plan/derive.go", "internal/plan/derive.go", "internal/plan/derive.go", "internal/run/run.go",
			},
			wantRound: 4, wantAdvisory: true, wantFile: "internal/plan/derive.go", wantAt: []int{1, 2, 3},
		},
		{
			name:      "two unrelated rounds do not stop",
			files:     []string{"first.go", "second.go"},
			wantRound: 2,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			detail := actionableDriverDetail(test.wantRound)
			detail.Events = []plan.Event{{
				Type:   plan.EventTypePlanReviewed,
				Review: &plan.PlanReview{Status: plan.ReviewStatusCompleted, Verdict: plan.ReviewVerdictChangesRequested, Findings: []plan.ReviewFinding{{File: "initial.go", Message: "initial"}}, FindingsCount: 1},
			}}
			for round, file := range test.files {
				finding := plan.ReviewFinding{Severity: "major", File: file, Line: round + 1, Message: fmt.Sprintf("round %d finding", round+1)}
				detail.Events = append(detail.Events, plan.Event{
					Type: plan.EventTypePlanReviewed, SliceID: fmt.Sprintf("r%d01-finding", round+1),
					Review: &plan.PlanReview{Status: plan.ReviewStatusCompleted, Verdict: plan.ReviewVerdictChangesRequested, Findings: []plan.ReviewFinding{finding}, FindingsCount: 1},
				})
				if round == len(test.files)-1 {
					detail.State.Plan.Review.Findings = []plan.ReviewFinding{finding}
				}
			}
			driver := Driver{Resolve: fixedDriverResolver(detail), Record: fixedAutomaticRecordFactory}
			got, err := driver.Decide(context.Background(), "plan", 0, 0, "distinct-previous-fingerprint", 10, plan.AgentBudgetThresholds{})
			if err != nil {
				t.Fatalf("Decide returned error: %v", err)
			}
			if !got.Reworked || got.Round != test.wantRound+1 || got.StopKind != StopKindNone || got.StopReason != "" {
				t.Fatalf("decision = %+v, want another rework round", got)
			}
			if test.wantAdvisory {
				want := Advisory{Kind: AdvisoryKindFileRecurrence, Location: test.wantFile, Rounds: test.wantAt}
				if !slices.ContainsFunc(got.Advisories, func(a Advisory) bool { return reflect.DeepEqual(a, want) }) {
					t.Fatalf("advisories = %+v, want %+v", got.Advisories, want)
				}
			} else if len(got.Advisories) != 0 {
				t.Fatalf("unexpected advisories: %+v", got.Advisories)
			}
			requireDriverReopenedRound(t, detail, got, test.wantRound+1)
		})
	}
}

func TestDriverDecideStopsOnStaleMergeIntentPlanBudgetAtRoundThree(t *testing.T) {
	detail := actionableDriverDetail(3)
	detail.Events = []plan.Event{{
		Type:    plan.EventTypeAgentMetrics,
		Metrics: &plan.AgentMetrics{SessionID: "stale-merge-intent-recovery", ToolCalls: 516, AssistantMessages: 320},
	}}
	driver := Driver{Resolve: fixedDriverResolver(detail), Record: fixedAutomaticRecordFactory}

	got, err := driver.Decide(context.Background(), "plan", 0, 0, "distinct-previous-fingerprint", 10, plan.DefaultAgentBudgetThresholds())
	if err != nil {
		t.Fatalf("Decide returned error: %v", err)
	}
	warning := planBudgetWarning{Metric: "tool_calls", Observed: 516, Threshold: 400}
	if got.StopKind != StopKindPlanBudget || got.Round != 3 || got.StopReason != planBudgetStopReason(warning) {
		t.Fatalf("decision = %+v, want tool-call budget stop at round 3", got)
	}
	for _, want := range []string{"plan agent budget warning", "tool_calls", "observed 516", "threshold 400"} {
		if message := FormatStopMessage(got); !strings.Contains(message, want) {
			t.Errorf("stop message %q does not contain %q", message, want)
		}
	}
}

func TestDriverDecidePlanBudgetFailsOpenAndRequiresTwoSpentRounds(t *testing.T) {
	tests := []struct {
		name       string
		round      int
		metrics    *plan.AgentMetrics
		thresholds plan.AgentBudgetThresholds
	}{
		{name: "only one round spent", round: 1, metrics: &plan.AgentMetrics{SessionID: "one", ToolCalls: 516}, thresholds: plan.DefaultAgentBudgetThresholds()},
		{name: "missing metrics", round: 3, thresholds: plan.DefaultAgentBudgetThresholds()},
		{name: "malformed infinite metric", round: 3, metrics: &plan.AgentMetrics{SessionID: "bad", Cost: math.Inf(1)}, thresholds: plan.DefaultAgentBudgetThresholds()},
		{name: "slice scope warning only", round: 3, metrics: &plan.AgentMetrics{SessionID: "slice", ToolCalls: 516}, thresholds: plan.AgentBudgetThresholds{Slice: plan.AgentBudgetScopeThresholds{ToolCalls: 120}, Plan: plan.AgentBudgetScopeThresholds{ToolCalls: 1000}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			detail := actionableDriverDetail(test.round)
			if test.metrics != nil {
				detail.Events = []plan.Event{{Type: plan.EventTypeAgentMetrics, SliceID: "003-work", Metrics: test.metrics}}
			}
			driver := Driver{Resolve: fixedDriverResolver(detail), Record: fixedAutomaticRecordFactory}
			got, err := driver.Decide(context.Background(), "plan", 0, 0, "distinct-previous-fingerprint", 10, test.thresholds)
			if err != nil {
				t.Fatalf("Decide returned error: %v", err)
			}
			if !got.Reworked || got.StopKind != StopKindNone {
				t.Fatalf("decision = %+v, want fail-open rework", got)
			}
		})
	}
}

func TestDriverDecidePlanBudgetStopsDespiteFileRecurrence(t *testing.T) {
	detail, previous := recurringDriverDetail()
	detail.Events = append(detail.Events, plan.Event{Type: plan.EventTypeAgentMetrics, Metrics: &plan.AgentMetrics{SessionID: "large", ToolCalls: 516}})
	driver := Driver{Resolve: fixedDriverResolver(detail), Record: fixedAutomaticRecordFactory}

	got, err := driver.Decide(context.Background(), "plan", 0, 0, previous, 10, plan.DefaultAgentBudgetThresholds())
	if err != nil {
		t.Fatalf("Decide returned error: %v", err)
	}
	if got.StopKind != StopKindPlanBudget || len(got.Advisories) != 0 || got.Reworked {
		t.Fatalf("decision = %+v, want plan budget stop without advisories", got)
	}
}

func TestDriverDecideContinuesWithAnchorAndFileAdvisories(t *testing.T) {
	detail := actionableDriverDetail(8)
	detail.Events = []plan.Event{{
		Type:   plan.EventTypePlanReviewed,
		Review: &plan.PlanReview{Status: plan.ReviewStatusCompleted, Verdict: plan.ReviewVerdictChangesRequested, FindingsCount: 1, Findings: []plan.ReviewFinding{{File: "initial.go", Line: 1, Message: "initial"}}},
	}}
	for round := 1; round <= 8; round++ {
		finding := plan.ReviewFinding{Severity: "major", File: fmt.Sprintf("round-%d.go", round), Line: round, Message: fmt.Sprintf("round %d finding", round)}
		if round == 1 {
			finding = plan.ReviewFinding{Severity: "major", File: "internal/plan/verification_repair.go", Line: 47, Message: "the check is too strict"}
		}
		if round == 4 {
			finding = plan.ReviewFinding{Severity: "major", File: "internal/plan/verification_repair.go", Line: 48, Message: "preserve the bounded path"}
		}
		if round == 8 {
			finding = plan.ReviewFinding{Severity: "major", File: "./internal/plan/verification_repair.go", Line: 47, Message: "the counting is never capped"}
		}
		detail.Events = append(detail.Events, plan.Event{
			Type: plan.EventTypePlanReviewed, SliceID: fmt.Sprintf("r%d01-finding", round),
			Review: &plan.PlanReview{Status: plan.ReviewStatusCompleted, Verdict: plan.ReviewVerdictChangesRequested, FindingsCount: 1, Findings: []plan.ReviewFinding{finding}},
		})
		if round == 8 {
			detail.State.Plan.Review.Findings = []plan.ReviewFinding{finding}
		}
	}
	driver := Driver{Resolve: fixedDriverResolver(detail), Record: fixedAutomaticRecordFactory}

	got, err := driver.Decide(context.Background(), "plan", 0, 0, "distinct-previous-fingerprint", 10, plan.AgentBudgetThresholds{})
	if err != nil {
		t.Fatalf("Decide returned error: %v", err)
	}
	want := []Advisory{
		{Kind: AdvisoryKindAnchorRecurrence, Location: "internal/plan/verification_repair.go:47", Rounds: []int{1, 8}},
		{Kind: AdvisoryKindFileRecurrence, Location: "internal/plan/verification_repair.go", Rounds: []int{1, 4, 8}},
	}
	if !got.Reworked || got.Round != 9 || got.StopKind != StopKindNone || got.StopReason != "" || !reflect.DeepEqual(got.Advisories, want) {
		t.Fatalf("decision = %+v, want advisories %#v", got, want)
	}
	if len(got.AnchorRounds) != 0 || len(got.AnchorFindings) != 0 || FormatStopMessage(got) != "" {
		t.Fatalf("advisories leaked into stop evidence: %+v", got)
	}
	requireDriverReopenedRound(t, detail, got, 9)
}

func TestDriverDecideNullLinesDoNotFormSharedAnchor(t *testing.T) {
	detail := actionableDriverDetail(2)
	detail.Events = []plan.Event{{Type: plan.EventTypePlanReviewed, Review: &plan.PlanReview{Status: plan.ReviewStatusCompleted}}}
	for round, message := range []string{"first unanchored finding", "second unanchored finding"} {
		finding := plan.ReviewFinding{Severity: "major", File: "shared.go", Line: 0, Message: message}
		detail.Events = append(detail.Events, plan.Event{
			Type: plan.EventTypePlanReviewed, SliceID: fmt.Sprintf("r%d01-finding", round+1),
			Review: &plan.PlanReview{Status: plan.ReviewStatusCompleted, Verdict: plan.ReviewVerdictChangesRequested, FindingsCount: 1, Findings: []plan.ReviewFinding{finding}},
		})
		if round == 1 {
			detail.State.Plan.Review.Findings = []plan.ReviewFinding{finding}
		}
	}
	driver := Driver{Resolve: fixedDriverResolver(detail), Record: fixedAutomaticRecordFactory}

	got, err := driver.Decide(context.Background(), "plan", 0, 0, "distinct-previous-fingerprint", 10, plan.AgentBudgetThresholds{})
	if err != nil {
		t.Fatalf("Decide returned error: %v", err)
	}
	if !got.Reworked || got.StopKind != StopKindNone || len(got.Advisories) != 0 {
		t.Fatalf("decision = %+v, want another rework round without advisories", got)
	}
}

func TestDriverDecideLegacyReviewWithoutFindingPayloadDoesNotStop(t *testing.T) {
	detail := actionableDriverDetail(3)
	detail.Events = []plan.Event{
		{Type: plan.EventTypePlanReviewed, Review: &plan.PlanReview{Status: plan.ReviewStatusCompleted, Verdict: plan.ReviewVerdictChangesRequested, FindingsCount: 1, Findings: []plan.ReviewFinding{{File: "initial.go", Message: "initial"}}}},
		{Type: plan.EventTypePlanReviewed, SliceID: "r101-finding", Review: &plan.PlanReview{Status: plan.ReviewStatusCompleted, Verdict: plan.ReviewVerdictChangesRequested, FindingsCount: 1, Findings: []plan.ReviewFinding{{File: "shared.go", Line: 42, Message: "first"}}}},
		{Type: plan.EventTypePlanReviewed, SliceID: "r201-finding", Review: &plan.PlanReview{Status: plan.ReviewStatusCompleted, Verdict: plan.ReviewVerdictChangesRequested, FindingsCount: 1}},
		{Type: plan.EventTypePlanReviewed, SliceID: "r301-finding", Review: &plan.PlanReview{Status: plan.ReviewStatusCompleted, Verdict: plan.ReviewVerdictChangesRequested, FindingsCount: 1, Findings: []plan.ReviewFinding{{File: "shared.go", Line: 42, Message: "third"}}}},
	}
	detail.State.Plan.Review.Findings = []plan.ReviewFinding{{Severity: "major", File: "shared.go", Message: "third"}}
	driver := Driver{Resolve: fixedDriverResolver(detail), Record: fixedAutomaticRecordFactory}

	got, err := driver.Decide(context.Background(), "plan", 0, 0, "distinct-previous-fingerprint", 10, plan.AgentBudgetThresholds{})
	if err != nil {
		t.Fatalf("Decide returned error: %v", err)
	}
	if !got.Reworked || got.StopKind != StopKindNone || len(got.Advisories) != 0 {
		t.Fatalf("decision = %+v, want fail-open rework without partial legacy advisories", got)
	}
}

func TestDriverDecideUsesPullRequestReopenAsFreshLocationAdvisoryBaseline(t *testing.T) {
	detail, _ := recurringDriverDetail()
	for _, event := range detail.Events {
		for index := range event.Review.Findings {
			event.Review.Findings[index].Line = 42
		}
	}
	if got := locationAdvisories(plan.ProjectReworkChurn(detail.Events, 0)); len(got) != 2 {
		t.Fatalf("fixture needs anchor and file recurrence: %+v", got)
	}
	before := slices.Clone(detail.Events)
	detail.State.Status = plan.StatusCompleted
	detail.State.Plan.PullRequest = &plan.PullRequest{Number: 17, HeadSHA: "head123"}
	detail.State.Plan.Review = &plan.PlanReview{Status: plan.ReviewStatusCompleted, Verdict: plan.ReviewVerdictApprove, Head: "head123"}
	detail.State.Plan.PRFeedbackTriage = plan.PRFeedbackTriageResult{
		"PRRT_change": {Kind: string(PRThreadKindChange), Rationale: "Requests a distinct fix in the recurring file."},
	}
	if _, err := ReopenFromPullRequest(&driverRecord{detail: detail}, []forge.ReviewThread{{NodeID: "PRRT_change", Path: "store/file.go", Comments: []forge.ReviewThreadComment{{Body: "Address the latest transaction bug."}}}}, time.Now()); err != nil {
		t.Fatalf("pull-request reopen failed: %v", err)
	}
	if got := RoundCount(detail); got != 3 {
		t.Fatalf("pull-request round = %d, want 3", got)
	}

	detail.State.Status = plan.StatusChangesRequested
	detail.State.Plan.Review = &plan.PlanReview{
		Status:  plan.ReviewStatusCompleted,
		Verdict: plan.ReviewVerdictChangesRequested,
		Findings: []plan.ReviewFinding{{
			Severity: "major", File: "store/file.go", Message: "A distinct post-PR review finding",
		}},
	}
	driver := Driver{
		Resolve: fixedDriverResolver(detail),
		Record:  func(detail *plan.PlanDetail) (AutomaticRecord, error) { return &driverRecord{detail: detail}, nil },
	}
	decision, err := driver.Decide(context.Background(), "plan", 0, 2, "stale-pre-PR-fingerprint", 2, plan.AgentBudgetThresholds{})
	if err != nil {
		t.Fatalf("Decide returned error: %v", err)
	}
	if !decision.Reworked || decision.BaselineRound != 3 || decision.Round != 4 || len(decision.Advisories) != 0 {
		t.Fatalf("post-PR decision=%+v, want baseline 3 and round 4 reopen", decision)
	}
	if !reflect.DeepEqual(detail.Events[:len(before)], before) {
		t.Fatal("PR reset rewrote historical evidence")
	}
	requireDriverReopenedRound(t, detail, decision, 1)
}

func TestDriverLoopPersistsAttemptsFromPullRequestResetBaseline(t *testing.T) {
	decisions := 0
	persistedAttempts := -1
	driver := Driver{DecideOne: func(context.Context, string, int, int, string, int, plan.AgentBudgetThresholds) (Decision, error) {
		decisions++
		if decisions == 1 {
			return Decision{Reworked: true, BaselineRound: 3, Round: 4, Fingerprint: "post-pr"}, nil
		}
		return Decision{}, nil
	}}
	err := driver.Loop(context.Background(), "plan", LoopOptions{
		Baseline:    0,
		Attempts:    2,
		MaxAttempts: 5,
		Execute:     func(context.Context) error { return nil },
		PersistProgress: func(_ context.Context, attempts, _ int, _ string) error {
			persistedAttempts = attempts
			return nil
		},
	})
	if err != nil {
		t.Fatalf("Loop returned error: %v", err)
	}
	if persistedAttempts != 1 {
		t.Fatalf("persisted attempts = %d, want 1 from reset baseline", persistedAttempts)
	}
}

func TestDriverReviewDrivenReopenStillConsumesExistingBudget(t *testing.T) {
	detail, _ := recurringDriverDetail()
	driver := Driver{Resolve: fixedDriverResolver(detail), Record: fixedAutomaticRecordFactory}

	decision, err := driver.Decide(context.Background(), "plan", 0, 0, "", 2, plan.AgentBudgetThresholds{})
	if err != nil {
		t.Fatalf("Decide returned error: %v", err)
	}
	if decision.StopKind != StopKindCapExhausted || decision.Round != 2 {
		t.Fatalf("review-driven decision = %+v, want consumed two-attempt budget", decision)
	}
}

func TestDriverDecideStopPrecedenceOverRecurringFiles(t *testing.T) {
	tests := []struct {
		name        string
		maxAttempts int
		previous    func(*plan.PlanDetail, string) string
		wantKind    StopKind
	}{
		{
			name:        "custom cap wins over fingerprint and budget",
			maxAttempts: 2,
			previous: func(detail *plan.PlanDetail, _ string) string {
				return ReworkFindingsFingerprint(ReviewFindings(detail))
			},
			wantKind: StopKindCapExhausted,
		},
		{
			name:        "exact fingerprint",
			maxAttempts: 5,
			previous: func(detail *plan.PlanDetail, _ string) string {
				return ReworkFindingsFingerprint(ReviewFindings(detail))
			},
			wantKind: StopKindFindingsStalled,
		},
		{
			name: "plan budget", maxAttempts: 5,
			previous: func(_ *plan.PlanDetail, previous string) string { return previous },
			wantKind: StopKindPlanBudget,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			detail, previous := recurringDriverDetail()
			detail.Events = append(detail.Events, plan.Event{Type: plan.EventTypeAgentMetrics, Metrics: &plan.AgentMetrics{SessionID: "large", ToolCalls: 516}})
			for _, event := range detail.Events {
				if event.Review != nil {
					for index := range event.Review.Findings {
						event.Review.Findings[index].Line = 42
					}
				}
			}
			driver := Driver{
				Resolve: fixedDriverResolver(detail),
				Record:  fixedAutomaticRecordFactory,
			}

			got, err := driver.Decide(context.Background(), "plan", 0, 0, test.previous(detail, previous), test.maxAttempts, plan.AgentBudgetThresholds{})
			if err != nil {
				t.Fatalf("Decide returned error: %v", err)
			}
			if got.StopKind != test.wantKind || len(got.Advisories) != 0 || got.Reworked {
				t.Fatalf("decision = %+v, want retained stop kind %q", got, test.wantKind)
			}
			var stopped plan.Event
			for _, event := range detail.Events {
				if event.Type == plan.EventTypeReworkStopped {
					stopped = event
				}
			}
			if stopped.Type != plan.EventTypeReworkStopped || StopKindForPersistedReason(stopped.Reason) != test.wantKind || stopped.Attempts != 2 || stopped.Fingerprint != got.Fingerprint {
				t.Fatalf("stop evidence = %+v", stopped)
			}
			if hasDriverEvent(detail.Events, plan.EventTypeReworkRound) {
				t.Fatal("stop reopened a round")
			}
		})
	}
}

func TestDriverDecideLegacyFingerprintPermitsOneAdditionalRound(t *testing.T) {
	first := actionableDriverDetail(1)
	findings := ReviewFindings(first)
	legacy := BatchLocationFindingsFingerprint(findings)
	driver := Driver{
		Resolve: fixedDriverResolver(first),
		Record:  func(detail *plan.PlanDetail) (AutomaticRecord, error) { return &driverRecord{detail: detail}, nil },
	}

	upgraded, err := driver.Decide(context.Background(), "plan", 1, 0, legacy, 5, plan.AgentBudgetThresholds{})
	if err != nil {
		t.Fatalf("Decide with legacy fingerprint returned error: %v", err)
	}
	if !upgraded.Reworked || upgraded.StopReason != "" || upgraded.Fingerprint == legacy {
		t.Fatalf("legacy fingerprint decision = %+v, want one upgraded round", upgraded)
	}

	repeated := actionableDriverDetail(2)
	repeated.State.Plan.Review.Findings = findings
	driver = Driver{Resolve: fixedDriverResolver(repeated), Record: fixedAutomaticRecordFactory}
	stopped, err := driver.Decide(context.Background(), "plan", 1, 1, upgraded.Fingerprint, 5, plan.AgentBudgetThresholds{})
	if err != nil {
		t.Fatalf("Decide with upgraded fingerprint returned error: %v", err)
	}
	if stopped.StopKind != StopKindFindingsStalled || stopped.Fingerprint != upgraded.Fingerprint {
		t.Fatalf("upgraded repeat decision = %+v, want equivalent-finding stop", stopped)
	}
}

func TestStopKindForPersistedReason(t *testing.T) {
	tests := []struct {
		name   string
		reason string
		want   StopKind
	}{
		{name: "empty", want: StopKindNone},
		{name: "cap", reason: "automatic rework cap exhausted after 5 cycles", want: StopKindCapExhausted},
		{name: "stalled findings", reason: equivalentFindingsStopReason, want: StopKindFindingsStalled},
		{name: "legacy recurring files", reason: recurringFilesStopReason([]string{"b.go", "a.go"}), want: StopKindRecurringFiles},
		{name: "anchor reversal", reason: anchorReversalStopReason([]anchorReversalRounds{{Anchor: "a.go:4", Rounds: []int{1, 4}}}), want: StopKindAnchorReversal},
		{name: "window file recurrence", reason: fileRecurrenceStopReason([]recurringFileRounds{{File: "a.go", Rounds: []int{1, 3, 4}}}), want: StopKindFileRecurrence},
		{name: "plan budget", reason: planBudgetStopReason(planBudgetWarning{Metric: "tool_calls", Observed: 516, Threshold: 400}), want: StopKindPlanBudget},
		{name: "malformed plan budget", reason: planBudgetStopReasonPrefix + "not-json", want: StopKindNone},
		{name: "malformed recurring files", reason: recurringFilesStopReasonPrefix + "not-json", want: StopKindNone},
		{name: "malformed window recurrence", reason: fileRecurrenceStopReasonPrefix + "not-json", want: StopKindNone},
		{name: "unknown", reason: "automatic rework stopped for another reason", want: StopKindNone},
		{name: "cap prefix only", reason: "automatic rework cap exhausted", want: StopKindNone},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := StopKindForPersistedReason(test.reason); got != test.want {
				t.Fatalf("StopKindForPersistedReason(%q) = %q, want %q", test.reason, got, test.want)
			}
		})
	}
}

func TestGuardAutoReworkRestart(t *testing.T) {
	const (
		stalledReason = "automatic rework stalled on equivalent consecutive findings"
		capReason     = "automatic rework cap exhausted after 5 cycles"
	)
	tests := []struct {
		name         string
		status       string
		events       []plan.Event
		allowRestart bool
		wantStopped  bool
		wantKind     StopKind
		wantReason   string
		wantFiles    []string
		wantError    string
	}{
		{
			name: "latest stopped event refuses a fresh budget",
			events: []plan.Event{
				{Type: plan.EventTypeReworkStopped, Reason: stalledReason},
				{Type: plan.EventTypeReworkRound},
				{Type: plan.EventTypePlanReviewed},
				{Type: plan.EventTypeReworkStopped, Reason: capReason},
			},
			wantStopped: true,
			wantKind:    StopKindCapExhausted,
			wantReason:  capReason,
			wantError: "Automatic rework stopped: attempt cap reached.\nReason: automatic rework cap exhausted after 5 cycles\n" +
				"Read the review and address the remaining findings before re-running.\n\n" +
				"A new automatic-rework budget was not started. To deliberately continue, rerun with --rework-restart",
		},
		{
			name:         "explicit restart grants a new budget",
			events:       []plan.Event{{Type: plan.EventTypeReworkStopped, Reason: stalledReason}},
			allowRestart: true,
			wantStopped:  true,
			wantKind:     StopKindFindingsStalled,
			wantReason:   stalledReason,
		},
		{
			name: "round after stop clears guard",
			events: []plan.Event{
				{Type: plan.EventTypeReworkStopped, Reason: stalledReason},
				{Type: plan.EventTypeReworkRound},
			},
		},
		{
			name: "manual reopen after stop clears guard",
			events: []plan.Event{
				{Type: plan.EventTypeReworkStopped, Reason: stalledReason},
				{Type: plan.EventTypePlanReopened},
				{Type: plan.EventTypePlanReviewed},
			},
		},
		{
			name:        "stop on a non-changes-requested plan does not guard",
			status:      plan.StatusReviewed,
			events:      []plan.Event{{Type: plan.EventTypeReworkStopped, Reason: capReason}},
			wantStopped: false,
		},
		{
			name:         "message-only persisted stop restores its kind",
			events:       []plan.Event{{Type: plan.EventTypeReworkStopped, Message: capReason}},
			allowRestart: true,
			wantStopped:  true,
			wantKind:     StopKindCapExhausted,
			wantReason:   capReason,
		},
		{
			name:         "legacy recurring-file stop restores classification",
			events:       []plan.Event{{Type: plan.EventTypeReworkStopped, Reason: recurringFilesStopReason([]string{"z.go", "a.go"})}},
			allowRestart: true,
			wantStopped:  true,
			wantKind:     StopKindRecurringFiles,
			wantReason:   recurringFilesStopReason([]string{"a.go", "z.go"}),
			wantFiles:    []string{"a.go", "z.go"},
		},
		{
			name: "window recurrence stop restores classification",
			events: []plan.Event{{Type: plan.EventTypeReworkStopped, Reason: fileRecurrenceStopReason([]recurringFileRounds{
				{File: "z.go", Rounds: []int{2, 4, 7}},
			})}},
			allowRestart: true,
			wantStopped:  true,
			wantKind:     StopKindFileRecurrence,
			wantReason: fileRecurrenceStopReason([]recurringFileRounds{
				{File: "z.go", Rounds: []int{2, 4, 7}},
			}),
			wantFiles: []string{"z.go"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			detail := actionableDriverDetail(0)
			if test.status != "" {
				detail.State.Status = test.status
			}
			detail.Events = test.events

			decision, stopped, err := GuardAutoReworkRestart(detail, test.allowRestart)
			if stopped != test.wantStopped {
				t.Fatalf("stopped = %t, want %t; decision=%+v", stopped, test.wantStopped, decision)
			}
			if decision.StopKind != test.wantKind || decision.StopReason != test.wantReason {
				t.Fatalf("stop = (%q, %q), want (%q, %q)", decision.StopKind, decision.StopReason, test.wantKind, test.wantReason)
			}
			if !slices.Equal(decision.RecurringFiles, test.wantFiles) {
				t.Fatalf("recurring files = %#v, want %#v", decision.RecurringFiles, test.wantFiles)
			}
			if test.wantError == "" {
				if err != nil {
					t.Fatalf("GuardAutoReworkRestart returned error: %v", err)
				}
			} else if err == nil || err.Error() != test.wantError {
				t.Fatalf("GuardAutoReworkRestart error = %q, want %q", err, test.wantError)
			}
		})
	}
}

func TestGuardAutoReworkRestartFormatsPersistedRecurringFileFindings(t *testing.T) {
	detail := actionableDriverDetail(0)
	detail.State.Plan.Review.Findings = []plan.ReviewFinding{{
		Severity:   "major",
		File:       "z.go",
		Line:       17,
		Message:    "the latest structured finding",
		Suggestion: "address the newest review",
	}}
	detail.Events = []plan.Event{{
		Type:   plan.EventTypeReworkStopped,
		Reason: recurringFilesStopReason([]string{"z.go", "a.go"}),
	}}

	decision, stopped, err := GuardAutoReworkRestart(detail, false)
	if !stopped || err == nil {
		t.Fatalf("persisted stop = (%+v, %t, %v), want guarded error", decision, stopped, err)
	}
	if decision.StopKind != StopKindRecurringFiles || !slices.Equal(decision.RecurringFiles, []string{"a.go", "z.go"}) || !reflect.DeepEqual(decision.Findings, ReviewFindings(detail)) {
		t.Fatalf("persisted recurring-file decision = %+v", decision)
	}
	for _, want := range []string{"THE SAME FILES KEEP RECURRING", "- a.go", "- z.go", "z.go:17", "the latest structured finding", "address the newest review", "--rework-restart"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("restart refusal %q does not contain %q", err, want)
		}
	}
}

func TestFormatStopMessageDistinguishesStallFromCap(t *testing.T) {
	finding := plan.ReviewFinding{
		Severity:   "major",
		File:       "internal/rework/driver.go",
		Line:       99,
		Message:    "the previous fix did not address the race",
		Suggestion: "serialize the update",
	}
	tests := []struct {
		name      string
		decision  Decision
		want      []string
		doNotWant []string
	}{
		{
			name:     "equivalent findings stall is loud and finding-bearing",
			decision: Decision{StopKind: StopKindFindingsStalled, StopReason: equivalentFindingsStopReason, Findings: []plan.ReviewFinding{finding}},
			want:     []string{"!!!!!!!!!!!!!!!!", "THE LOOP IS GOING IN CIRCLES", "internal/rework/driver.go:99", finding.Message, finding.Suggestion, "before re-running"},
		},
		{
			name:     "legacy recurring files remain distinct and finding-bearing",
			decision: Decision{StopKind: StopKindRecurringFiles, StopReason: recurringFilesStopReason([]string{"z.go", "a.go"}), RecurringFiles: []string{"z.go", "a.go"}, Findings: []plan.ReviewFinding{finding}},
			want:     []string{"!!!!!!!!!!!!!!!!", "THE SAME FILES KEEP RECURRING", "three consecutive reviews", "- a.go", "- z.go", finding.Message, finding.Suggestion, "before re-running"},
		},
		{
			name: "window-wide recurrence reports file rounds and current findings",
			decision: Decision{
				StopKind: StopKindFileRecurrence,
				StopReason: fileRecurrenceStopReason([]recurringFileRounds{
					{File: "internal/plan/derive.go", Rounds: []int{1, 2, 3}},
				}),
				Findings: []plan.ReviewFinding{finding},
			},
			want: []string{"!!!!!!!!!!!!!!!!", "A FINDING FILE KEEPS RECURRING", "internal/plan/derive.go", "rounds [1 2 3]", "current findings", finding.Message, finding.Suggestion, "before re-running"},
		},
		{
			name: "plan budget warning names the tripped measurement",
			decision: Decision{
				StopKind:   StopKindPlanBudget,
				StopReason: planBudgetStopReason(planBudgetWarning{Metric: "assistant_messages", Observed: 320, Threshold: 300}),
			},
			want: []string{"plan agent budget warning", "assistant_messages", "observed 320", "threshold 300", "resource use"},
		},
		{
			name:      "cap exhaustion is milder",
			decision:  Decision{StopKind: StopKindCapExhausted, StopReason: "automatic rework cap exhausted after 5 cycles", Findings: []plan.ReviewFinding{finding}},
			want:      []string{"Automatic rework stopped: attempt cap reached", "automatic rework cap exhausted after 5 cycles"},
			doNotWant: []string{"!!!!!!!!!!!!!!!!", "GOING IN CIRCLES", finding.Message},
		},
		{
			name:      "untyped prose is not inferred",
			decision:  Decision{StopReason: "automatic rework cap exhausted after 5 cycles"},
			want:      []string{"automatic rework cap exhausted after 5 cycles"},
			doNotWant: []string{"Automatic rework stopped: attempt cap reached"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			message := FormatStopMessage(test.decision)
			for _, want := range test.want {
				if !strings.Contains(message, want) {
					t.Errorf("stop message %q does not contain %q", message, want)
				}
			}
			for _, unwanted := range test.doNotWant {
				if strings.Contains(message, unwanted) {
					t.Errorf("stop message %q unexpectedly contains %q", message, unwanted)
				}
			}
		})
	}
}

func TestDriverRunDisabledPolicyExecutesOnceWithoutLoadingState(t *testing.T) {
	driver := Driver{
		Resolve: func(context.Context, string) (*plan.PlanDetail, error) {
			t.Fatal("disabled automatic rework resolved plan detail")
			return nil, nil
		},
		DecideOne: func(context.Context, string, int, int, string, int, plan.AgentBudgetThresholds) (Decision, error) {
			t.Fatal("disabled automatic rework made a decision")
			return Decision{}, nil
		},
	}
	executions := 0
	err := driver.Run(context.Background(), "plan", RunOptions{
		Enabled:     false,
		MaxAttempts: 5,
		Execute: func(context.Context) error {
			executions++
			return nil
		},
		BeforeDecision: func(context.Context) (int, bool, error) {
			t.Fatal("disabled automatic rework checked dynamic policy")
			return 0, false, nil
		},
	})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if executions != 1 {
		t.Fatalf("executions = %d, want 1", executions)
	}
}

func TestDriverRunExecutesThenDecidesWithFreshBudget(t *testing.T) {
	detail := actionableDriverDetail(2)
	var calls []string
	decisions := 0
	driver := Driver{
		Resolve: fixedDriverResolver(detail),
		DecideOne: func(_ context.Context, _ string, baseline, attempts int, previous string, maxAttempts int, _ plan.AgentBudgetThresholds) (Decision, error) {
			calls = append(calls, fmt.Sprintf("decide:%d:%d:%s:%d", baseline, attempts, previous, maxAttempts))
			decisions++
			if decisions == 1 {
				return Decision{Reworked: true, Round: 3, Fingerprint: "finding-1"}, nil
			}
			return Decision{}, nil
		},
	}
	err := driver.Run(context.Background(), "plan", RunOptions{
		Enabled:     true,
		MaxAttempts: 5,
		Execute: func(context.Context) error {
			calls = append(calls, "execute")
			return nil
		},
		PersistProgress: func(_ context.Context, attempts, round int, fingerprint string) error {
			calls = append(calls, fmt.Sprintf("persist:%d:%d:%s", attempts, round, fingerprint))
			return nil
		},
		LogProgress: func(round int) error {
			calls = append(calls, fmt.Sprintf("log:%d", round))
			return nil
		},
	})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	want := []string{
		"execute",
		"decide:2:0::5",
		"persist:1:3:finding-1",
		"log:3",
		"execute",
		"decide:2:1:finding-1:5",
	}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %#v, want %#v", calls, want)
	}
}

func TestDriverRunSuppliesBudgetThresholdsToDecision(t *testing.T) {
	custom := plan.DefaultAgentBudgetThresholds()
	custom.Plan.Cost++
	tests := []struct {
		name       string
		configured plan.AgentBudgetThresholds
		want       plan.AgentBudgetThresholds
	}{
		{name: "defaults when unset", want: plan.DefaultAgentBudgetThresholds()},
		{name: "preserves configured thresholds", configured: custom, want: custom},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var got plan.AgentBudgetThresholds
			driver := Driver{
				Resolve: fixedDriverResolver(actionableDriverDetail(0)),
				DecideOne: func(_ context.Context, _ string, _, _ int, _ string, _ int, thresholds plan.AgentBudgetThresholds) (Decision, error) {
					got = thresholds
					return Decision{}, nil
				},
			}
			err := driver.Run(context.Background(), "plan", RunOptions{
				Enabled:          true,
				MaxAttempts:      5,
				BudgetThresholds: test.configured,
				Execute:          func(context.Context) error { return nil },
			})
			if err != nil {
				t.Fatalf("Run returned error: %v", err)
			}
			if got != test.want {
				t.Fatalf("decision thresholds = %#v, want %#v", got, test.want)
			}
		})
	}
}

func TestDriverRunAcknowledgedRestartDecidesBeforeFirstExecution(t *testing.T) {
	detail := actionableDriverDetail(0)
	detail.Events = []plan.Event{{Type: plan.EventTypeReworkStopped, Reason: equivalentFindingsStopReason}}
	var calls []string
	decisions := 0
	driver := Driver{
		Resolve: fixedDriverResolver(detail),
		DecideOne: func(context.Context, string, int, int, string, int, plan.AgentBudgetThresholds) (Decision, error) {
			calls = append(calls, "decide")
			decisions++
			if decisions == 1 {
				return Decision{Reworked: true, Round: 1, Fingerprint: "fresh-finding"}, nil
			}
			return Decision{}, nil
		},
	}
	err := driver.Run(context.Background(), "plan", RunOptions{
		Enabled:      true,
		MaxAttempts:  5,
		AllowRestart: true,
		Execute: func(context.Context) error {
			calls = append(calls, "execute")
			return nil
		},
		PersistProgress: func(context.Context, int, int, string) error {
			calls = append(calls, "persist")
			return nil
		},
		LogProgress: func(int) error {
			calls = append(calls, "log")
			return nil
		},
	})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	want := []string{"decide", "persist", "log", "execute", "decide"}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %#v, want %#v", calls, want)
	}
}

func TestDriverRunRefusesPersistedStopBeforeExecution(t *testing.T) {
	detail := actionableDriverDetail(0)
	detail.Events = []plan.Event{{Type: plan.EventTypeReworkStopped, Reason: equivalentFindingsStopReason}}
	driver := Driver{
		Resolve: fixedDriverResolver(detail),
		DecideOne: func(context.Context, string, int, int, string, int, plan.AgentBudgetThresholds) (Decision, error) {
			t.Fatal("guarded run made a decision")
			return Decision{}, nil
		},
	}
	executed := false
	err := driver.Run(context.Background(), "plan", RunOptions{
		Enabled:     true,
		MaxAttempts: 5,
		Execute: func(context.Context) error {
			executed = true
			return nil
		},
	})
	if err == nil || !strings.Contains(err.Error(), "A new automatic-rework budget was not started") {
		t.Fatalf("Run error = %v, want persisted-stop restart refusal", err)
	}
	if executed {
		t.Fatal("guarded run executed the plan")
	}
}

func TestDriverRunAcceptsRecoveredBudgetState(t *testing.T) {
	var gotBaseline, gotAttempts, gotMax int
	var gotPrevious string
	driver := Driver{
		Resolve: func(context.Context, string) (*plan.PlanDetail, error) {
			t.Fatal("recovered automatic rework resolved fresh plan detail")
			return nil, nil
		},
		DecideOne: func(_ context.Context, _ string, baseline, attempts int, previous string, maxAttempts int, _ plan.AgentBudgetThresholds) (Decision, error) {
			gotBaseline, gotAttempts, gotPrevious, gotMax = baseline, attempts, previous, maxAttempts
			return Decision{}, nil
		},
	}
	err := driver.Run(context.Background(), "plan", RunOptions{
		Enabled:     true,
		MaxAttempts: 7,
		Recovered: &ExecutionState{Budget: Budget{
			BaselineRound:              3,
			Attempts:                   2,
			PreviousFindingFingerprint: "persisted-finding",
		}},
		Execute: func(context.Context) error { return nil },
	})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if gotBaseline != 3 || gotAttempts != 2 || gotPrevious != "persisted-finding" || gotMax != 7 {
		t.Fatalf("recovered decision state = (%d, %d, %q, %d)", gotBaseline, gotAttempts, gotPrevious, gotMax)
	}
}

func TestDriverRunPropagatesHookErrors(t *testing.T) {
	tests := []struct {
		name string
		run  func(error) error
	}{
		{
			name: "execute",
			run: func(want error) error {
				return (Driver{}).Run(context.Background(), "plan", RunOptions{
					Execute: func(context.Context) error { return want },
				})
			},
		},
		{
			name: "dynamic policy and stop check",
			run: func(want error) error {
				return (Driver{Resolve: fixedDriverResolver(actionableDriverDetail(0))}).Run(context.Background(), "plan", RunOptions{
					Enabled:     true,
					MaxAttempts: 5,
					Execute:     func(context.Context) error { return nil },
					BeforeDecision: func(context.Context) (int, bool, error) {
						return 0, false, want
					},
				})
			},
		},
		{
			name: "durable progress",
			run: func(want error) error {
				driver := Driver{
					Resolve: fixedDriverResolver(actionableDriverDetail(0)),
					DecideOne: func(context.Context, string, int, int, string, int, plan.AgentBudgetThresholds) (Decision, error) {
						return Decision{Reworked: true, Round: 1, Fingerprint: "finding"}, nil
					},
				}
				return driver.Run(context.Background(), "plan", RunOptions{
					Enabled:         true,
					MaxAttempts:     5,
					Execute:         func(context.Context) error { return nil },
					PersistProgress: func(context.Context, int, int, string) error { return want },
				})
			},
		},
		{
			name: "progress logging",
			run: func(want error) error {
				driver := Driver{
					Resolve: fixedDriverResolver(actionableDriverDetail(0)),
					DecideOne: func(context.Context, string, int, int, string, int, plan.AgentBudgetThresholds) (Decision, error) {
						return Decision{Reworked: true, Round: 1, Fingerprint: "finding"}, nil
					},
				}
				return driver.Run(context.Background(), "plan", RunOptions{
					Enabled:         true,
					MaxAttempts:     5,
					Execute:         func(context.Context) error { return nil },
					PersistProgress: func(context.Context, int, int, string) error { return nil },
					LogProgress:     func(int) error { return want },
				})
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			want := errors.New(test.name + " failed")
			if err := test.run(want); !errors.Is(err, want) {
				t.Fatalf("Run error = %v, want %v", err, want)
			}
		})
	}
}

func TestDriverRunRecoveredAttemptsNeverDecrease(t *testing.T) {
	decisions := 0
	persistedAttempts := 0
	driver := Driver{DecideOne: func(context.Context, string, int, int, string, int, plan.AgentBudgetThresholds) (Decision, error) {
		decisions++
		if decisions == 1 {
			return Decision{Reworked: true, Round: 11, Fingerprint: "new-finding"}, nil
		}
		return Decision{}, nil
	}}
	err := driver.Run(context.Background(), "plan", RunOptions{
		Enabled:     true,
		MaxAttempts: 9,
		Recovered: &ExecutionState{Budget: Budget{
			BaselineRound: 10,
			Attempts:      4,
		}},
		Execute: func(context.Context) error { return nil },
		PersistProgress: func(_ context.Context, attempts, _ int, _ string) error {
			persistedAttempts = attempts
			return nil
		},
	})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if persistedAttempts != 4 {
		t.Fatalf("persisted attempts = %d, want 4", persistedAttempts)
	}
}

func TestDriverLoopPersistsBeforeEachRerun(t *testing.T) {
	details := []*plan.PlanDetail{
		actionableDriverDetail(0),
		actionableDriverDetail(1),
		{State: plan.State{Status: plan.StatusReviewed}},
	}
	resolveIndex := 0
	driver := Driver{
		Resolve: func(context.Context, string) (*plan.PlanDetail, error) {
			detail := details[resolveIndex]
			resolveIndex++
			return detail, nil
		},
		Record: func(detail *plan.PlanDetail) (AutomaticRecord, error) { return &driverRecord{detail: detail}, nil },
		Now:    func() time.Time { return time.Date(2026, 7, 14, 1, 0, 0, 0, time.FixedZone("offset", 3600)) },
	}
	var calls []string
	err := driver.Loop(context.Background(), "plan", LoopOptions{
		MaxAttempts: 5,
		Execute: func(context.Context) error {
			calls = append(calls, "execute")
			return nil
		},
		PersistProgress: func(_ context.Context, attempts, round int, fingerprint string) error {
			if fingerprint == "" {
				t.Fatal("PersistProgress received an empty fingerprint")
			}
			calls = append(calls, fmt.Sprintf("persist:%d:%d", attempts, round))
			return nil
		},
	})
	if err != nil {
		t.Fatalf("Loop returned error: %v", err)
	}
	want := []string{"execute", "persist:1:1", "execute", "persist:2:2", "execute"}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %#v, want %#v", calls, want)
	}
}

func TestDriverLoopCanReopenBeforeFirstExecution(t *testing.T) {
	detail := actionableDriverDetail(0)
	driver := Driver{
		Resolve: fixedDriverResolver(detail),
		Record:  func(detail *plan.PlanDetail) (AutomaticRecord, error) { return &driverRecord{detail: detail}, nil },
	}
	var calls []string
	err := driver.Loop(context.Background(), "plan", LoopOptions{
		MaxAttempts:         5,
		DecideBeforeExecute: true,
		Execute: func(context.Context) error {
			calls = append(calls, "execute")
			if detail.State.Status != plan.StatusInProgress || len(detail.State.Plan.PendingSlices) == 0 {
				t.Fatalf("first execution saw plan before reopen: status=%q pending=%v", detail.State.Status, detail.State.Plan.PendingSlices)
			}
			detail.State.Status = plan.StatusReviewed
			return nil
		},
		PersistProgress: func(context.Context, int, int, string) error {
			calls = append(calls, "persist")
			return nil
		},
	})
	if err != nil {
		t.Fatalf("Loop returned error: %v", err)
	}
	want := []string{"persist", "execute"}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %#v, want %#v", calls, want)
	}
}

func TestDriverLoopPropagatesExecuteError(t *testing.T) {
	want := errors.New("execute failed")
	driver := Driver{}
	err := driver.Loop(context.Background(), "plan", LoopOptions{
		MaxAttempts: 5,
		Execute:     func(context.Context) error { return want },
	})
	if !errors.Is(err, want) {
		t.Fatalf("Loop error = %v, want %v", err, want)
	}
}

func TestDriverLoopAttemptsNeverDecrease(t *testing.T) {
	details := []*plan.PlanDetail{actionableDriverDetail(0), {State: plan.State{Status: plan.StatusReviewed}}}
	resolveIndex := 0
	driver := Driver{
		Resolve: func(context.Context, string) (*plan.PlanDetail, error) {
			detail := details[resolveIndex]
			resolveIndex++
			return detail, nil
		},
		Record: func(detail *plan.PlanDetail) (AutomaticRecord, error) { return &driverRecord{detail: detail}, nil },
	}
	persistedAttempts := 0
	err := driver.Loop(context.Background(), "plan", LoopOptions{
		Attempts:    4,
		MaxAttempts: 5,
		Execute:     func(context.Context) error { return nil },
		PersistProgress: func(_ context.Context, attempts, _ int, _ string) error {
			persistedAttempts = attempts
			return nil
		},
	})
	if err != nil {
		t.Fatalf("Loop returned error: %v", err)
	}
	if persistedAttempts != 4 {
		t.Fatalf("persisted attempts = %d, want 4", persistedAttempts)
	}
}

func TestDriverAdvisoryDeliveryIsBestEffortAfterMandatoryProgress(t *testing.T) {
	for _, entry := range []string{"Run", "Loop"} {
		for _, failure := range []string{"none", "no callback", "mutation", "persist", "log", "execute", "stop", "decline"} {
			t.Run(entry+"/"+failure, func(t *testing.T) {
				detail, _ := recurringDriverDetail()
				wantErr := errors.New("mandatory " + failure + " failure")
				var calls []string
				driver := Driver{
					Resolve: fixedDriverResolver(detail),
					Record: func(detail *plan.PlanDetail) (AutomaticRecord, error) {
						r := &driverRecord{detail: detail}
						if failure == "mutation" {
							r.reopenErr = wantErr
						}
						return r, nil
					},
				}
				opts := LoopOptions{
					MaxAttempts: 5, DecideBeforeExecute: true,
					Execute: func(context.Context) error {
						calls = append(calls, "execute")
						if failure == "execute" {
							return wantErr
						}
						detail.State.Status = plan.StatusReviewed
						return nil
					},
					PersistProgress: func(_ context.Context, attempts, round int, fingerprint string) error {
						calls = append(calls, "persist")
						if attempts != 3 || round != 3 || fingerprint == "" || !hasDriverEvent(detail.Events, plan.EventTypeReworkRound) {
							t.Fatalf("progress before settled round: attempts=%d round=%d fingerprint=%q", attempts, round, fingerprint)
						}
						if failure == "persist" {
							return wantErr
						}
						return nil
					},
					LogProgress: func(int) error {
						calls = append(calls, "log")
						if failure == "log" {
							return wantErr
						}
						return nil
					},
					LogAdvisories: func(round int, advisories []Advisory) error {
						calls = append(calls, "advisory")
						want := []Advisory{{Kind: AdvisoryKindFileRecurrence, Location: "store/file.go", Rounds: []int{1, 2, 3}}}
						if round != 3 || !reflect.DeepEqual(advisories, want) {
							t.Fatalf("delivery = %d %+v, want round 3 %+v", round, advisories, want)
						}
						return errors.New("best-effort output failed")
					},
				}
				wantCalls := []string{"persist", "log", "advisory", "execute"}
				switch failure {
				case "no callback":
					opts.LogAdvisories = nil
					wantCalls = []string{"persist", "log", "execute"}
				case "mutation":
					wantCalls = nil
				case "persist":
					wantCalls = []string{"persist"}
				case "log":
					wantCalls = []string{"persist", "log"}
				case "stop":
					opts.MaxAttempts = 2
					wantCalls = nil
				case "decline":
					detail.State.Status = plan.StatusReviewed
					wantCalls = []string{"execute"}
				}
				var err error
				if entry == "Run" {
					err = driver.Run(context.Background(), "plan", RunOptions{
						Enabled: true, MaxAttempts: opts.MaxAttempts,
						Recovered: &ExecutionState{DecideBeforeExecute: true},
						Execute:   opts.Execute, PersistProgress: opts.PersistProgress,
						LogProgress: opts.LogProgress, LogAdvisories: opts.LogAdvisories,
					})
				} else {
					err = driver.Loop(context.Background(), "plan", opts)
				}
				switch failure {
				case "mutation", "persist", "log", "execute":
					if !errors.Is(err, wantErr) {
						t.Fatalf("error = %v, want %v", err, wantErr)
					}
				case "stop":
					if err == nil || !strings.Contains(err.Error(), "attempt cap reached") {
						t.Fatalf("error = %v, want cap stop", err)
					}
				default:
					if err != nil {
						t.Fatalf("advisory delivery blocked execution: %v", err)
					}
				}
				if !reflect.DeepEqual(calls, wantCalls) {
					t.Fatalf("calls = %v, want %v", calls, wantCalls)
				}
				if failure != "stop" && hasDriverEvent(detail.Events, plan.EventTypeReworkStopped) {
					t.Fatal("advisory wrote stop evidence")
				}
			})
		}
	}
}

func TestDriverRunHistoricalLocationStopsRequireExplicitBoundedRestart(t *testing.T) {
	for _, reason := range []string{
		anchorReversalStopReason([]anchorReversalRounds{{Anchor: "store/file.go:42", Rounds: []int{1, 2}}}),
		fileRecurrenceStopReason([]recurringFileRounds{{File: "store/file.go", Rounds: []int{1, 2, 3}}}),
		recurringFilesStopReason([]string{"store/file.go"}),
	} {
		for _, restart := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/restart=%t", StopKindForPersistedReason(reason), restart), func(t *testing.T) {
				detail, _ := recurringDriverDetail()
				detail.Events = append(detail.Events, plan.Event{Type: plan.EventTypeReworkStopped, Round: 2, Attempts: 2, Reason: reason})
				before := slices.Clone(detail.Events)
				driver := Driver{Resolve: fixedDriverResolver(detail), Record: fixedAutomaticRecordFactory}
				executions, progress := 0, 0
				err := driver.Run(context.Background(), "plan", RunOptions{
					Enabled: true, MaxAttempts: 1, AllowRestart: restart,
					Execute: func(context.Context) error {
						executions++
						if RoundCount(detail) != 3 || detail.State.Status != plan.StatusInProgress {
							t.Fatalf("execution before reopened round: %+v", detail.State)
						}
						detail.State.Status = plan.StatusChangesRequested
						return nil
					},
					PersistProgress: func(_ context.Context, attempts, round int, _ string) error {
						progress++
						if attempts != 1 || round != 3 {
							t.Fatalf("restart budget = %d/%d, want 1/3", attempts, round)
						}
						return nil
					},
					LogAdvisories: func(int, []Advisory) error {
						t.Fatal("historical recurrence leaked across restart baseline")
						return nil
					},
				})
				if !reflect.DeepEqual(detail.Events[:len(before)], before) {
					t.Fatal("historical events were rewritten")
				}
				if !restart {
					if err == nil || !strings.Contains(err.Error(), "--rework-restart") || !strings.Contains(err.Error(), "store/file.go") || executions != 0 || progress != 0 || !reflect.DeepEqual(detail.Events, before) {
						t.Fatalf("historical stop not authoritative: error=%v executions=%d progress=%d", err, executions, progress)
					}
					return
				}
				if err == nil || !strings.Contains(err.Error(), "attempt cap reached") || executions != 1 || progress != 1 {
					t.Fatalf("restart was not bounded: error=%v executions=%d progress=%d", err, executions, progress)
				}
				var round, stop plan.Event
				for _, event := range detail.Events[len(before):] {
					switch event.Type {
					case plan.EventTypeReworkRound:
						round = event
					case plan.EventTypeReworkStopped:
						stop = event
					}
				}
				if round.Round != 3 || round.Attempts != 1 || stop.Round != 3 || stop.Attempts != 1 || StopKindForPersistedReason(stop.Reason) != StopKindCapExhausted {
					t.Fatalf("restart evidence: round=%+v stop=%+v", round, stop)
				}
			})
		}
	}
}

func requireDriverReopenedRound(t *testing.T, detail *plan.PlanDetail, decision Decision, attempts int) {
	t.Helper()
	var round plan.Event
	for _, event := range detail.Events {
		switch event.Type {
		case plan.EventTypeReworkStopped:
			t.Fatalf("location advisory wrote stop evidence: %+v", event)
		case plan.EventTypeReworkRound:
			if event.Round == decision.Round {
				round = event
			}
		}
	}
	if round.Type != plan.EventTypeReworkRound || round.PlanID != "plan" || round.Round != decision.Round || round.Attempts != attempts || round.Fingerprint != decision.Fingerprint {
		t.Fatalf("rework_round event = %+v, decision = %+v", round, decision)
	}
}

func hasDriverEvent(events []plan.Event, eventType string) bool {
	for _, event := range events {
		if event.Type == eventType {
			return true
		}
	}
	return false
}

func fixedAutomaticRecordFactory(detail *plan.PlanDetail) (AutomaticRecord, error) {
	return &driverRecord{detail: detail}, nil
}

func fixedDriverResolver(detail *plan.PlanDetail) PlanResolver {
	return func(context.Context, string) (*plan.PlanDetail, error) { return detail, nil }
}

func recurringDriverDetail() (*plan.PlanDetail, string) {
	detail := actionableDriverDetail(2)
	line := 40
	reviewEvent := func(sliceID, message string) plan.Event {
		line++
		return plan.Event{
			Type: plan.EventTypePlanReviewed, SliceID: sliceID,
			Review: &plan.PlanReview{
				Status: plan.ReviewStatusCompleted, Verdict: plan.ReviewVerdictChangesRequested, FindingsCount: 1,
				Findings: []plan.ReviewFinding{{Severity: "major", File: "store/file.go", Line: line, Message: message}},
			},
		}
	}
	detail.Events = []plan.Event{
		reviewEvent("", "initial review"),
		reviewEvent("r101-finding", "Warp drops the recovered record"),
		reviewEvent("r201-finding", "Warp leaks the write transaction"),
		reviewEvent("r301-finding", "Warp corrupts the committed record"),
	}
	firstMessage := "Warp drops the recovered record"
	secondMessage := "Warp leaks the write transaction"
	for index := range detail.Slices.Slices {
		detail.Slices.Slices[index].ExpectedFiles = []string{"store/file.go", "store/file_test.go"}
		detail.Slices.Slices[index].Goal = []string{firstMessage, secondMessage}[index]
	}
	detail.State.Plan.Review.Findings = []plan.ReviewFinding{{
		Severity:   "major",
		File:       "store/file.go",
		Line:       42,
		Message:    "Warp corrupts the committed record",
		Suggestion: "preserve the committed value",
	}}
	previous := ReworkFindingsFingerprint([]plan.ReviewFinding{{
		Severity:   "major",
		File:       "store/file.go",
		Line:       42,
		Message:    secondMessage,
		Suggestion: "close the transaction",
	}})
	return detail, previous
}

func actionableDriverDetail(round int) *plan.PlanDetail {
	detail := &plan.PlanDetail{
		State: plan.State{
			Status: plan.StatusChangesRequested,
			Plan: plan.PlanState{
				ID: "plan",
				Review: &plan.PlanReview{
					Status:   plan.ReviewStatusCompleted,
					Verdict:  plan.ReviewVerdictChangesRequested,
					Findings: []plan.ReviewFinding{{Severity: "major", File: fmt.Sprintf("internal/rework/driver-round-%d.go", round), Message: fmt.Sprintf("fix driver round %d", round)}},
				},
			},
		},
	}
	for current := 1; current <= round; current++ {
		detail.Slices.Slices = append(detail.Slices.Slices, plan.Slice{ID: fmt.Sprintf("r%d01-driver", current)})
	}
	return detail
}
