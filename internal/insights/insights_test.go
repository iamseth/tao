package insights

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/iamseth/tao/internal/plan"
)

type fixtureLister struct {
	summaries []plan.PlanSummary
	err       error
}

func (l fixtureLister) ListPlans(context.Context, plan.PlanFilter) ([]plan.PlanSummary, error) {
	return l.summaries, l.err
}

type fixtureSourceLister struct {
	sources []RepositorySource
	err     error
}

func (l fixtureSourceLister) ListInsightSources(context.Context) ([]RepositorySource, error) {
	return l.sources, l.err
}

func TestAggregateStreamsPlanHistoriesLeniently(t *testing.T) {
	root := "testdata"
	report, err := Aggregate(context.Background(), fixtureLister{summaries: []plan.PlanSummary{
		{ID: "alpha", Dir: filepath.Join(root, "plan-alpha")},
		{ID: "beta", Dir: filepath.Join(root, "plan-beta")},
		{ID: "broken", Dir: filepath.Join(root, "not-a-dir")},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if report.PlansScanned != 2 || report.PlansSkipped != 1 {
		t.Fatalf("plan counts = scanned %d skipped %d", report.PlansScanned, report.PlansSkipped)
	}
	if len(report.BlockedReasons) != 1 {
		t.Fatalf("blocked buckets = %#v", report.BlockedReasons)
	}
	bucket := report.BlockedReasons[0]
	if bucket.Reason != "unreachable_service" || bucket.Count != 3 {
		t.Fatalf("blocked bucket = %#v", bucket)
	}
	if len(bucket.Exemplars) != 2 || bucket.Exemplars[0] != "External service unreachable while verifying" || bucket.Exemplars[1] != "connection refused by test database" {
		t.Fatalf("blocked exemplars = %#v", bucket.Exemplars)
	}
	if len(report.ReworkPlans) != 1 || report.ReworkPlans[0].PlanID != "alpha" || report.ReworkPlans[0].Rounds != 3 {
		t.Fatalf("rework plans = %#v", report.ReworkPlans)
	}
	if len(report.ReworkPlans[0].StoppedReasons) != 1 || report.ReworkPlans[0].StoppedReasons[0] != "automatic rework cap exhausted after 3 cycles" {
		t.Fatalf("stop reasons = %#v", report.ReworkPlans[0].StoppedReasons)
	}
	wantSignals := (SignalCounts{SessionTimeout: 1, SliceResumeFailed: 1, VerificationCommandInvalid: 1, PlanCommitFallback: 1, PlanCommitGuard: 1})
	if report.Signals != wantSignals {
		t.Fatalf("signals = %#v, want %#v", report.Signals, wantSignals)
	}
	if report.OutputTokens.Sessions != 20 || report.OutputTokens.P50 != 100 || report.OutputTokens.P90 != 180 || report.OutputTokens.P95 != 190 {
		t.Fatalf("output percentiles = %#v", report.OutputTokens)
	}
	if report.Cost.Sessions != 20 || report.Cost.P50 != 10 || report.Cost.P90 != 18 || report.Cost.P95 != 19 {
		t.Fatalf("cost percentiles = %#v", report.Cost)
	}
	if len(report.OutlierPlans) != 1 || report.OutlierPlans[0].PlanID != "beta" || !report.OutlierPlans[0].OutputTokensOutlier || !report.OutlierPlans[0].CostOutlier {
		t.Fatalf("outliers = %#v", report.OutlierPlans)
	}
}

func TestAggregateBuildsSignalEvidenceFromEnclosingPlan(t *testing.T) {
	dir := t.TempDir()
	events := "" +
		`{"type":"session_timeout","timestamp":"2026-08-18T20:00:00-04:00","plan_id":"untrusted-a"}` + "\n" +
		`{"type":"session_timeout","plan_id":"untrusted-b"}` + "\n" +
		`{"type":"session_timeout","timestamp":"2026-08-18T22:00:00Z"}` + "\n" +
		`{"type":"slice_resume_attempted","timestamp":"2026-08-18T23:00:00Z"}` + "\n" +
		`{"type":"slice_resume_attempted"}` + "\n" +
		`{"type":"slice_resume_failed"}` + "\n" +
		`{"type":"verification_command_invalid","timestamp":"2026-08-18T21:00:00Z"}` + "\n" +
		`{"type":"plan_commit_fallback"}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, "events.jsonl"), []byte(events), 0o600); err != nil {
		t.Fatal(err)
	}

	report, err := Aggregate(context.Background(), fixtureLister{summaries: []plan.PlanSummary{{ID: "actual-plan", Dir: dir}}})
	if err != nil {
		t.Fatal(err)
	}
	latest := time.Date(2026, 8, 19, 0, 0, 0, 0, time.UTC)
	wantTimeout := SignalObservation{Count: 3, Plans: 1, Repositories: 1, MissingTimestamps: 1, LatestTimestamp: &latest}
	if got := report.SignalEvidence.SessionTimeout; got.Count != wantTimeout.Count || got.Plans != wantTimeout.Plans || got.Repositories != wantTimeout.Repositories || got.MissingTimestamps != wantTimeout.MissingTimestamps || got.LatestTimestamp == nil || !got.LatestTimestamp.Equal(*wantTimeout.LatestTimestamp) {
		t.Fatalf("session timeout evidence = %#v, want %#v", got, wantTimeout)
	}
	if got := report.SignalEvidence.SliceResumeAttempted; got.Count != 2 || got.Plans != 1 || got.Repositories != 1 || got.MissingTimestamps != 1 || got.LatestTimestamp == nil {
		t.Fatalf("resume attempt evidence = %#v", got)
	}
	if got := report.SignalEvidence.SliceResumeFailed; got.Count != 1 || got.Plans != 1 || got.Repositories != 1 || got.MissingTimestamps != 1 || got.LatestTimestamp != nil {
		t.Fatalf("resume failure evidence = %#v", got)
	}
	if got := report.SignalEvidence.PlanCommitGuard; got != (SignalObservation{}) {
		t.Fatalf("zero-count commit guard evidence = %#v", got)
	}
	wantCounts := SignalCounts{SessionTimeout: 3, SliceResumeFailed: 1, VerificationCommandInvalid: 1, PlanCommitFallback: 1}
	if report.Signals != wantCounts {
		t.Fatalf("derived signal counts = %#v, want %#v", report.Signals, wantCounts)
	}
}

func TestAggregateSourcesQualifiesSignalPlanAndRepositoryBreadth(t *testing.T) {
	writeEvents := func(event string) string {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "events.jsonl"), []byte(event+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		return dir
	}
	first := writeEvents(`{"type":"session_timeout","timestamp":"2026-08-18T21:00:00Z","plan_id":"wrong"}`)
	second := writeEvents(`{"type":"session_timeout","timestamp":"2026-08-18T22:00:00Z","plan_id":"wrong"}`)

	report, err := AggregateSources(context.Background(), fixtureSourceLister{sources: []RepositorySource{
		{ID: "repo-a", Plans: fixtureLister{summaries: []plan.PlanSummary{{ID: "duplicate", Dir: first}}}},
		{ID: "repo-b", Plans: fixtureLister{summaries: []plan.PlanSummary{{ID: "duplicate", Dir: second}}}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	got := report.SignalEvidence.SessionTimeout
	if got.Count != 2 || got.Plans != 2 || got.Repositories != 2 || got.LatestTimestamp == nil || !got.LatestTimestamp.Equal(time.Date(2026, 8, 18, 22, 0, 0, 0, time.UTC)) {
		t.Fatalf("qualified signal evidence = %#v", got)
	}
}

func TestAggregateSkipsInvalidSummaryWithReadableTelemetry(t *testing.T) {
	dir := t.TempDir()
	events := `{"type":"agent_metrics","metrics":{"session_id":"invalid","output_tokens":999,"cost":99}}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, "events.jsonl"), []byte(events), 0o600); err != nil {
		t.Fatal(err)
	}
	report, err := Aggregate(context.Background(), fixtureLister{summaries: []plan.PlanSummary{{
		ID:     "invalid",
		Dir:    dir,
		Status: plan.StatusInvalid,
	}}})
	if err != nil {
		t.Fatal(err)
	}
	if report.PlansScanned != 0 || report.PlansSkipped != 1 {
		t.Fatalf("plan counts = scanned %d skipped %d", report.PlansScanned, report.PlansSkipped)
	}
	if report.OutputTokens.Sessions != 0 || report.Cost.Sessions != 0 || len(report.OutlierPlans) != 0 {
		t.Fatalf("invalid plan telemetry included: output %#v cost %#v outliers %#v", report.OutputTokens, report.Cost, report.OutlierPlans)
	}
}

func TestAggregateSumsAttemptsWithinSessionUsingPlanTelemetry(t *testing.T) {
	dir := t.TempDir()
	events := "" +
		`{"type":"agent_metrics","metrics":{"session_id":"same","output_tokens":4,"cost":0.25}}` + "\n" +
		`{"type":"agent_metrics","metrics":{"session_id":"same","output_tokens":6,"cost":0.75}}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, "events.jsonl"), []byte(events), 0o600); err != nil {
		t.Fatal(err)
	}
	report, err := Aggregate(context.Background(), fixtureLister{summaries: []plan.PlanSummary{{ID: "one", Dir: dir}}})
	if err != nil {
		t.Fatal(err)
	}
	if report.OutputTokens.Sessions != 1 || report.OutputTokens.P95 != 10 || report.Cost.P95 != 1 {
		t.Fatalf("percentiles = output %#v cost %#v", report.OutputTokens, report.Cost)
	}
}

func TestAggregateSourcesQualifiesDuplicatePlansAndReportsPartialCoverage(t *testing.T) {
	writeEvents := func(name, events string) string {
		dir := filepath.Join(t.TempDir(), name)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "events.jsonl"), []byte(events), 0o600); err != nil {
			t.Fatal(err)
		}
		return dir
	}
	var firstEvents strings.Builder
	firstEvents.WriteString("" +
		`{"type":"slice_blocked","reason":"network unavailable"}` + "\n" +
		`{"type":"slice_blocked","reason":"service unavailable"}` + "\n" +
		`{"type":"rework_round","round":1}` + "\n" +
		`{"type":"rework_round","round":2}` + "\n" +
		`{"type":"rework_round","round":3}` + "\n" +
		`{"type":"agent_metrics","metrics":{"session_id":"same","output_tokens":1,"cost":0}}` + "\n")
	for i := 2; i <= 19; i++ {
		_, _ = fmt.Fprintf(&firstEvents, "{\"type\":\"agent_metrics\",\"metrics\":{\"session_id\":\"first-%d\",\"output_tokens\":%d,\"cost\":0}}\n", i, i)
	}
	first := writeEvents("first", firstEvents.String())
	second := writeEvents("second", ""+
		`{"type":"slice_blocked","reason":"external service unreachable"}`+"\n"+
		`{"type":"rework_round","round":1}`+"\n"+
		`{"type":"rework_round","round":2}`+"\n"+
		`{"type":"rework_round","round":3}`+"\n"+
		`{"type":"agent_metrics","metrics":{"session_id":"same","output_tokens":100,"cost":100}}`+"\n")

	report, err := AggregateSources(context.Background(), fixtureSourceLister{sources: []RepositorySource{
		{ID: "repo-b", Name: "Beta", Plans: fixtureLister{summaries: []plan.PlanSummary{{ID: "duplicate", Dir: second}}}},
		{ID: "repo-unreadable", Plans: fixtureLister{err: errors.New("damaged plan store")}},
		{ID: "repo-empty", Plans: fixtureLister{}},
		{ID: "", Name: "invalid", Plans: fixtureLister{}},
		{ID: "repo-a", Name: "Alpha", Plans: fixtureLister{summaries: []plan.PlanSummary{{ID: "duplicate", Dir: first}}}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	coverage := report.RepositoryCoverage
	if coverage.Scanned != 2 || coverage.Skipped != 1 || coverage.Unreadable != 1 || coverage.Empty != 1 {
		t.Fatalf("coverage = %#v", coverage)
	}
	wantStatuses := []string{"skipped", "scanned", "scanned", "empty", "unreadable"}
	for i, want := range wantStatuses {
		if coverage.Repositories[i].Status != want {
			t.Fatalf("coverage repositories = %#v", coverage.Repositories)
		}
	}
	if report.OutputTokens.Sessions != 20 || report.OutputTokens.P50 != 10 || report.OutputTokens.P95 != 19 {
		t.Fatalf("global output percentiles = %#v", report.OutputTokens)
	}
	if len(report.ReworkPlans) != 2 || report.ReworkPlans[0].RepositoryID != "repo-a" || report.ReworkPlans[1].RepositoryID != "repo-b" {
		t.Fatalf("qualified rework plans = %#v", report.ReworkPlans)
	}
	if len(report.BlockedReasons) != 1 || len(report.BlockedReasons[0].QualifiedExemplars) != 2 {
		t.Fatalf("qualified blocked evidence = %#v", report.BlockedReasons)
	}
	bucket := report.BlockedReasons[0]
	if bucket.QualifiedExemplars[0].RepositoryID != "repo-a" || bucket.QualifiedExemplars[1].RepositoryID != "repo-a" {
		t.Fatalf("bounded blocked exemplars = %#v", bucket.QualifiedExemplars)
	}
	if len(bucket.Repositories) != 2 || bucket.Repositories[0].RepositoryID != "repo-a" || bucket.Repositories[0].Count != 2 || bucket.Repositories[1].RepositoryID != "repo-b" || bucket.Repositories[1].Count != 1 {
		t.Fatalf("blocked repository counts = %#v", bucket.Repositories)
	}
	if len(report.OutlierPlans) != 1 || report.OutlierPlans[0].RepositoryID != "repo-b" || report.OutlierPlans[0].PlanID != "duplicate" {
		t.Fatalf("qualified outliers = %#v", report.OutlierPlans)
	}
}

func TestAggregateSourcesReturnsCatalogAndContextErrors(t *testing.T) {
	want := errors.New("catalog failed")
	if _, err := AggregateSources(context.Background(), fixtureSourceLister{err: want}); !errors.Is(err, want) {
		t.Fatalf("error = %v, want %v", err, want)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := AggregateSources(ctx, fixtureSourceLister{sources: []RepositorySource{{ID: "repo", Plans: fixtureLister{}}}}); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context canceled", err)
	}
}

func TestAggregateReturnsListingAndContextErrors(t *testing.T) {
	want := errors.New("list failed")
	if _, err := Aggregate(context.Background(), fixtureLister{err: want}); !errors.Is(err, want) {
		t.Fatalf("error = %v, want %v", err, want)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Aggregate(ctx, fixtureLister{summaries: []plan.PlanSummary{{ID: "one", Dir: "testdata/plan-alpha"}}}); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context canceled", err)
	}
}

func TestNormalizeBlockedReasonUsesKnownKeywordsAndPrefixes(t *testing.T) {
	tests := map[string]string{
		"pre-existing lint failure":       "unrelated_failure",
		"verification command is invalid": "invalid_verification_command",
		"agent timed out":                 "timeout",
		"Dependency missing":              "dependency",
		"Custom Check: detail 123":        "custom_check",
		"":                                "unknown",
	}
	for input, want := range tests {
		if got := NormalizeBlockedReason(input); got != want {
			t.Errorf("NormalizeBlockedReason(%q) = %q, want %q", input, got, want)
		}
	}
}
