package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/iamseth/tao/internal/agent/logrecord"
	"github.com/iamseth/tao/internal/insights"
	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/internal/taodata"
	"github.com/iamseth/tao/internal/view"
)

const (
	digestMaxBuckets      = 5
	digestMaxReworkPlans  = 5
	digestMaxOutlierPlans = 5
	digestMaxTextBytes    = 160
	digestMaxBytes        = 4096
	allDigestMaxSources   = 8
	allDigestMaxSignals   = 3
)

func renderInsightsReport(out *bytes.Buffer, report insights.Report) error {
	return view.RenderInsights(out, report, view.InsightsOptions{Scope: view.InsightsScopeRepository, Format: view.InsightsFormatReport})
}

func renderInsightsDigest(out *bytes.Buffer, report insights.Report) error {
	return view.RenderInsights(out, report, view.InsightsOptions{Scope: view.InsightsScopeRepository, Format: view.InsightsFormatDigest})
}

func renderAllInsightsReport(out *bytes.Buffer, report insights.Report) error {
	return view.RenderInsights(out, report, view.InsightsOptions{Scope: view.InsightsScopeAllRepositories, Format: view.InsightsFormatReport})
}

func renderAllInsightsDigest(out *bytes.Buffer, report insights.Report) error {
	return view.RenderInsights(out, report, view.InsightsOptions{Scope: view.InsightsScopeAllRepositories, Format: view.InsightsFormatDigest})
}

func renderInsightsScorecard(out *bytes.Buffer, report insights.Report) error {
	return view.RenderInsights(out, report, view.InsightsOptions{Scope: view.InsightsScopeRepository, Format: view.InsightsFormatScorecard})
}

func renderAllInsightsScorecard(out *bytes.Buffer, report insights.Report) error {
	return view.RenderInsights(out, report, view.InsightsOptions{Scope: view.InsightsScopeAllRepositories, Format: view.InsightsFormatScorecard})
}

func TestInsightsScorecardFlags(t *testing.T) {
	var out bytes.Buffer
	app := App{Out: &out, Err: &out, Repository: func(string) Repository { return fakeRepository{} }}
	if err := app.Run(context.Background(), []string{"insi", "--scorecard", "--digest"}); err == nil || err.Error() != "--scorecard cannot be combined with --digest" {
		t.Fatalf("conflicting flags error = %v", err)
	}
	out.Reset()
	if err := app.Run(context.Background(), []string{"insi", "--scorecard"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "No plan history.\n") || !strings.Contains(out.String(), "# Tao Planner Scorecard") {
		t.Fatalf("empty scorecard = %q", out.String())
	}
}

func limitDigestText(value string) string {
	value = strings.Join(strings.Fields(value), " ")
	if len(value) <= digestMaxTextBytes {
		return value
	}
	value = value[:digestMaxTextBytes-len("…")]
	for !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value + "…"
}

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
	if out.String() != "No plan history.\n\nPlanner routing:\n- No planner routing records.\n" {
		t.Fatalf("output = %q", out.String())
	}
}

func TestInsightsCurrentRepositoryRoutes(t *testing.T) {
	repo := taodata.Repo{ID: "route-repo", Name: "routing", Root: t.TempDir()}
	app, out, _ := noteTestApp(t, nil, repo)
	createTestRoute(t, app, repo, testRouteID)
	if err := app.insights(context.Background(), fakeRepository{}, "", nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "1 records; shadow=1") {
		t.Fatalf("routing missing: %s", out)
	}
	out.Reset()
	if err := app.insights(context.Background(), fakeRepository{}, t.TempDir(), nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "No planner routing records.") {
		t.Fatalf("explicit plans dir joined unrelated ledger: %s", out)
	}
}

