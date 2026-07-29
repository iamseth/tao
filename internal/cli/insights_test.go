package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iamseth/tao/internal/insights"
	"github.com/iamseth/tao/internal/plan"
)

func TestInsightsCommandRegistrationAndPlansDir(t *testing.T) {
	metadata := commandByName("insights")
	if metadata == nil || metadata.repository != repositoryDefault || metadata.registerFlags == nil {
		t.Fatalf("insights metadata = %#v", metadata)
	}
	if got := normalizeCommand("insi"); got != "insights" {
		t.Fatalf("normalizeCommand(insi) = %q, want insights", got)
	}

	var gotPlansDir string
	var out bytes.Buffer
	app := App{Out: &out, Err: &out, Repository: func(plansDir string) Repository {
		gotPlansDir = plansDir
		return fakeRepository{}
	}}
	if err := app.Run(context.Background(), []string{"--plans-dir", "/tmp/insights-plans", "insi"}); err != nil {
		t.Fatal(err)
	}
	if gotPlansDir != "/tmp/insights-plans" {
		t.Fatalf("plans dir = %q", gotPlansDir)
	}
	if out.String() != "No plan history.\n" {
		t.Fatalf("output = %q", out.String())
	}
}

func TestInsightsReportRendering(t *testing.T) {
	report := insights.Report{
		PlansScanned: 2,
		PlansSkipped: 1,
		BlockedReasons: []insights.ReasonBucket{{
			Reason: "unreachable_service", Count: 3, Exemplars: []string{"database connection refused"},
		}},
		ReworkPlans: []insights.ReworkPlan{{PlanID: "plan-a", Rounds: 4, StoppedReasons: []string{"cap exhausted"}}},
		Signals: insights.SignalCounts{
			SessionTimeout: 2, SliceResumeFailed: 1, VerificationCommandInvalid: 3, PlanCommitFallback: 4, PlanCommitGuard: 5,
		},
		OutputTokens: insights.Percentiles{Sessions: 10, P50: 100, P90: 200, P95: 250},
		Cost:         insights.Percentiles{Sessions: 10, P50: 1.25, P90: 2.5, P95: 3.75},
		OutlierPlans: []insights.PlanOutlier{{PlanID: "plan-b", OutputTokens: 300, Cost: 4.5, OutputTokensOutlier: true, CostOutlier: true}},
	}
	var out bytes.Buffer
	if err := renderInsightsReport(&out, report); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"Repository insights (2 plans scanned, 1 skipped)",
		"Failure patterns:", "unreachable_service: 3", "database connection refused",
		"Rework-loop plans:", "plan-a: 4 rounds (cap exhausted)",
		"Event counters:", "session_timeout: 2", "verification_command_invalid: 3",
		"Session telemetry:", "output tokens (10 sessions): p50=100 p90=200 p95=250", "cost (10 sessions): p50=$1.25",
		"Outlier plans:", "plan-b: output_tokens=300 cost=$4.50",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("report missing %q:\n%s", want, out.String())
		}
	}
}

func TestInsightsDigestIsDeterministicAndCapped(t *testing.T) {
	buckets := make([]insights.ReasonBucket, 0, digestMaxBuckets+1)
	for _, reason := range []string{"alpha", "bravo", "charlie", "delta", "echo", "foxtrot"} {
		buckets = append(buckets, insights.ReasonBucket{Reason: reason, Count: 2, Exemplars: []string{reason + " first", reason + " second"}})
	}
	buckets[0].Exemplars[0] = strings.Repeat("long exemplar ", 30) + "exemplar-tail"

	reworkPlans := make([]insights.ReworkPlan, 0, digestMaxReworkPlans+1)
	outlierPlans := make([]insights.PlanOutlier, 0, digestMaxOutlierPlans+1)
	for _, suffix := range []string{"a", "b", "c", "d", "e", "f"} {
		reworkPlans = append(reworkPlans, insights.ReworkPlan{PlanID: "rework-" + suffix, Rounds: 3})
		outlierPlans = append(outlierPlans, insights.PlanOutlier{PlanID: "outlier-" + suffix, OutputTokens: 100, Cost: 1})
	}
	report := insights.Report{
		PlansScanned:   7,
		BlockedReasons: buckets,
		ReworkPlans:    reworkPlans,
		OutputTokens:   insights.Percentiles{Sessions: 2, P50: 10, P90: 20, P95: 20},
		Cost:           insights.Percentiles{Sessions: 2, P50: 1, P90: 2, P95: 2},
		OutlierPlans:   outlierPlans,
	}
	var first, second bytes.Buffer
	if err := renderInsightsDigest(&first, report); err != nil {
		t.Fatal(err)
	}
	if err := renderInsightsDigest(&second, report); err != nil {
		t.Fatal(err)
	}
	if first.String() != second.String() {
		t.Fatalf("digest changed between renders:\n%s\n---\n%s", first.String(), second.String())
	}
	for _, want := range []string{
		"# Tao Insights Digest", "## Failure patterns", "## Rework loops", "## Event counters", "## Session telemetry", "## Outlier plans",
		"`echo`: 2 — echo first", "`rework-e`: 3 rounds", "`outlier-e`: output_tokens=100",
	} {
		if !strings.Contains(first.String(), want) {
			t.Errorf("digest missing %q:\n%s", want, first.String())
		}
	}
	for _, excluded := range []string{"foxtrot", "alpha second", "exemplar-tail", "rework-f", "outlier-f"} {
		if strings.Contains(first.String(), excluded) {
			t.Errorf("digest unexpectedly contains capped value %q:\n%s", excluded, first.String())
		}
	}
	if first.Len() > digestMaxBytes {
		t.Errorf("digest length = %d, want at most %d", first.Len(), digestMaxBytes)
	}
}

func TestInsightsCommandAggregatesHistoryAndDigestHandlesEmptyHistory(t *testing.T) {
	dir := t.TempDir()
	events := `{"type":"slice_blocked","reason":"external service unreachable"}` + "\n" +
		`{"type":"session_timeout"}` + "\n" +
		`{"type":"agent_metrics","metrics":{"session_id":"one","output_tokens":42,"cost":1.5}}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, "events.jsonl"), []byte(events), 0o600); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	app := App{Out: &out, Err: &out, Repository: func(string) Repository {
		return fakeRepository{summaries: []plan.PlanSummary{{ID: "plan-a", Dir: dir}}}
	}}
	if err := app.Run(context.Background(), []string{"insights"}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"unreachable_service: 1", "session_timeout: 1", "output tokens (1 sessions): p50=42"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("command output missing %q:\n%s", want, out.String())
		}
	}

	out.Reset()
	empty := App{Out: &out, Err: &out, Repository: func(string) Repository { return fakeRepository{} }}
	if err := empty.Run(context.Background(), []string{"insights", "--digest"}); err != nil {
		t.Fatal(err)
	}
	if out.String() != "# Tao Insights Digest\n\nNo plan history.\n" {
		t.Fatalf("empty digest = %q", out.String())
	}
}
