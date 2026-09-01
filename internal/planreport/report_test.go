package planreport

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/iamseth/tao/internal/plan"
)

func TestProjectFullAcrossLifecyclePhases(t *testing.T) {
	now := time.Date(2026, 8, 4, 16, 0, 0, 0, time.FixedZone("offset", 3600))
	cases := []struct {
		name, status, want string
		review             *plan.PlanReview
		merged             bool
	}{
		{"planned", plan.StatusPlanned, plan.StatusPlanned, nil, false},
		{"in progress", plan.StatusInProgress, plan.StatusInProgress, nil, false},
		{"blocked", plan.StatusBlocked, plan.StatusBlocked, nil, false},
		{"review phase", plan.StatusInReview, plan.StatusInReview, nil, false},
		{"changes requested", plan.StatusChangesRequested, plan.StatusChangesRequested, &plan.PlanReview{Status: plan.ReviewStatusCompleted, Verdict: plan.ReviewVerdictChangesRequested}, false},
		{"reviewed", plan.StatusReviewed, plan.StatusReviewed, &plan.PlanReview{Status: plan.ReviewStatusCompleted, Verdict: plan.ReviewVerdictApprove}, false},
		{"completed and merged", plan.StatusCompleted, plan.StatusCompleted, &plan.PlanReview{Status: plan.ReviewStatusCompleted, Verdict: plan.ReviewVerdictApprove}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			detail := reportFixture(now)
			detail.State.Status = tc.status
			if plan.IsPostSliceStatus(tc.status) {
				detail.State.Plan.PendingSlices = nil
				detail.Slices.Slices[0].Status = plan.StatusCompleted
			}
			if tc.review != nil {
				plan.SetPersistedReview(detail, *tc.review)
			}
			if tc.merged {
				detail.Events = append(detail.Events, plan.Event{Type: plan.EventTypePlanMerged, Timestamp: now})
			}
			got := ProjectFull(detail, now)
			if got.Schema != SchemaV1 || got.Mode != ModeFull || got.Status != tc.want || got.Outcome.Merged != tc.merged {
				t.Fatalf("projection = schema %q mode %q status %q merged %v", got.Schema, got.Mode, got.Status, got.Outcome.Merged)
			}
			if !got.SnapshotAt.Equal(now.UTC()) || got.SnapshotAt.Location() != time.UTC {
				t.Fatalf("snapshot = %s, want UTC %s", got.SnapshotAt, now.UTC())
			}
		})
	}
}

func TestProjectFullSanitizesAbandonmentAndNeverProjectsMergeOrApproval(t *testing.T) {
	now := time.Date(2026, 9, 1, 17, 0, 0, 0, time.UTC)
	detail := reportFixture(now)
	detail.State.Status = plan.StatusAbandoned
	detail.Events = []plan.Event{
		{Type: plan.EventTypePlanAbandoned, Timestamp: now, Reason: "superseded\nowner@example.com\x1b[31m"},
		{Type: plan.EventTypePlanMerged, Timestamp: now.Add(time.Minute)},
	}
	plan.SetPersistedReview(detail, plan.PlanReview{Status: plan.ReviewStatusCompleted, Verdict: plan.ReviewVerdictApprove, Summary: "approved secret"})

	got := ProjectFull(detail, now)
	if got.Status != plan.StatusAbandoned || !got.Outcome.Abandoned || got.Outcome.Merged || !got.Outcome.AbandonedAt.Equal(now) {
		t.Fatalf("abandonment outcome = status %q outcome %+v", got.Status, got.Outcome)
	}
	if got.Review.Available || got.Outcome.Reason.Text.text == "" {
		t.Fatalf("abandonment projected approval or omitted reason: review=%+v outcome=%+v", got.Review, got.Outcome)
	}
	values := collectSafeText(got)
	for _, forbidden := range []string{"owner@example.com", "approved secret", "\x1b"} {
		if strings.Contains(values, forbidden) {
			t.Fatalf("abandonment projection retained %q in %q", forbidden, values)
		}
	}

	planningJSON, err := json.Marshal(ProjectPlanningOnly(detail, now))
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"Abandon", "abandon", "superseded", "owner@example.com"} {
		if strings.Contains(string(planningJSON), forbidden) {
			t.Fatalf("planning-only projection contains abandonment data: %s", planningJSON)
		}
	}
}