func TestInsightsCatalogRoutes(t *testing.T) {
	registry := taodata.NewRegistry(t.TempDir())
	repo := taodata.Repo{Schema: taodata.RepoSchema, ID: "route-repo", Name: "routing", Root: filepath.Join(t.TempDir(), "missing")}
	if err := registry.WriteRepo(repo); err != nil {
		t.Fatal(err)
	}
	legacy := taodata.Repo{Schema: taodata.RepoSchema, ID: "legacy-repo", Root: filepath.Join(t.TempDir(), "missing")}
	if err := registry.WriteRepo(legacy); err != nil {
		t.Fatal(err)
	}
	createTestRoute(t, App{Registry: func() NoteRegistry { return registry }}, repo, testRouteID)
	report, err := insights.AggregateSources(context.Background(), catalogInsightSources{registry: registry})
	if err != nil {
		t.Fatal(err)
	}
	if report.PlannerRouting.Records != 1 || report.RepositoryCoverage.Empty != 2 || len(report.PlannerRouting.Warnings) != 0 {
		t.Fatalf("report = %+v", report)
	}
	if _, err := os.Stat(registry.PlannerRoutesDir(legacy)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("read created legacy ledger: %v", err)
	}
}

func TestInsightsRenderersGolden(t *testing.T) {
	report := representativeInsightsReport()
	tests := []struct {
		name    string
		fixture string
		render  func(*bytes.Buffer, insights.Report) error
	}{
		{name: "repository scorecard", fixture: "repository-scorecard.golden", render: renderInsightsScorecard},
		{name: "all-repositories scorecard", fixture: "all-repositories-scorecard.golden", render: renderAllInsightsScorecard},
		{name: "repository report", fixture: "repository-report.golden", render: renderInsightsReport},
		{name: "repository digest", fixture: "repository-digest.golden", render: renderInsightsDigest},
		{name: "all-repositories report", fixture: "all-repositories-report.golden", render: renderAllInsightsReport},
		{name: "all-repositories digest", fixture: "all-repositories-digest.golden", render: renderAllInsightsDigest},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var out bytes.Buffer
			if err := test.render(&out, report); err != nil {
				t.Fatal(err)
			}
			assertGolden(t, filepath.Join("testdata", "insights", test.fixture), out.Bytes())
		})
	}
}

func TestInsightsCorrectedVerificationEventsRetainExistingGoldenProjection(t *testing.T) {
	// Successful corrections still count as invalid-command observations;
	// mismatch diagnostics must not introduce a new counter or rendered view.
	fixture := newRunPlanFixture(t, plan.StatusInProgress, []string{"slice-a"}, nil, "slice-a", plan.StatusInProgress)
	for _, event := range []plan.Event{
		{Type: plan.EventTypeVerificationCommandInvalid, SliceID: "slice-a", VerificationAttemptID: "attempt-one", Command: "go test pkg/example_test.go", CorrectedCommand: "go test example_test.go", Result: "passed", Reason: "No test files found"},
		{Type: plan.EventTypeVerificationCommandInvalid, SliceID: "slice-a", VerificationAttemptID: "attempt-two", Command: "go test pkg/example_test.go", CorrectedCommand: "go test example_test.go", Result: "passed", Reason: "No test files found"},
		{Type: plan.EventTypeVerificationClaimMismatch, SliceID: "slice-a", VerificationAttemptID: "attempt-two", Command: "go test pkg/example_test.go", ClaimedResult: "passed", Result: "failed", Message: "password=private-claim"},
	} {
		if err := plan.AppendEvent(fixture.dir, event); err != nil {
			t.Fatal(err)
		}
	}
	collected, err := insights.Aggregate(context.Background(), plan.NewFileRepository(fixture.root))
	if err != nil {
		t.Fatal(err)
	}
	if collected.Signals.VerificationCommandInvalid != 2 {
		t.Fatalf("successful correction count = %d", collected.Signals.VerificationCommandInvalid)
	}
	report := representativeInsightsReport()
	report.Signals.VerificationCommandInvalid = collected.Signals.VerificationCommandInvalid
	for _, test := range []struct {
		fixture string
		render  func(*bytes.Buffer, insights.Report) error
	}{
		{"repository-report.golden", renderInsightsReport},
		{"repository-digest.golden", renderInsightsDigest},
		{"all-repositories-report.golden", renderAllInsightsReport},
		{"all-repositories-digest.golden", renderAllInsightsDigest},
	} {
		t.Run(test.fixture, func(t *testing.T) {
			var out bytes.Buffer
			if err := test.render(&out, report); err != nil {
				t.Fatal(err)
			}
			assertGolden(t, filepath.Join("testdata", "insights", test.fixture), out.Bytes())
			out.Reset()
			if err := test.render(&out, collected); err != nil {
				t.Fatal(err)
			}
			for _, forbidden := range []string{plan.EventTypeVerificationClaimMismatch, "private-claim", "attempt-two"} {
				if strings.Contains(out.String(), forbidden) {
					t.Fatalf("diagnostic became a rendered insight: %q", forbidden)
				}
			}
		})
	}
}

