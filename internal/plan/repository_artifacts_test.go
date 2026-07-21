package plan

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPlanningSessionDuration(t *testing.T) {
	created := time.Date(2026, 5, 3, 21, 0, 0, 0, time.UTC)
	updated := created.Add(5*time.Minute + 750*time.Millisecond)
	if got := PlanningSessionDuration(&PlanningSessionStats{TimeCreated: &created, TimeUpdated: &updated}); got != 5*time.Minute+1*time.Second {
		t.Fatalf("expected rounded planning duration, got %s", got)
	}

	if got := PlanningSessionDuration(&PlanningSessionStats{TimeCreated: &created}); got != 0 {
		t.Fatalf("expected missing timestamp to return zero, got %s", got)
	}
	invalidUpdated := created.Add(-time.Second)
	if got := PlanningSessionDuration(&PlanningSessionStats{TimeCreated: &created, TimeUpdated: &invalidUpdated}); got != 0 {
		t.Fatalf("expected invalid timestamp order to return zero, got %s", got)
	}
}

func TestSummarizePlanningSessionMetricsUsesPlanningStartedAt(t *testing.T) {
	planCreatedAt := time.Date(2026, 5, 3, 21, 6, 30, 400*int(time.Millisecond), time.UTC)
	planningStartedAt := planCreatedAt.Add(-2*time.Minute - 1250*time.Millisecond)
	sessionCreatedAt := planCreatedAt.Add(-10 * time.Minute)
	sessionUpdatedAt := planCreatedAt.Add(30 * time.Second)
	stats := &PlanningSessionStats{
		PlanningStartedAt: &planningStartedAt,
		TimeCreated:       &sessionCreatedAt,
		TimeUpdated:       &sessionUpdatedAt,
		TotalTokens:       150,
		TotalMessages:     3,
	}

	summary := SummarizePlanningSessionMetrics(stats, planCreatedAt)
	if !summary.Present || !summary.Valid || summary.Duration != 2*time.Minute+1*time.Second || summary.TotalTokens != 150 || summary.TotalMessages != 3 {
		t.Fatalf("unexpected planning metrics summary: %+v", summary)
	}
	if !PlanningSessionMetricsValid(stats, planCreatedAt) {
		t.Fatal("expected planning metrics to be valid")
	}
}

func TestSummarizePlanningSessionMetricsFallsBackFromZeroPlanningStartedAt(t *testing.T) {
	planCreatedAt := time.Date(2026, 5, 3, 21, 6, 30, 0, time.UTC)
	planningStartedAt := planCreatedAt
	sessionCreatedAt := planCreatedAt.Add(-4 * time.Minute)
	sessionUpdatedAt := planCreatedAt.Add(30 * time.Second)
	stats := &PlanningSessionStats{
		PlanningStartedAt: &planningStartedAt,
		TimeCreated:       &sessionCreatedAt,
		TimeUpdated:       &sessionUpdatedAt,
		TotalTokens:       150,
		TotalMessages:     3,
	}

	summary := SummarizePlanningSessionMetrics(stats, planCreatedAt)
	if !summary.Present || !summary.Valid || summary.Duration != 4*time.Minute || summary.TotalTokens != 150 || summary.TotalMessages != 3 {
		t.Fatalf("unexpected fallback planning metrics summary: %+v", summary)
	}
}

func TestSummarizePlanningSessionMetricsFallsBackFromFuturePlanningStartedAt(t *testing.T) {
	planCreatedAt := time.Date(2026, 5, 3, 21, 6, 30, 0, time.UTC)
	planningStartedAt := planCreatedAt.Add(30 * time.Second)
	sessionCreatedAt := planCreatedAt.Add(-3 * time.Minute)
	sessionUpdatedAt := planCreatedAt.Add(30 * time.Second)
	stats := &PlanningSessionStats{
		PlanningStartedAt: &planningStartedAt,
		TimeCreated:       &sessionCreatedAt,
		TimeUpdated:       &sessionUpdatedAt,
		TotalTokens:       150,
		TotalMessages:     3,
	}

	summary := SummarizePlanningSessionMetrics(stats, planCreatedAt)
	if !summary.Present || !summary.Valid || summary.Duration != 3*time.Minute || summary.TotalTokens != 150 || summary.TotalMessages != 3 {
		t.Fatalf("unexpected fallback planning metrics summary: %+v", summary)
	}
}

