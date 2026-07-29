package insights

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/iamseth/tao/internal/plan"
)

type fixtureLister struct {
	summaries []plan.PlanSummary
	err       error
}

func (l fixtureLister) ListPlans(context.Context, plan.PlanFilter) ([]plan.PlanSummary, error) {
	return l.summaries, l.err
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