func representativeInsightsReport() insights.Report {
	latestTimeout := time.Date(2026, 8, 17, 14, 30, 0, 0, time.FixedZone("fixture", -7*60*60))
	latestResume := time.Date(2026, 8, 18, 9, 15, 0, 0, time.UTC)
	latestFallback := time.Date(2026, 8, 16, 20, 0, 0, 0, time.UTC)
	latestGuard := time.Date(2026, 8, 18, 22, 5, 0, 0, time.UTC)
	report := insights.Report{
		Scorecard:    representativeScorecard(),
		PlansScanned: 9,
		PlansSkipped: 2,
		PlannerRouting: insights.PlannerRoutingReport{
			Records: 3, Randomized: 3, LinkedPlans: 2, Matured: 1, Censored: 1, AttemptsWithoutPlan: 1,
			Policies: []insights.RoutingPolicyCohort{{PolicyVersion: "policy-v1", Mode: "randomized", Arms: []insights.RoutingArmCohort{
				{ArmKey: "pi|inherited|inherited|inherited|prompt-v1|default", Assigned: 3, Linked: 2, Matured: 1, Completed: 1, WeightSum: 6, WeightedCompletionRate: new(float64(1))},
			}}},
		},
		RepositoryCoverage: insights.RepositoryCoverage{
			Scanned: 6, Empty: 1, Unreadable: 1, Skipped: 1,
			Repositories: []insights.RepositoryScanResult{
				{RepositoryID: "repo-a", RepositoryName: "alpha", Status: "scanned"},
				{RepositoryID: "repo-b", RepositoryName: "beta", Status: "empty"},
				{RepositoryID: "repo-c", RepositoryName: "gamma", Status: "unreadable"},
				{RepositoryID: "repo-d", RepositoryName: "delta", Status: "skipped"},
				{RepositoryID: "repo-e", Status: "scanned"},
				{RepositoryID: "repo-f", Status: "scanned"},
				{RepositoryID: "repo-g", Status: "scanned"},
				{RepositoryID: "repo-h", Status: "scanned"},
				{RepositoryID: "repo-i", Status: "scanned"},
			},
		},
		Signals: insights.SignalCounts{
			SessionTimeout: 3, SliceResumeFailed: 0, VerificationCommandInvalid: 2, PlanCommitFallback: 1, PlanCommitGuard: 4,
		},
		SignalEvidence: insights.SignalEvidence{
			SessionTimeout:             insights.SignalObservation{Count: 3, Plans: 2, Repositories: 2, LatestTimestamp: &latestTimeout},
			SliceResumeAttempted:       insights.SignalObservation{Count: 2, Plans: 1, Repositories: 1, LatestTimestamp: &latestResume},
			VerificationCommandInvalid: insights.SignalObservation{Count: 2, Plans: 2, Repositories: 1},
			PlanCommitFallback:         insights.SignalObservation{Count: 1, Plans: 1, Repositories: 1, LatestTimestamp: &latestFallback},
			PlanCommitGuard:            insights.SignalObservation{Count: 4, Plans: 3, Repositories: 2, LatestTimestamp: &latestGuard},
		},
		OutputTokens: insights.Percentiles{Sessions: 7, P50: 1200, P90: 3400, P95: 5600},
		Cost:         insights.Percentiles{},
		RecentLogs: insights.RecentLogReport{
			Coverage: insights.LogCoverage{Eligible: 9, Scanned: 5, MissingRecency: 1, OutsideWindow: 2, Missing: 1, Unreadable: 1, Unsupported: 0, Oversized: 1, WorkLimited: 1},
			Repositories: []insights.RepositoryLogCoverage{
				{RepositoryID: "repo-c", RepositoryName: "gamma", Coverage: insights.LogCoverage{Eligible: 3, Scanned: 1, MissingRecency: 1, OutsideWindow: 1, Unreadable: 1, WorkLimited: 1}},
				{RepositoryID: "repo-a", RepositoryName: "alpha", Coverage: insights.LogCoverage{Eligible: 3, Scanned: 3}},
				{RepositoryID: "repo-b", RepositoryName: "beta", Coverage: insights.LogCoverage{Eligible: 3, Scanned: 1, OutsideWindow: 1, Missing: 1, Oversized: 1}},
			},
			MissingExecutables: []insights.LogSignal{{Name: "jq", Count: 2, PlanCount: 2, RepositoryCount: 1, Exemplars: []insights.LogExemplar{{RepositoryID: "repo-a", RepositoryName: "alpha", PlanID: "plan-1", Excerpt: "jq: command not found"}}}},
			ToolUses: []insights.LogSignal{
				{Name: "git", Count: 8, PlanCount: 4, RepositoryCount: 3, Exemplars: []insights.LogExemplar{{RepositoryID: "repo-a", RepositoryName: "alpha", PlanID: "plan-1", Excerpt: "git status --short"}}},
				{Name: "go", Count: 6, PlanCount: 3, RepositoryCount: 2},
				{Name: "rg", Count: 4, PlanCount: 2, RepositoryCount: 2},
				{Name: "make", Count: 1, PlanCount: 1, RepositoryCount: 1},
			},
		},
	}
	for i, reason := range []string{"network_timeout", "invalid_command", "merge_conflict", "missing_approval", "stale_base", "digest_overflow"} {
		report.BlockedReasons = append(report.BlockedReasons, insights.ReasonBucket{
			Reason: reason, Count: i + 1, Exemplars: []string{fmt.Sprintf("example %d", i+1), "second example"},
			QualifiedExemplars: []insights.EvidenceExemplar{{RepositoryID: "repo-a", RepositoryName: "alpha", Value: fmt.Sprintf("example %d", i+1)}},
			Repositories:       []insights.ReasonRepository{{RepositoryID: "repo-a", RepositoryName: "alpha", Count: i + 1}},
		})
	}
	for i := range 6 {
		rework := insights.ReworkPlan{RepositoryID: "repo-a", RepositoryName: "alpha", PlanID: fmt.Sprintf("rework-%d", i+1), Rounds: i + 3}
		if i == 0 {
			rework.StoppedReasons = []string{"same finding repeated", "attempt cap reached"}
		}
		report.ReworkPlans = append(report.ReworkPlans, rework)
		report.OutlierPlans = append(report.OutlierPlans, insights.PlanOutlier{
			RepositoryID: "repo-a", RepositoryName: "alpha", PlanID: fmt.Sprintf("outlier-%d", i+1),
			OutputTokens: int64(6000 + i*100), Cost: float64(i) + 0.5, OutputTokensOutlier: true, CostOutlier: i%2 == 0,
		})
	}
	return report
}