func TestProjectFullHandlesMissingAndMalformedAbandonmentEvidence(t *testing.T) {
	now := time.Date(2026, 9, 1, 17, 0, 0, 0, time.UTC)
	missing := reportFixture(now)
	missing.State.Status = plan.StatusAbandoned
	got := ProjectFull(missing, now)
	if !got.Outcome.Abandoned || got.Outcome.Reason.Available || !got.Outcome.AbandonedAt.IsZero() || got.Outcome.Merged {
		t.Fatalf("missing abandonment evidence = %+v", got.Outcome)
	}

	malformed := reportFixture(now)
	malformed.State.Status = plan.StatusAbandoned
	malformed.Events = []plan.Event{{Type: plan.EventTypePlanAbandoned, Reason: strings.Repeat("x", defaultTextLimit+100)}}
	got = ProjectFull(malformed, now)
	if !got.Outcome.Reason.Available || len([]rune(got.Outcome.Reason.Text.text)) > defaultTextLimit || !got.Outcome.AbandonedAt.IsZero() {
		t.Fatalf("malformed abandonment evidence = %+v", got.Outcome)
	}
	foundTruncation := false
	for _, disclosure := range got.Disclosures {
		if disclosure.Section == sectionOutcome && disclosure.Category == DisclosureTruncated {
			foundTruncation = true
		}
	}
	if !foundTruncation {
		t.Fatalf("malformed long reason lacks outcome disclosure: %+v", got.Disclosures)
	}
}

func TestProjectFullAcceptsProjectedVerificationFailureStatus(t *testing.T) {
	now := time.Date(2026, 8, 4, 15, 0, 0, 0, time.UTC)
	detail := reportFixture(now)
	detail.State.Status = plan.StatusInReview
	detail.State.Workspace = &plan.Workspace{HeadSHA: "head-a"}
	detail.State.Plan.PendingSlices = nil
	detail.State.Plan.CompletedSlices = []string{"001-build", "002-ship"}
	detail.State.Plan.FinalVerification = &plan.FinalVerification{Command: "make verify", HeadSHA: "head-a", Result: "failed", FailureKind: plan.FinalVerificationFailureKindCode, Fingerprint: "failure-a"}
	for i := range detail.Slices.Slices {
		detail.Slices.Slices[i].Status = plan.StatusCompleted
	}

	if got := ProjectFull(detail, now).Status; got != plan.StatusVerificationFailed {
		t.Fatalf("projected report status = %q, want %q", got, plan.StatusVerificationFailed)
	}
}

func TestProjectFullPullRequestCompletionDistinguishesMergeEvidence(t *testing.T) {
	now := time.Date(2026, 8, 4, 15, 0, 0, 0, time.UTC)
	detail := reportFixture(now)
	detail.State.Status = plan.StatusReviewed
	detail.State.Plan.PendingSlices = nil
	for i := range detail.Slices.Slices {
		detail.Slices.Slices[i].Status = plan.StatusCompleted
	}
	review := plan.PlanReview{Status: plan.ReviewStatusCompleted, Verdict: plan.ReviewVerdictApprove, Head: "head123"}
	plan.SetPersistedReview(detail, review)
	detail.State.Plan.PullRequest = &plan.PullRequest{Number: 42, URL: "https://example.test/pull/42", HeadSHA: review.Head}

	if !plan.PlanIsPullRequestComplete(detail) || plan.PlanIsMerged(detail.Events) {
		t.Fatalf("fixture PR complete = %t merged = %t, want true/false", plan.PlanIsPullRequestComplete(detail), plan.PlanIsMerged(detail.Events))
	}
	got := ProjectFull(detail, now)
	if got.Status != plan.StatusCompleted || got.Outcome.Merged {
		t.Fatalf("PR projection = status %q merged %v, want completed and not merged", got.Status, got.Outcome.Merged)
	}
	markdown, err := RenderFull(got)
	if err != nil {
		t.Fatal(err)
	}
	if text := string(markdown); !strings.Contains(text, "status: completed\n") || !strings.Contains(text, "`not merged`") {
		t.Fatalf("PR report did not render completed and not merged:\n%s", text)
	}

	detail.Events = append(detail.Events, plan.Event{Type: plan.EventTypePlanMerged, Timestamp: now})
	got = ProjectFull(detail, now)
	if got.Status != plan.StatusCompleted || !got.Outcome.Merged {
		t.Fatalf("merged PR projection = status %q merged %v, want completed and merged", got.Status, got.Outcome.Merged)
	}
}