func TestSummarizePlanningSessionMetricsHidesSuspectStats(t *testing.T) {
	planCreatedAt := time.Date(2026, 5, 3, 21, 6, 30, 0, time.UTC)
	planningStartedAt := planCreatedAt.Add(-2 * time.Minute)
	stats := &PlanningSessionStats{
		PlanningStartedAt:    &planningStartedAt,
		CaptureSuspect:       true,
		CaptureSuspectReason: "stale planning session",
		TotalTokens:          150,
		TotalMessages:        3,
	}

	summary := SummarizePlanningSessionMetrics(stats, planCreatedAt)
	if !summary.Present || summary.Valid || summary.Duration != 0 || summary.TotalTokens != 0 || summary.TotalMessages != 0 {
		t.Fatalf("suspect stats should not surface metrics: %+v", summary)
	}
	if summary.UnavailableReason != "stale planning session" {
		t.Fatalf("unexpected unavailable reason: %+v", summary)
	}
}

func TestSummarizePlanningSessionMetricsHandlesMissingStats(t *testing.T) {
	summary := SummarizePlanningSessionMetrics(nil, time.Date(2026, 5, 3, 21, 6, 30, 0, time.UTC))
	if summary.Present || summary.Valid || summary.UnavailableReason != "" || summary.Duration != 0 || summary.TotalTokens != 0 || summary.TotalMessages != 0 {
		t.Fatalf("missing stats should stay quiet: %+v", summary)
	}
}

func TestSummarizePlanningSessionMetricsRejectsStaleLegacyStats(t *testing.T) {
	planCreatedAt := time.Date(2026, 5, 3, 21, 6, 30, 0, time.UTC)
	sessionCreatedAt := planCreatedAt.Add(-10 * time.Minute)
	sessionUpdatedAt := planCreatedAt.Add(-5 * time.Minute)
	stats := &PlanningSessionStats{
		TimeCreated:   &sessionCreatedAt,
		TimeUpdated:   &sessionUpdatedAt,
		TotalTokens:   150,
		TotalMessages: 3,
	}

	summary := SummarizePlanningSessionMetrics(stats, planCreatedAt)
	if !summary.Present || summary.Valid || summary.Duration != 0 || summary.TotalTokens != 0 || summary.TotalMessages != 0 {
		t.Fatalf("stale legacy stats should not surface metrics: %+v", summary)
	}
	if summary.UnavailableReason == "" {
		t.Fatalf("expected stale legacy stats to report an unavailable reason: %+v", summary)
	}
}

func TestSummarizePlanningSessionMetricsAllowsSafeLegacyFallback(t *testing.T) {
	planCreatedAt := time.Date(2026, 5, 3, 21, 6, 30, 250*int(time.Millisecond), time.UTC)
	sessionCreatedAt := planCreatedAt.Add(-4*time.Minute - 750*time.Millisecond)
	sessionUpdatedAt := planCreatedAt.Add(30 * time.Second)
	stats := &PlanningSessionStats{
		TimeCreated:   &sessionCreatedAt,
		TimeUpdated:   &sessionUpdatedAt,
		TotalTokens:   150,
		TotalMessages: 3,
	}

	summary := SummarizePlanningSessionMetrics(stats, planCreatedAt)
	if !summary.Present || !summary.Valid || summary.Duration != 4*time.Minute+1*time.Second || summary.TotalTokens != 150 || summary.TotalMessages != 3 {
		t.Fatalf("unexpected legacy planning metrics summary: %+v", summary)
	}
}