func representativeScorecard() insights.Scorecard {
	rate := func(k, n int) insights.RateEstimate { return insights.EstimateRate(k, n, 5) }
	median := func(value float64, n int) insights.MedianEstimate {
		return insights.MedianEstimate{Median: value, Samples: n, Sparse: n < 5}
	}
	cohort := func(key, runtime, model string, n int) insights.TreatmentCohort {
		efficiency := insights.EfficiencyMedians{
			Plans: n, Sessions: median(3, n), OutputTokens: median(1200, n), TotalTokens: median(6000, n),
			Cost: median(0.75, n), ToolCalls: median(18, n), CostPerOriginalSlice: median(0.25, n),
			OutputTokensPerOriginalSlice: median(400, n), HoursToApproval: median(2.5, n),
			PlanningCost: median(0, 0), PlanningOutputTokens: median(0, 0),
		}
		return insights.TreatmentCohort{
			Key: key, Runtime: runtime, Model: model, Confidence: insights.TreatmentConfidenceLow,
			Outcomes: insights.CohortOutcomes{
				Plans: n + 1, Matured: n, Censored: 1, NeverStarted: 1,
				Quality: insights.QualityOutcomes{
					Started: rate(n, n+1), FirstReviewApproved: rate(n-1, n), ExactApproval: rate(n, n),
					Completed: rate(n, n), Merged: rate(n-1, n), PullRequestCompleted: rate(1, n),
					Abandoned: rate(0, n), ReworkStopped: rate(0, n), AnyRework: rate(1, n), AnyVerificationRepair: rate(1, n),
					ReviewRounds: median(1, n), OriginalSlices: median(3, n), ValidationWarningPlans: 1,
					FindingSeverities: []insights.LabelCount{{Label: "warning", Count: 2}, {Label: "error", Count: 1}},
				},
				Efficiency: insights.EfficiencyOutcomes{
					MaturedCompleted: efficiency, MaturedAll: efficiency, RoleAttributedRatio: 0.75,
					ByRole: []insights.RoleTotals{
						{Role: "implementation", Sessions: 3, OutputTokens: 1200, TotalTokens: 6000, Cost: 0.75, ToolCalls: 18},
						{Role: "unattributed", Sessions: 1, OutputTokens: 400, TotalTokens: 2000, Cost: 0.25, ToolCalls: 6},
					},
				},
				Reliability: insights.ReliabilityOutcomes{
					Infrastructure: []insights.LabelCount{{Label: "session_timeout", Count: 1}},
					Quality:        []insights.LabelCount{{Label: "verification_failed", Count: 1}},
				},
			},
		}
	}
	claude := cohort("claude/claude-opus-5", "claude", "claude-opus-5", 1)
	pi := cohort("pi", "pi", "", 5)
	pi.Confidence = insights.TreatmentConfidenceHigh
	return insights.Scorecard{
		Coverage: insights.ScorecardCoverage{
			Plans: 10, NeverStarted: 2, Active: 2, Terminal: 6, Matured: 6, Censored: 4,
			TreatmentHigh: 6, TreatmentLow: 2, TreatmentAmbiguous: 1, TreatmentMissing: 1,
			RoleAttributedSessions: 6, UnattributedSessions: 2, MaturityWindowDays: 14, MinimumSamples: 5,
		},
		Cohorts: []insights.TreatmentCohort{claude, pi},
		Strata: []insights.StratumBreakdown{
			{Stratum: "effort", Key: "small", Cohorts: []insights.TreatmentCohort{pi}},
			{Stratum: "repository", Key: "repo-a", Cohorts: []insights.TreatmentCohort{claude, pi}},
			{Stratum: "repository", Key: "repo-b", Cohorts: []insights.TreatmentCohort{claude}},
		},
		Inversions: []insights.Inversion{{Metric: "completed_rate", CohortA: "claude/claude-opus-5", CohortB: "pi", Stratum: "repository", Key: "repo-a", GlobalDelta: 0.2, StratumDelta: -0.1}},
		ExcludedLabels: []insights.ExcludedLabel{
			{Label: "build", Plans: 1, Reason: "ambiguous treatment"},
			{Label: strings.Repeat("界", 200), Plans: 1, Reason: "unknown runtime"},
		},
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
		outlierPlans = append(outlierPlans, insights.PlanOutlier{PlanID: "outlier-" + suffix, OutputTokens: 100, Cost: 1, OutputTokensOutlier: true, CostOutlier: true})
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

func TestInsightsDigestRendersBoundedReworkStopContext(t *testing.T) {
	stopReasons := []string{"  durable\nstop  ", strings.Repeat("界", 80) + " tail"}
	reworkPlans := []insights.ReworkPlan{
		{RepositoryName: "alpha", RepositoryID: "repo-a", PlanID: "plan-a", Rounds: 4, StoppedReasons: stopReasons},
		{RepositoryName: "alpha", RepositoryID: "repo-a", PlanID: "plan-b", Rounds: 3},
	}
	for _, suffix := range []string{"c", "d", "e"} {
		reworkPlans = append(reworkPlans, insights.ReworkPlan{RepositoryName: "alpha", RepositoryID: "repo-a", PlanID: "plan-" + suffix, Rounds: 2})
	}
	reworkPlans = append(reworkPlans, insights.ReworkPlan{RepositoryName: "alpha", RepositoryID: "repo-a", PlanID: "overflow", Rounds: 6, StoppedReasons: []string{"overflow-stop"}})
	report := insights.Report{PlansScanned: 6, ReworkPlans: reworkPlans}
	boundedReasons := limitDigestText(strings.Join(stopReasons, "; "))

	tests := []struct {
		name          string
		render        func(*bytes.Buffer, insights.Report) error
		stoppedLabel  string
		unstoppedLine string
	}{
		{name: "repository", render: renderInsightsDigest, stoppedLabel: "plan-a", unstoppedLine: "- `plan-b`: 3 rounds\n"},
		{name: "all repositories", render: renderAllInsightsDigest, stoppedLabel: "alpha [repo-a]/plan-a", unstoppedLine: "- `alpha [repo-a]/plan-b`: 3 rounds\n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var first, second bytes.Buffer
			if err := test.render(&first, report); err != nil {
				t.Fatal(err)
			}
			if err := test.render(&second, report); err != nil {
				t.Fatal(err)
			}
			if first.String() != second.String() {
				t.Fatal("digest is not deterministic")
			}
			wantStopped := fmt.Sprintf("- `%s`: 4 rounds — stopped: %s\n", test.stoppedLabel, boundedReasons)
			if !strings.Contains(first.String(), wantStopped) {
				t.Errorf("digest missing bounded stopped context %q:\n%s", wantStopped, first.String())
			}
			if !strings.Contains(first.String(), test.unstoppedLine) {
				t.Errorf("digest changed unstopped line %q:\n%s", test.unstoppedLine, first.String())
			}
			for _, excluded := range []string{" tail", "overflow", "overflow-stop"} {
				if strings.Contains(first.String(), excluded) {
					t.Errorf("digest contains excluded text %q:\n%s", excluded, first.String())
				}
			}
			if !utf8.ValidString(first.String()) {
				t.Fatal("digest is not valid UTF-8")
			}
		})
	}
}

func TestInsightsDigestGlobalCapPreservesUTF8(t *testing.T) {
	longText := strings.Repeat("界", 100)
	report := insights.Report{PlansScanned: 20}
	for i := range digestMaxBuckets {
		report.BlockedReasons = append(report.BlockedReasons, insights.ReasonBucket{Reason: fmt.Sprintf("reason-%d-%s", i, longText), Count: 1, Exemplars: []string{longText}})
	}
	for i := range digestMaxReworkPlans {
		report.ReworkPlans = append(report.ReworkPlans, insights.ReworkPlan{PlanID: fmt.Sprintf("rework-%d-%s", i, longText), Rounds: 5, StoppedReasons: []string{longText}})
	}
	for i := range digestMaxOutlierPlans {
		report.OutlierPlans = append(report.OutlierPlans, insights.PlanOutlier{PlanID: fmt.Sprintf("outlier-%d-%s", i, longText), OutputTokensOutlier: true})
	}

	var out bytes.Buffer
	if err := renderInsightsDigest(&out, report); err != nil {
		t.Fatal(err)
	}
	if out.Len() > digestMaxBytes {
		t.Fatalf("digest length = %d, want <= %d", out.Len(), digestMaxBytes)
	}
	if !strings.HasSuffix(out.String(), "\n… digest truncated\n") {
		t.Fatalf("digest did not exercise global cap:\n%s", out.String())
	}
	if !utf8.ValidString(out.String()) {
		t.Fatal("globally capped digest is not valid UTF-8")
	}
}

func TestInsightsAllReposHelpAndFlagConflict(t *testing.T) {
	var out bytes.Buffer
	if err := (App{Out: &out, Err: &out}).Run(context.Background(), []string{"insights", "--help"}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"--all-repos", "all registered repositories", "tao insights --all-repos --digest"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("help missing %q:\n%s", want, out.String())
		}
	}

	err := (App{Out: &out, Err: &out, Repository: func(string) Repository { return fakeRepository{} }}).Run(
		context.Background(), []string{"--plans-dir", "/tmp/explicit", "insights", "--all-repos"},
	)
	if err == nil || !strings.Contains(err.Error(), "--all-repos cannot be combined with --plans-dir") {
		t.Fatalf("flag conflict error = %v", err)
	}
}