func TestProjectFullLegacyCompletedPlanReportsMerged(t *testing.T) {
	now := time.Date(2026, 8, 4, 15, 0, 0, 0, time.UTC)
	detail := reportFixture(now)
	detail.State.Status = plan.StatusCompleted
	detail.State.Plan.PendingSlices = nil
	for i := range detail.Slices.Slices {
		detail.Slices.Slices[i].Status = plan.StatusCompleted
	}
	if plan.PlanIsMerged(detail.Events) {
		t.Fatal("legacy fixture must not have a plan_merged event")
	}

	got := ProjectFull(detail, now)
	if got.Status != plan.StatusCompleted || !got.Outcome.Merged {
		t.Fatalf("legacy projection = status %q merged %v, want completed and merged", got.Status, got.Outcome.Merged)
	}

	plan.SetPersistedReview(detail, plan.PlanReview{Status: plan.ReviewStatusCompleted, Verdict: plan.ReviewVerdictApprove, Head: "current-head"})
	detail.State.Plan.PullRequest = &plan.PullRequest{HeadSHA: "stale-head"}
	if plan.PlanIsPullRequestComplete(detail) {
		t.Fatal("mismatched PR metadata must not qualify for PR completion")
	}
	got = ProjectFull(detail, now)
	if got.Status != plan.StatusCompleted || !got.Outcome.Merged {
		t.Fatalf("legacy projection with nonqualifying PR = status %q merged %v, want completed and merged", got.Status, got.Outcome.Merged)
	}
}

func TestProjectFullLegacyCompletedPlanWithPullRequestIntentIsNotMerged(t *testing.T) {
	now := time.Date(2026, 8, 31, 18, 0, 0, 0, time.UTC)
	detail := reportFixture(now)
	detail.State.Status = plan.StatusCompleted
	detail.State.Plan.PendingSlices = nil
	for i := range detail.Slices.Slices {
		detail.Slices.Slices[i].Status = plan.StatusCompleted
	}
	plan.SetPersistedReview(detail, plan.PlanReview{Status: plan.ReviewStatusCompleted, Verdict: plan.ReviewVerdictApprove, Head: "current-head"})
	detail.State.Plan.PullRequestIntent = &plan.PullRequest{Branch: "fix/report", HeadSHA: "current-head"}

	if action := plan.DeriveNextAction(detail).Primary; action.Kind != plan.PlanActionRecoverPullRequest {
		t.Fatalf("intent-only next action = %+v, want pull-request recovery", action)
	}
	got := ProjectFull(detail, now)
	if got.Status != plan.StatusCompleted || got.Outcome.Merged || got.Finalization.Available {
		t.Fatalf("intent-only projection = status %q merged %v finalization %+v, want completed, not merged, and no failure projection", got.Status, got.Outcome.Merged, got.Finalization)
	}
	markdown, err := RenderFull(got)
	if err != nil {
		t.Fatal(err)
	}
	if text := string(markdown); !strings.Contains(text, "`not merged`") {
		t.Fatalf("intent-only report must not infer integration:\n%s", text)
	}

	detail.Events = append(detail.Events, plan.Event{Type: plan.EventTypePlanMerged, Timestamp: now})
	if got = ProjectFull(detail, now); !got.Outcome.Merged {
		t.Fatalf("explicit merge projection = merged %v, want true despite retained intent", got.Outcome.Merged)
	}
}

func TestProjectFullLegacyCompletedPlanWithFinalizationRecoveryIsNotMerged(t *testing.T) {
	now := time.Date(2026, 8, 31, 18, 0, 0, 0, time.UTC)
	detail := reportFixture(now)
	detail.State.Status = plan.StatusCompleted
	detail.State.Plan.PendingSlices = nil
	for i := range detail.Slices.Slices {
		detail.Slices.Slices[i].Status = plan.StatusCompleted
	}
	detail.State.Workspace = &plan.Workspace{Branch: "fix/report", HeadSHA: "current-head"}
	detail.State.Plan.FinalizationFailure = &plan.FinalizationFailure{
		Phase: plan.FinalizationFailurePhasePullRequest, Category: "publication_failed", Branch: "fix/report", HeadSHA: "current-head",
		FailedAt: now, RecoveryAction: plan.FinalizationRecoveryResumePullRequest,
	}

	got := ProjectFull(detail, now)
	if got.Status != plan.StatusCompleted || got.Outcome.Merged || !got.Finalization.Available {
		t.Fatalf("legacy recovery projection = status %q merged %v finalization %+v, want completed, not merged, and available recovery", got.Status, got.Outcome.Merged, got.Finalization)
	}
	markdown, err := RenderFull(got)
	if err != nil {
		t.Fatal(err)
	}
	text := string(markdown)
	if !strings.Contains(text, "`not merged`") || !strings.Contains(text, "**Finalization recovery**") || !strings.Contains(text, "tao run --pull-request leadership-report") {
		t.Fatalf("legacy recovery report must distinguish pending finalization from integration:\n%s", text)
	}
}