func TestSummarizePopulatesPlanningSessionFields(t *testing.T) {
	planCreatedAt := time.Date(2026, 5, 3, 21, 5, 0, 0, time.UTC)
	planningStartedAt := planCreatedAt.Add(-5 * time.Minute)
	detail := &PlanDetail{
		State: State{CreatedAt: planCreatedAt, Plan: PlanState{ID: "plan", Title: "Plan"}},
		PlanningSession: PlanningSessionArtifacts{Stats: &PlanningSessionStats{
			PlanningStartedAt: &planningStartedAt,
			TotalTokens:       150,
			TotalMessages:     3,
		}},
	}

	summary := Summarize(detail, time.Time{})
	if !summary.PlanningSessionPresent || !summary.PlanningSessionValid || summary.PlanningSessionDuration != 5*time.Minute || summary.PlanningSessionTotalTokens != 150 || summary.PlanningSessionTotalMessages != 3 {
		t.Fatalf("unexpected planning-session summary fields: %+v", summary)
	}

	detail.PlanningSession.Stats = nil
	summary = Summarize(detail, time.Time{})
	if summary.PlanningSessionPresent || summary.PlanningSessionValid || summary.PlanningSessionDuration != 0 || summary.PlanningSessionTotalTokens != 0 || summary.PlanningSessionTotalMessages != 0 {
		t.Fatalf("missing planning stats should preserve zero summary fields: %+v", summary)
	}
}

func TestSummarizePopulatesPullRequest(t *testing.T) {
	createdAt := time.Date(2026, 5, 26, 18, 30, 0, 0, time.UTC)
	detail := &PlanDetail{State: State{Plan: PlanState{
		ID:    "plan",
		Title: "Plan",
		PullRequest: &PullRequest{
			Number:    1234,
			URL:       "https://github.com/iamseth/tao/pull/1234",
			CreatedAt: createdAt,
		},
	}}}

	summary := Summarize(detail, time.Time{})
	if summary.PullRequest == nil || summary.PullRequest.Number != 1234 || summary.PullRequest.URL != "https://github.com/iamseth/tao/pull/1234" {
		t.Fatalf("expected pull request metadata in summary, got %+v", summary.PullRequest)
	}

	detail.State.Plan.PullRequest = nil
	summary = Summarize(detail, time.Time{})
	if summary.PullRequest != nil {
		t.Fatalf("expected missing pull request metadata to stay nil, got %+v", summary.PullRequest)
	}
}

func TestPlanLoadsFormalVerificationAndApproval(t *testing.T) {
	dir := t.TempDir()
	writePlan(t, dir, "formal", `{
  "schema":"tao.plan.state.v1",
  "status":"planned",
  "created_at":"2026-04-27T18:10:50Z",
  "updated_at":"2026-04-27T18:10:50Z",
  "repo":{"name":"rollcall","root":"/repo","branch":"main"},
  "plan":{"id":"formal","title":"Formal Plan","current_slice":null,"completed_slices":[],"pending_slices":["001-a"],"timing":{"started_at":null,"completed_at":null,"last_activity_at":"2026-04-27T18:10:50Z"}},
  "global_invariants":[],"open_questions":[]
}`, `{
  "schema":"tao.plan.slices.v1","plan_id":"formal","execution":{"mode":"serial","parallel_safe":false},
  "slices":[{
    "id":"001-a",
    "title":"A",
    "status":"pending",
    "depends_on":[],
    "timing":{"created_at":"2026-04-27T18:10:50Z","started_at":null,"completed_at":null,"updated_at":"2026-04-27T18:10:50Z","last_activity_at":null,"duration_seconds":null},
    "goal":"",
    "context":"",
    "tasks":[],
    "expected_files":[],
    "verification":{
      "commands":["cd services/Foo && pnpm test"],
      "source":"services/Foo/package.json scripts.test",
      "steps":[{"command":"pnpm test","cwd":"services/Foo","source":"services/Foo/package.json scripts.test","reason":"service-local test script"}],
      "manual_checks":["Review final diff"]
    },
    "approval":{"required":true,"reason":"Cutover approval required","approved":false}
  }]
}`)

	detail, err := NewFileRepository(dir).GetPlan(context.Background(), "formal")
	if err != nil {
		t.Fatal(err)
	}
	slice := detail.Slices.Slices[0]
	if slice.Verification.Source != "services/Foo/package.json scripts.test" {
		t.Fatalf("unexpected verification source %q", slice.Verification.Source)
	}
	if len(slice.Verification.Steps) != 1 || slice.Verification.Steps[0].CWD != "services/Foo" {
		t.Fatalf("unexpected verification steps: %+v", slice.Verification.Steps)
	}
	if slice.Approval == nil || !slice.Approval.Required || slice.Approval.Approved || slice.Approval.Reason == "" {
		t.Fatalf("unexpected approval: %+v", slice.Approval)
	}
}