func TestInsightsAllReposCatalogCoverageSignalsAndOrdering(t *testing.T) {
	dataHome := t.TempDir()
	t.Setenv("TAO_DATA_HOME", dataHome)
	registry := taodata.NewRegistry(dataHome)
	for _, repo := range []taodata.Repo{
		{Schema: taodata.RepoSchema, ID: "repo-z", Name: "zeta", Root: filepath.Join(t.TempDir(), "missing-z")},
		{Schema: taodata.RepoSchema, ID: "repo-a", Name: "alpha", Root: filepath.Join(t.TempDir(), "missing-a")},
		{Schema: taodata.RepoSchema, ID: "repo-m", Name: "middle", Root: filepath.Join(t.TempDir(), "missing-m")},
	} {
		if err := registry.WriteRepo(repo); err != nil {
			t.Fatal(err)
		}
	}

	planDir := t.TempDir()
	now := time.Now()
	events := `{"type":"slice_blocked","reason":"external service unreachable"}` + "\n" +
		`{"type":"session_timeout"}` + "\n" +
		`{"type":"agent_metrics","metrics":{"session_id":"one","output_tokens":42,"cost":1.5}}` + "\n"
	if err := os.WriteFile(filepath.Join(planDir, "events.jsonl"), []byte(events), 0o600); err != nil {
		t.Fatal(err)
	}
	logFile, err := os.Create(filepath.Join(planDir, "agent-run.log")) // #nosec G304 -- test temporary directory.
	if err != nil {
		t.Fatal(err)
	}
	for _, record := range []logrecord.Record{
		{Type: logrecord.TypeSession, Content: "test", Timestamp: now.Format(time.RFC3339)},
		{Type: logrecord.TypeToolCall, Name: "bash", Payload: `{"command":"gh issue list && missing-tool --version"}`},
		{Type: logrecord.TypeToolResult, Name: "bash", Content: "sh: missing-tool: command not found", Failed: true},
	} {
		if err := logrecord.Write(logFile, record); err != nil {
			t.Fatal(err)
		}
	}
	if err := logFile.Close(); err != nil {
		t.Fatal(err)
	}
	repositories := map[string]Repository{
		"repo-a": fakeRepository{summaries: []plan.PlanSummary{{ID: "duplicate", Dir: planDir, LastActivityAt: &now}}},
		"repo-m": fakeRepository{},
		"repo-z": fakeRepository{err: errors.New("permission denied")},
	}
	var out bytes.Buffer
	app := App{Out: &out, Err: &out, Repository: func(plansDir string) Repository {
		return repositories[filepath.Base(filepath.Dir(plansDir))]
	}}
	if err := app.Run(context.Background(), []string{"insights", "--all-repos"}); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	for _, want := range []string{
		"All-repository insights (3 registered; 1 scanned, 1 empty, 1 unreadable, 0 skipped)",
		"alpha [repo-a]: scanned", "middle [repo-m]: empty", "zeta [repo-z]: unreadable",
		"Skipped-source warnings:", "unreachable_service: 1", "alpha [repo-a]: external service unreachable",
		"Structured event counters:", "session_timeout: 1", "Global session telemetry:",
		"output tokens (1 sessions): p50=42", "cutoff: plan activity within the last 30 days",
		"missing-tool: 1 occurrences across 1 plans / 1 repositories",
		"gh: 1 occurrences across 1 plans / 1 repositories", "github.com: 1 occurrences across 1 plans / 1 repositories",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("all-repository report missing %q:\n%s", want, text)
		}
	}
	if alpha, middle, zeta := strings.Index(text, "alpha [repo-a]: scanned"), strings.Index(text, "middle [repo-m]: empty"), strings.Index(text, "zeta [repo-z]: unreadable"); alpha < 0 || alpha >= middle || middle >= zeta {
		t.Errorf("repository ordering is not stable by catalog id:\n%s", text)
	}
	if strings.Contains(text, "Repository coverage limits:") {
		t.Errorf("complete recent-log coverage should not render repository limit rows:\n%s", text)
	}

	out.Reset()
	if err := app.Run(context.Background(), []string{"insi", "--all-repos", "--scorecard"}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"# Tao Planner Scorecard (all repositories)",
		"repositories: 3 registered; 1 scanned; 1 empty; 1 unreadable; 0 skipped",
		"alpha [repo-a]: scanned", "middle [repo-m]: empty", "zeta [repo-z]: unreadable",
		"planning-metrics coverage: 0/1 plans", "reasoning effort: not recorded",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("all-repository scorecard missing %q:\n%s", want, out.String())
		}
	}
}