func TestProjectFullSummarizesWithoutRawExecutionEvidence(t *testing.T) {
	now := time.Date(2026, 8, 4, 15, 0, 0, 0, time.UTC)
	detail := reportFixture(now)
	duration := int64(90)
	detail.Slices.Slices[0].Timing.DurationSeconds = &duration
	detail.Slices.Slices[0].VerificationResults = []plan.VerificationRun{
		{Command: "deploy --token secret-value", CWD: "/home/person/project", Result: "passed", Details: "customer@example.com"},
		{Command: "unsafe", Result: "failed", Details: "raw details"},
	}
	detail.State.Plan.FinalVerification = &plan.FinalVerification{Command: "make verify", CWD: "/private/repo", Result: "passed", Details: "secret output"}
	detail.Events = []plan.Event{
		{Type: plan.EventTypeAgentMetrics, Agent: "pi-secret", Metrics: &plan.AgentMetrics{Agent: "pi-secret", SessionID: "session-secret", ProviderID: "provider-secret", ModelID: "model-secret", Status: plan.StatusCompleted, InputTokens: 10, OutputTokens: 20, TotalTokens: 30, ToolCalls: 2}},
		{Type: plan.EventTypeAgentMetrics, Metrics: &plan.AgentMetrics{Agent: "other-secret", SessionID: "session-two", Result: "failed", OutputTokens: 5, Cost: 1.25}},
	}
	plan.SetPersistedReview(detail, plan.PlanReview{Status: plan.ReviewStatusCompleted, Verdict: plan.ReviewVerdictChangesRequested, Summary: "Needs a safer rollout.", FindingsCount: 3, Findings: []plan.ReviewFinding{{File: "/secret/file", Message: "raw finding"}}, Agent: "reviewer-secret", Base: "base-sha", Head: "head-sha"})

	got := ProjectFull(detail, now)
	if got.Slices[0].Verification != (CountSummary{Total: 2, Passed: 1, Failed: 1}) || got.Slices[0].Duration.Seconds != 90 {
		t.Fatalf("slice summary = %+v", got.Slices[0])
	}
	if !got.Execution.FinalVerification.Available || !got.Execution.Telemetry.Available || got.Execution.Telemetry.Attempts != 2 || got.Execution.Telemetry.AgentCount.Value != 2 || got.Execution.Telemetry.OutputTokens.Value != 25 {
		t.Fatalf("execution summary = %+v", got.Execution)
	}
	if !got.Review.Available || got.Review.FindingCount != 3 || got.Review.Verdict != plan.ReviewVerdictChangesRequested {
		t.Fatalf("review = %+v", got.Review)
	}
	values := collectSafeText(got)
	for _, forbidden := range []string{"deploy --token", "/home/person", "customer@example.com", "raw details", "session-secret", "provider-secret", "model-secret", "pi-secret", "reviewer-secret", "base-sha", "head-sha", "raw finding"} {
		if strings.Contains(values, forbidden) {
			t.Fatalf("projection retained forbidden value %q in %q", forbidden, values)
		}
	}
}