func TestPlanLoadsPlanningSessionArtifacts(t *testing.T) {
	dir := t.TempDir()
	writeMinimalPlan(t, dir, "capture", "Capture Plan")
	planDir := filepath.Join(dir, "capture")
	if err := os.WriteFile(filepath.Join(planDir, PlanningSessionExportFile), []byte(`{"session":"native export"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(planDir, PlanningSessionStatsFile), []byte(`{
  "schema":"tao.planning_session.stats.v1",
  "plan_id":"capture",
  "session_id":"session-1",
  "repository_root":"/repo",
  "time_created":"2026-05-03T21:00:00Z",
  "time_updated":"2026-05-03T21:05:00Z",
  "capture_status":"captured",
  "provider_id":"anthropic",
  "model_id":"claude",
  "input_tokens":100,
  "output_tokens":50,
  "total_tokens":150,
  "cost":0.01,
  "total_messages":3,
  "user_messages":1,
  "assistant_messages":2,
  "tool_calls":4,
  "export_sanitized":true,
  "export_status":"completed",
  "prompt_extracted":true,
  "prompt_extraction_source":"part",
  "prompt_message_rows_examined":1,
  "prompt_part_rows_examined":2,
  "prompt_bytes":12,
  "prompt_lines":1
}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(planDir, PlanningPromptFile), []byte("build a plan\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	detail, err := NewFileRepository(dir).GetPlan(context.Background(), "capture")
	if err != nil {
		t.Fatal(err)
	}
	if !detail.PlanningSession.HasExport || detail.PlanningSession.ExportPath == "" {
		t.Fatalf("expected planning-session export presence, got %+v", detail.PlanningSession)
	}
	stats := detail.PlanningSession.Stats
	if stats == nil || stats.SessionID != "session-1" || stats.ProviderID != "anthropic" || stats.ModelID != "claude" {
		t.Fatalf("unexpected planning-session stats: %+v", stats)
	}
	if stats.TotalTokens != 150 || stats.ToolCalls != 4 || !stats.ExportSanitized || !stats.PromptExtracted || stats.CaptureStatus != "captured" {
		t.Fatalf("unexpected planning-session totals: %+v", stats)
	}
	if stats.PromptExtractionSource != "part" || stats.PromptMessageRows != 1 || stats.PromptPartRows != 2 {
		t.Fatalf("unexpected planning prompt diagnostics: %+v", stats)
	}
	if detail.PlanningSession.Prompt != "build a plan\n" || detail.PlanningSession.PromptPath == "" {
		t.Fatalf("unexpected planning prompt: %+v", detail.PlanningSession)
	}
}

func TestPlanWarnsWhenPlanningPromptExtractionFailed(t *testing.T) {
	dir := t.TempDir()
	writeMinimalPlan(t, dir, "capture", "Capture Plan")
	planDir := filepath.Join(dir, "capture")
	if err := os.WriteFile(filepath.Join(planDir, PlanningSessionStatsFile), []byte(`{
  "schema":"tao.planning_session.stats.v1",
  "session_id":"session-1",
  "capture_status":"prompt_extraction_empty",
  "export_sanitized":true,
  "prompt_extracted":false,
  "prompt_extraction_note":"user planning messages contained no text (message_rows=3, part_rows=10)",
  "prompt_extraction_failure":"user planning messages contained no text (message_rows=3, part_rows=10)",
  "prompt_message_rows_examined":3,
  "prompt_part_rows_examined":10
}`), 0o600); err != nil {
		t.Fatal(err)
	}

	detail, err := NewFileRepository(dir).GetPlan(context.Background(), "capture")
	if err != nil {
		t.Fatal(err)
	}
	warnings := strings.Join(detail.Warnings, "\n")
	if !strings.Contains(warnings, "planning prompt extraction failed") || !strings.Contains(warnings, "message_rows=3") {
		t.Fatalf("expected planning prompt extraction warning, got %v", detail.Warnings)
	}
}

func TestRepositoryLoadsPlanningBriefArtifact(t *testing.T) {
	dir := t.TempDir()
	writeMinimalPlan(t, dir, "brief", "Brief Plan")
	planDir := filepath.Join(dir, "brief")
	if err := os.WriteFile(filepath.Join(planDir, PlanningBriefFile), []byte(`# Planning Brief

## User Goal
Do less.

## Constraints
Keep it small.

## Non-goals
No unrelated work.

## Expected Files/Packages
- internal/plan

## Validation Strategy
Run focused tests.

## Open Questions
None.
`), 0o600); err != nil {
		t.Fatal(err)
	}

	detail, err := NewFileRepository(dir).GetPlan(context.Background(), "brief")
	if err != nil {
		t.Fatal(err)
	}
	if detail.PlanningBrief.Path == "" || !strings.Contains(detail.PlanningBrief.Content, "Do less.") {
		t.Fatalf("unexpected planning brief: %+v", detail.PlanningBrief)
	}
	if len(detail.Warnings) != 0 {
		t.Fatalf("complete planning brief should not warn, got %v", detail.Warnings)
	}
}

func TestRepositoryIgnoresHistoricalPreviewExtraFile(t *testing.T) {
	dir := t.TempDir()
	writeMinimalPlan(t, dir, "historical", "Historical Plan")
	planDir := filepath.Join(dir, "historical")
	if err := os.WriteFile(filepath.Join(planDir, PlanningBriefFile), []byte(completePlanningBriefMarkdown()), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(planDir, "plan-"+"preview.md"), []byte("historical preview data"), 0o600); err != nil {
		t.Fatal(err)
	}

	detail, err := NewFileRepository(dir).GetPlan(context.Background(), "historical")
	if err != nil {
		t.Fatal(err)
	}
	if detail.State.Status != StatusPlanned || detail.State.Plan.ID != "historical" {
		t.Fatalf("historical extra file affected lifecycle: status=%q id=%q", detail.State.Status, detail.State.Plan.ID)
	}
	if len(detail.Warnings) != 0 {
		t.Fatalf("historical extra file affected validation: %v", detail.Warnings)
	}
}

func TestRepositoryLoadsPlanNarrativeArtifact(t *testing.T) {
	dir := t.TempDir()
	writeMinimalPlan(t, dir, "narrative", "Narrative Plan")
	planDir := filepath.Join(dir, "narrative")
	if err := os.WriteFile(filepath.Join(planDir, PlanningBriefFile), []byte(completePlanningBriefMarkdown()), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(planDir, PlanMarkdownFile), []byte("# Plan\n\nShip the intended outcome.\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	detail, err := NewFileRepository(dir).GetPlan(context.Background(), "narrative")
	if err != nil {
		t.Fatal(err)
	}
	if detail.PlanNarrative.Path == "" || !strings.Contains(detail.PlanNarrative.Content, "intended outcome") {
		t.Fatalf("unexpected plan narrative: %+v", detail.PlanNarrative)
	}
	if len(detail.Warnings) != 0 {
		t.Fatalf("complete optional artifacts should not warn, got %v", detail.Warnings)
	}
}

func TestRepositoryDoesNotWarnWhenPlanNarrativeIsMissing(t *testing.T) {
	dir := t.TempDir()
	writeMinimalPlan(t, dir, "legacy", "Legacy Plan")
	planDir := filepath.Join(dir, "legacy")
	if err := os.WriteFile(filepath.Join(planDir, PlanningBriefFile), []byte(completePlanningBriefMarkdown()), 0o600); err != nil {
		t.Fatal(err)
	}

	detail, err := NewFileRepository(dir).GetPlan(context.Background(), "legacy")
	if err != nil {
		t.Fatal(err)
	}
	if detail.PlanNarrative.Content != "" || detail.PlanNarrative.Path != "" {
		t.Fatalf("expected no plan narrative, got %+v", detail.PlanNarrative)
	}
	warnings := strings.Join(detail.Warnings, "\n")
	if strings.Contains(warnings, PlanMarkdownFile) {
		t.Fatalf("missing plan.md should not warn, got %v", detail.Warnings)
	}
}

func TestPlanWarnsWhenPlanningBriefIsMissingOrMalformed(t *testing.T) {
	dir := t.TempDir()
	writeMinimalPlan(t, dir, "missing", "Missing Brief")

	detail, err := NewFileRepository(dir).GetPlan(context.Background(), "missing")
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.Warnings) == 0 || !strings.Contains(strings.Join(detail.Warnings, "\n"), "planning-brief.md missing") {
		t.Fatalf("expected missing brief warning, got %v", detail.Warnings)
	}

	writeMinimalPlan(t, dir, "malformed", "Malformed Brief")
	planDir := filepath.Join(dir, "malformed")
	if err := os.WriteFile(filepath.Join(planDir, PlanningBriefFile), []byte("# Planning Brief\n\n## User Goal\nDo less.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	detail, err = NewFileRepository(dir).GetPlan(context.Background(), "malformed")
	if err != nil {
		t.Fatal(err)
	}
	warnings := strings.Join(detail.Warnings, "\n")
	for _, want := range []string{"Constraints", "Expected Files/Packages", "Validation Strategy"} {
		if !strings.Contains(warnings, want) {
			t.Fatalf("expected malformed brief warning %q in %q", want, warnings)
		}
	}
}

func TestPlanLoadsLegacyPlanWithoutPlanningSessionArtifacts(t *testing.T) {
	dir := t.TempDir()
	writeMinimalPlan(t, dir, "legacy", "Legacy Plan")

	detail, err := NewFileRepository(dir).GetPlan(context.Background(), "legacy")
	if err != nil {
		t.Fatal(err)
	}
	if detail.PlanningSession.HasExport || detail.PlanningSession.Stats != nil || detail.PlanningSession.Prompt != "" {
		t.Fatalf("expected no planning-session artifacts, got %+v", detail.PlanningSession)
	}
	warnings := strings.Join(detail.Warnings, "\n")
	if strings.Contains(warnings, "planning-session") {
		t.Fatalf("missing planning-session artifacts should not warn, got %v", detail.Warnings)
	}
}

func TestRepositoryLoadsAgentMetricsEvents(t *testing.T) {
	dir := t.TempDir()
	writeMinimalPlan(t, dir, "metrics", "Metrics Plan")
	if err := os.WriteFile(filepath.Join(dir, "metrics", "events.jsonl"), []byte(`{"type":"agent_metrics","timestamp":"2026-05-01T23:00:00Z","plan_id":"metrics","slice_id":"001-a","metrics":{"session_id":"session-1","provider_id":"anthropic","model_id":"claude","status":"completed","input_tokens":10,"output_tokens":5,"total_tokens":15,"cost":0.01,"tool_calls":2},"message":"captured metrics"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	detail, err := NewFileRepository(dir).GetPlan(context.Background(), "metrics")
	if err != nil {
		t.Fatal(err)
	}
	metrics := AgentMetricsEvents(detail.Events)
	if len(metrics) != 1 {
		t.Fatalf("expected one metrics event, got %d", len(metrics))
	}
	if metrics[0].Metrics.SessionID != "session-1" || metrics[0].Metrics.TotalTokens != 15 || metrics[0].Metrics.ToolCalls != 2 {
		t.Fatalf("unexpected metrics event: %+v", metrics[0])
	}
}