func TestInsightsAllReposEmptyHistoryAndDigestCap(t *testing.T) {
	dataHome := t.TempDir()
	t.Setenv("TAO_DATA_HOME", dataHome)
	var out bytes.Buffer
	app := App{Out: &out, Err: &out, Repository: func(string) Repository { return fakeRepository{} }}
	if err := app.Run(context.Background(), []string{"insights", "--all-repos", "--digest"}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"# Tao All-Repository Insights Digest", "Repositories: 0 registered", "None registered", "Cutoff: plan activity within the last 30 days"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("empty digest missing %q:\n%s", want, out.String())
		}
	}

	latestSignal := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	report := insights.Report{
		PlansScanned: 100,
		Signals:      insights.SignalCounts{SessionTimeout: 2},
		SignalEvidence: insights.SignalEvidence{
			SessionTimeout: insights.SignalObservation{Count: 2, Plans: 2, Repositories: 2, LatestTimestamp: &latestSignal},
		},
	}
	for i := range allDigestMaxSources + 10 {
		report.RepositoryCoverage.Repositories = append(report.RepositoryCoverage.Repositories, insights.RepositoryScanResult{RepositoryID: fmt.Sprintf("repo-%02d", i), Status: "scanned"})
	}
	for i := range allDigestMaxSignals + 10 {
		report.RecentLogs.ToolUses = append(report.RecentLogs.ToolUses, insights.LogSignal{Name: fmt.Sprintf("tool-%02d", i), Count: 10, PlanCount: 4, RepositoryCount: 3})
	}
	report.BlockedReasons = []insights.ReasonBucket{{
		Reason: "shared_failure", Count: 12,
		QualifiedExemplars: []insights.EvidenceExemplar{{RepositoryID: "repo-00", Value: "first"}, {RepositoryID: "repo-00", Value: "second"}},
		Repositories:       []insights.ReasonRepository{{RepositoryID: "repo-00", Count: 2}, {RepositoryID: "repo-01", Count: 1}},
	}}
	var first, second bytes.Buffer
	if err := renderAllInsightsDigest(&first, report); err != nil {
		t.Fatal(err)
	}
	if err := renderAllInsightsDigest(&second, report); err != nil {
		t.Fatal(err)
	}
	if first.String() != second.String() {
		t.Fatal("all-repository digest is not deterministic")
	}
	if first.Len() > digestMaxBytes {
		t.Fatalf("digest length = %d, want <= %d", first.Len(), digestMaxBytes)
	}
	if !utf8.Valid(first.Bytes()) {
		t.Fatal("all-repository digest is not valid UTF-8")
	}
	for _, want := range []string{"… 10 more repositories", "repository evidence: repo-00, repo-01", "session_timeout: 2 — observed across 2 plans / 2 repositories; latest 2026-08-18", "tool-02: 10 occurrences across 4 plans / 3 repositories"} {
		if !strings.Contains(first.String(), want) {
			t.Errorf("bounded digest missing %q:\n%s", want, first.String())
		}
	}
	for _, excluded := range []string{"repo-08`: scanned", "tool-03:"} {
		if strings.Contains(first.String(), excluded) {
			t.Errorf("bounded digest contains %q:\n%s", excluded, first.String())
		}
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
	if out.String() != "# Tao Insights Digest\n\nNo plan history.\n\n## Planner routing\n- No planner routing records.\n" {
		t.Fatalf("empty digest = %q", out.String())
	}
}