func TestProjectFullSanitizesCurrentFinalizationRecovery(t *testing.T) {
	now := time.Date(2026, 8, 29, 20, 0, 0, 0, time.UTC)
	detail := reportFixture(now)
	detail.State.Workspace = &plan.Workspace{Branch: "owner@example.com", HeadSHA: "secret-head-identity"}
	detail.State.Plan.FinalizationFailure = &plan.FinalizationFailure{
		Phase: plan.FinalizationFailurePhasePullRequest, Category: "publication_failed", Branch: "owner@example.com", HeadSHA: "secret-head-identity",
		FailedAt: now, RecoveryAction: "resume_pull_request",
	}

	got := ProjectFull(detail, now)
	if !got.Finalization.Available || got.Finalization.Phase != string(plan.FinalizationFailurePhasePullRequest) || !got.Finalization.Category.Available || !got.Finalization.Action.Available {
		t.Fatalf("finalization projection = %+v", got.Finalization)
	}
	markdown, err := RenderFull(got)
	if err != nil {
		t.Fatal(err)
	}
	text := string(markdown)
	for _, want := range []string{"**Finalization recovery**", "- Failed phase: pull request finalization", `- Category: publication\_failed`, "- Next action: tao run --pull-request leadership-report"} {
		if !strings.Contains(text, want) {
			t.Fatalf("full report missing %q:\n%s", want, text)
		}
	}
	for _, forbidden := range []string{"owner@example.com", "secret-head-identity", "resume_pull_request"} {
		if strings.Contains(collectSafeText(got)+text, forbidden) {
			t.Fatalf("full report exposed raw finalization value %q", forbidden)
		}
	}

	planning := ProjectPlanningOnly(detail, now)
	planningJSON, err := json.Marshal(planning)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.ToLower(string(planningJSON)), "finalization") || strings.Contains(string(planningJSON), "publication_failed") {
		t.Fatalf("planning-only projection contains execution finalization: %s", planningJSON)
	}
}

func TestProjectFullAttributesSliceTokensByExactID(t *testing.T) {
	now := time.Date(2026, 8, 4, 15, 0, 0, 0, time.UTC)
	detail := reportFixture(now)
	detail.Events = []plan.Event{
		{Type: plan.EventTypeAgentMetrics, SliceID: "001-build", Metrics: &plan.AgentMetrics{Status: plan.StatusCompleted, TotalTokens: 10}},
		{Type: plan.EventTypeAgentMetrics, SliceID: "001-build", Metrics: &plan.AgentMetrics{Result: "failed", TotalTokens: 25}},
		{Type: plan.EventTypeAgentMetrics, SliceID: "001-build", Metrics: &plan.AgentMetrics{Status: plan.StatusCompleted}},
		{Type: plan.EventTypeAgentMetrics, SliceID: "001-build-extra", Metrics: &plan.AgentMetrics{TotalTokens: 1000}},
		{Type: plan.EventTypeAgentMetrics, SliceID: "unknown", Metrics: &plan.AgentMetrics{TotalTokens: 2000}},
	}

	got := ProjectFull(detail, now)
	if got.Slices[0].TotalTokens != (OptionalInt64{Available: true, Value: 35}) {
		t.Fatalf("first slice tokens = %+v, want measured total 35", got.Slices[0].TotalTokens)
	}
	if got.Slices[1].TotalTokens.Available || got.Slices[1].TotalTokens.Value != 0 {
		t.Fatalf("missing slice tokens presented as measured: %+v", got.Slices[1].TotalTokens)
	}
}

func TestProjectFullDistinguishesRecordedZeroTokensFromOmitted(t *testing.T) {
	now := time.Date(2026, 8, 4, 15, 0, 0, 0, time.UTC)
	detail := reportFixture(now)
	if err := json.Unmarshal([]byte(`[
		{"type":"agent_metrics","slice_id":"001-build","metrics":{"total_tokens":0}},
		{"type":"agent_metrics","slice_id":"002-ship","metrics":{}}
	]`), &detail.Events); err != nil {
		t.Fatal(err)
	}

	got := ProjectFull(detail, now)
	if got.Slices[0].TotalTokens != (OptionalInt64{Available: true, Value: 0}) {
		t.Fatalf("recorded zero tokens = %+v, want available zero", got.Slices[0].TotalTokens)
	}
	if got.Slices[1].TotalTokens.Available {
		t.Fatalf("omitted tokens = %+v, want unavailable", got.Slices[1].TotalTokens)
	}
}

func TestProjectFullProjectsOnlyCreatedSliceCommits(t *testing.T) {
	now := time.Date(2026, 8, 4, 15, 0, 0, 0, time.UTC)
	cases := []struct {
		name        string
		status      string
		completion  *plan.SliceCompletionOutcome
		wantOutcome string
		wantSHA     string
	}{
		{name: "committed", status: plan.StatusCompleted, completion: &plan.SliceCompletionOutcome{Outcome: plan.SliceCompletionCommitted, CommitSHA: "abcdef1234567890abcdef1234567890abcdef12"}, wantOutcome: plan.SliceCompletionCommitted, wantSHA: "abcdef1"},
		{name: "committed missing sha", status: plan.StatusCompleted, completion: &plan.SliceCompletionOutcome{Outcome: plan.SliceCompletionCommitted}, wantOutcome: plan.SliceCompletionCommitted},
		{name: "no changes", status: plan.StatusCompleted, completion: &plan.SliceCompletionOutcome{Outcome: plan.SliceCompletionNoChanges, CommitSHA: "abcdef1234567890"}, wantOutcome: plan.SliceCompletionNoChanges},
		{name: "manual", status: plan.StatusCompleted, completion: &plan.SliceCompletionOutcome{Outcome: plan.SliceCompletionManualUncommitted, CommitSHA: "abcdef1234567890"}, wantOutcome: plan.SliceCompletionManualUncommitted},
		{name: "legacy completed", status: plan.StatusCompleted, wantOutcome: "legacy"},
		{name: "not completed", status: plan.StatusPending, wantOutcome: "not_recorded"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			detail := reportFixture(now)
			detail.Slices.Slices[0].Status = tc.status
			detail.Slices.Slices[0].Completion = tc.completion
			got := ProjectFull(detail, now).Slices[0].Commit
			if got.Outcome != tc.wantOutcome || got.SHA.Text.text != tc.wantSHA || got.SHA.Available != (tc.wantSHA != "") {
				t.Fatalf("commit projection = %+v, want outcome %q sha %q", got, tc.wantOutcome, tc.wantSHA)
			}
		})
	}
}

func TestPlanningEffortUsesOnlyValidLegacySummary(t *testing.T) {
	now := time.Date(2026, 8, 4, 15, 0, 0, 0, time.UTC)
	started := now.Add(-95 * time.Second)
	detail := reportFixture(now)
	detail.State.CreatedAt = now
	detail.PlanningSession.Stats = &plan.PlanningSessionStats{
		Agent: "private-agent", SessionID: "private-session", ProviderID: "private-provider", ModelID: "private-model",
		PlanningStartedAt: &started, TotalTokens: 321, TotalMessages: 7,
	}

	planning := ProjectPlanningOnly(detail, now)
	full := ProjectFull(detail, now)
	want := PlanningEffortSummary{
		Available: true, Duration: DurationSummary{Available: true, Seconds: 95},
		TotalTokens: OptionalInt64{Available: true, Value: 321}, TotalMessages: OptionalInt64{Available: true, Value: 7},
	}
	if planning.PlanningEffort != want || full.PlanningEffort != want {
		t.Fatalf("planning effort = planning %+v full %+v, want %+v", planning.PlanningEffort, full.PlanningEffort, want)
	}
	if got := collectSafeText(planning); strings.Contains(got, "private-") {
		t.Fatalf("planning identity entered projection: %q", got)
	}

	detail.PlanningSession.Stats.CaptureSuspect = true
	if got := ProjectPlanningOnly(detail, now).PlanningEffort; got.Available {
		t.Fatalf("invalid planning stats projected: %+v", got)
	}
	detail.PlanningSession.Stats = nil
	if got := ProjectPlanningOnly(detail, now).PlanningEffort; got.Available {
		t.Fatalf("absent planning stats projected: %+v", got)
	}
}

func TestPlanningOnlyProductionTypesExcludeExecutionFields(t *testing.T) {
	for _, tc := range []struct {
		typeOf    reflect.Type
		forbidden []string
	}{
		{reflect.TypeOf(PlanningOnlyReport{}), []string{"Status", "Execution", "Review", "Finalization", "Outcome", "Telemetry", "Verification", "Commit"}},
		{reflect.TypeOf(PlannedSlice{}), []string{"Status", "Rework", "Duration", "Verification", "Commit", "TotalTokens", "Completion", "Timing"}},
		{reflect.TypeOf(PlanningEffortSummary{}), []string{"Agent", "Provider", "Model", "Session", "Prompt", "Cost", "ToolCalls"}},
	} {
		for _, name := range tc.forbidden {
			if _, ok := tc.typeOf.FieldByName(name); ok {
				t.Errorf("%s unexpectedly contains forbidden field %s", tc.typeOf.Name(), name)
			}
		}
	}
}

func TestPlanIdentifierPreservedThroughProjectionAndRendering(t *testing.T) {
	now := time.Date(2026, 8, 4, 15, 0, 0, 0, time.UTC)
	detail := reportFixture(now)
	detail.State.Plan.ID = "20260804-151535-plan-reports"

	report := ProjectPlanningOnly(detail, now)
	if got := report.PlanID.text; got != detail.State.Plan.ID {
		t.Fatalf("projected plan ID = %q, want %q", got, detail.State.Plan.ID)
	}
	markdown, err := RenderPlanningOnly(report)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(markdown), "plan-id: "+detail.State.Plan.ID+"\n") {
		t.Fatalf("rendered report did not preserve plan ID:\n%s", markdown)
	}
}

func TestPlanningReportPreservesCoworkerAccessibleURLsAndPaths(t *testing.T) {
	now := time.Date(2026, 8, 4, 15, 0, 0, 0, time.UTC)
	detail := reportFixture(now)
	values := []string{
		"https://docs.example.com/project/guide",
		"ssh://git.example.com/repository",
		"file:///srv/company/plan.md",
		"//internal.example.com/private",
		"www.example.com/private",
		"portal.example.org:8443/reports?id=project",
		"/workspace/repo",
		"~alice/private/plan.md",
	}
	detail.PlanningBrief.Content = "## User Goal\nRetrieve " + values[0] + ", " + values[2] + ", and " + values[3] + ".\n\n## Constraints\n- Build from " + values[1] + ", " + values[4] + ", and " + values[6] + "\n\n## Risks\n- Check " + values[5] + " and " + values[7] + "\n"

	markdown, err := RenderPlanningOnly(ProjectPlanningOnly(detail, now))
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range values {
		if !strings.Contains(string(markdown), value) {
			t.Errorf("rendered report removed coworker-accessible value %q:\n%s", value, markdown)
		}
	}
}

func TestPlanningOnlyProjectionIsPhaseIndependent(t *testing.T) {
	now := time.Date(2026, 8, 4, 15, 0, 0, 0, time.UTC)
	planned := reportFixture(now)
	completed := reportFixture(now)
	planningStarted := now.Add(-2 * time.Minute)
	for _, detail := range []*plan.PlanDetail{planned, completed} {
		detail.State.CreatedAt = now
		detail.PlanningSession.Stats = &plan.PlanningSessionStats{PlanningStartedAt: &planningStarted, TotalTokens: 55, TotalMessages: 4}
	}
	completed.State.Status = plan.StatusCompleted
	completed.State.UpdatedAt = now.Add(8 * time.Hour)
	completed.State.Plan.PendingSlices = nil
	completed.State.Plan.CompletedSlices = []string{"001-build", "002-ship"}
	for i := range completed.Slices.Slices {
		completed.Slices.Slices[i].Status = plan.StatusCompleted
		completed.Slices.Slices[i].Notes = "execution-tainted-notes"
		completed.Slices.Slices[i].VerificationResults = []plan.VerificationRun{{Command: "execution-tainted-command", CWD: "/execution/tainted", Result: "passed"}}
	}
	completed.Slices.Slices = append(completed.Slices.Slices, plan.Slice{ID: "r101-fix", Title: "execution-tainted-rework", Goal: "execution-tainted-finding"})
	completed.Events = []plan.Event{{Type: plan.EventTypeAgentMetrics, Agent: "execution-tainted-agent", Metrics: &plan.AgentMetrics{SessionID: "execution-tainted-session", OutputTokens: 99}}, {Type: plan.EventTypePlanMerged, MergedDefaultSHA: "execution-tainted-sha"}}
	completed.State.Plan.FinalizationFailure = &plan.FinalizationFailure{Phase: plan.FinalizationFailurePhasePullRequest, Category: "publication_failed", Branch: "execution-tainted-branch", HeadSHA: "execution-tainted-head", FailedAt: now, RecoveryAction: "resume_pull_request"}
	plan.SetPersistedReview(completed, plan.PlanReview{Summary: "execution-tainted-review", FindingsCount: 1})

	left := ProjectPlanningOnly(planned, now)
	right := ProjectPlanningOnly(completed, now)
	if !reflect.DeepEqual(left, right) {
		t.Fatalf("planning projection changed across execution:\nplanned=%#v\ncompleted=%#v", left, right)
	}
	if got := collectSafeText(right); strings.Contains(got, "execution-tainted") {
		t.Fatalf("execution value entered planning projection: %q", got)
	}
	if len(right.Slices) != 2 || !right.Synthesized || right.Mode != ModePlanningOnly {
		t.Fatalf("planning-only shape = %+v", right)
	}
}

func TestProjectionLegacyMissingAndMalformedOptionalData(t *testing.T) {
	now := time.Date(2026, 8, 4, 15, 0, 0, 0, time.UTC)
	detail := reportFixture(now)
	detail.PlanningBrief = plan.PlanningBriefArtifact{Content: "##User Goal\nmalformed secret\n\n## Risks ###\nnot a supported heading"}
	detail.PlanNarrative = plan.PlanNarrativeArtifact{}
	detail.State.GlobalInvariants = nil
	detail.State.OpenQuestions = nil
	detail.Events = nil

	planning := ProjectPlanningOnly(detail, now)
	full := ProjectFull(detail, now)
	if !planning.Planning.Goal.Available { // structured original-slice fallback
		t.Fatal("missing sidecar should use structured goal fallback")
	}
	if len(planning.Planning.Risks) != 0 {
		t.Fatalf("malformed risks heading was accepted: %+v", planning.Planning.Risks)
	}
	if full.Execution.Telemetry.Available || full.Execution.Telemetry.OutputTokens.Available || full.Execution.Telemetry.OutputTokens.Value != 0 {
		t.Fatalf("missing telemetry presented as measured: %+v", full.Execution.Telemetry)
	}
}

func TestPlanningContextSanitizesKnownSectionsAndUsesFallbacks(t *testing.T) {
	now := time.Now()
	detail := reportFixture(now)
	detail.PlanningBrief.Content = "## User Goal\nShip for owner@example.com.\n\n## Constraints\n- Build from /home/owner/work\n\n## Non-goals\n- Dashboard\n\n## Open Questions\n"
	detail.PlanNarrative.Content = "## Decisions\n- Use one stream\n\n## Risks\n- Check URL https://example.com/private\n"
	detail.State.OpenQuestions = []string{"Who approves?"}
	got := ProjectPlanningOnly(detail, now)
	text := collectSafeText(got)
	if strings.Contains(text, "owner@example.com") {
		t.Fatalf("retained personal identifier in %q", text)
	}
	for _, retained := range []string{"/home/owner/work", "https://example.com/private"} {
		if !strings.Contains(text, retained) {
			t.Fatalf("removed coworker-accessible context %q from %q", retained, text)
		}
	}
	if len(got.Planning.Constraints) != 1 || len(got.Planning.NonGoals) != 1 || len(got.Planning.Decisions) != 1 || len(got.Planning.Risks) != 1 || len(got.Planning.Questions) != 1 {
		t.Fatalf("planning context = %+v", got.Planning)
	}
}

func reportFixture(now time.Time) *plan.PlanDetail {
	started := now.Add(-2 * time.Minute)
	return &plan.PlanDetail{
		State: plan.State{
			Status:           plan.StatusPlanned,
			Plan:             plan.PlanState{ID: "leadership-report", Title: "Leadership Report", PendingSlices: []string{"001-build", "002-ship"}, Timing: plan.PlanTiming{StartedAt: &started}},
			GlobalInvariants: []string{"Keep reports share-safe"},
			OpenQuestions:    []string{"Who receives the report?"},
		},
		Slices: plan.SlicesFile{Slices: []plan.Slice{
			{ID: "001-build", Title: "Build projection", Status: plan.StatusPending, Goal: "Create safe models", Context: "Renderers need an allowlist"},
			{ID: "002-ship", Title: "Ship report", Status: plan.StatusPending, Goal: "Expose the report", Context: "Leaders need snapshots", DependsOn: []string{"001-build"}},
		}},
		PlanningBrief: plan.PlanningBriefArtifact{Content: "# Planning Brief\n\n## User Goal\nShare a leadership snapshot.\n\n## Constraints\n- Keep it safe\n\n## Non-goals\n- Raw export\n\n## Open Questions\n- Who receives it?\n"},
		PlanNarrative: plan.PlanNarrativeArtifact{Content: "# Plan\n\n## Decisions\n- Use typed projections\n\n## Risks\n- Sensitive text\n"},
	}
}

func collectSafeText(value any) string {
	var out []string
	var visit func(reflect.Value)
	visit = func(v reflect.Value) {
		if !v.IsValid() {
			return
		}
		if v.Kind() == reflect.Interface || v.Kind() == reflect.Pointer {
			if !v.IsNil() {
				visit(v.Elem())
			}
			return
		}
		if v.Type() == reflect.TypeOf(SafeText{}) {
			out = append(out, v.FieldByName("text").String())
			return
		}
		switch v.Kind() {
		case reflect.Struct:
			for i := 0; i < v.NumField(); i++ {
				visit(v.Field(i))
			}
		case reflect.Slice, reflect.Array:
			for i := 0; i < v.Len(); i++ {
				visit(v.Index(i))
			}
		}
	}
	visit(reflect.ValueOf(value))
	return strings.Join(out, "\n")
}
