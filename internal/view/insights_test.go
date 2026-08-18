package view

import (
	"bytes"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/iamseth/tao/internal/insights"
)

func TestRenderInsightsValidatesOptions(t *testing.T) {
	report := insights.Report{}
	valid := []InsightsOptions{
		{Scope: InsightsScopeRepository, Format: InsightsFormatReport},
		{Scope: InsightsScopeRepository, Format: InsightsFormatDigest},
		{Scope: InsightsScopeAllRepositories, Format: InsightsFormatReport},
		{Scope: InsightsScopeAllRepositories, Format: InsightsFormatDigest},
	}
	for _, options := range valid {
		var out bytes.Buffer
		if err := RenderInsights(&out, report, options); err != nil {
			t.Errorf("RenderInsights(%+v) error = %v", options, err)
		}
	}

	if err := RenderInsights(nil, report, valid[0]); err == nil || !strings.Contains(err.Error(), "writer is required") {
		t.Fatalf("nil writer error = %v", err)
	}

	for _, test := range []struct {
		name    string
		out     *bytes.Buffer
		options InsightsOptions
		want    string
	}{
		{name: "invalid scope", out: &bytes.Buffer{}, options: InsightsOptions{Scope: "organization", Format: InsightsFormatReport}, want: "invalid insights scope"},
		{name: "invalid format", out: &bytes.Buffer{}, options: InsightsOptions{Scope: InsightsScopeRepository, Format: "json"}, want: "invalid insights format"},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := RenderInsights(test.out, report, test.options)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want containing %q", err, test.want)
			}
		})
	}
}

func TestInsightsProjectionOrdersSharedSections(t *testing.T) {
	repositorySections := []insightsSection{
		insightsSectionPatterns,
		insightsSectionRework,
		insightsSectionSignals,
		insightsSectionTelemetry,
		insightsSectionOutliers,
	}
	allReportSections := append([]insightsSection{insightsSectionCoverage}, repositorySections...)
	allReportSections = append(allReportSections, insightsSectionRecentLogs)
	allDigestSections := append([]insightsSection(nil), allReportSections...)
	allDigestSections[len(allDigestSections)-2], allDigestSections[len(allDigestSections)-1] = allDigestSections[len(allDigestSections)-1], allDigestSections[len(allDigestSections)-2]

	for _, test := range []struct {
		name    string
		options InsightsOptions
		want    []insightsSection
	}{
		{name: "repository report", options: InsightsOptions{Scope: InsightsScopeRepository, Format: InsightsFormatReport}, want: repositorySections},
		{name: "repository digest", options: InsightsOptions{Scope: InsightsScopeRepository, Format: InsightsFormatDigest}, want: repositorySections},
		{name: "all-repository report", options: InsightsOptions{Scope: InsightsScopeAllRepositories, Format: InsightsFormatReport}, want: allReportSections},
		{name: "all-repository digest", options: InsightsOptions{Scope: InsightsScopeAllRepositories, Format: InsightsFormatDigest}, want: allDigestSections},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := projectInsights(insights.Report{}, test.options).sections
			if !slices.Equal(got, test.want) {
				t.Fatalf("sections = %v, want %v", got, test.want)
			}
		})
	}
}

func TestSelectDigestOutliers(t *testing.T) {
	out := func(repository, plan string, tokens int64) insights.PlanOutlier {
		return insights.PlanOutlier{RepositoryID: repository, PlanID: plan, OutputTokens: tokens, OutputTokensOutlier: true}
	}
	cost := func(repository, plan string, value float64) insights.PlanOutlier {
		return insights.PlanOutlier{RepositoryID: repository, PlanID: plan, Cost: value, CostOutlier: true}
	}
	both := func(repository, plan string, tokens int64, value float64) insights.PlanOutlier {
		return insights.PlanOutlier{RepositoryID: repository, PlanID: plan, OutputTokens: tokens, Cost: value, OutputTokensOutlier: true, CostOutlier: true}
	}

	for _, test := range []struct {
		name  string
		items []insights.PlanOutlier
		limit int
		want  []string
	}{
		{
			name: "shuffled disjoint rankings",
			items: []insights.PlanOutlier{
				cost("repo", "c2", 20), out("repo", "o2", 200), cost("repo", "c1", 30),
				out("repo", "o1", 300), cost("repo", "c3", 10), out("repo", "o3", 100),
			},
			limit: 5,
			want:  []string{"o1", "c1", "o2", "c2", "o3"},
		},
		{
			name: "overlapping metric leaders",
			items: []insights.PlanOutlier{
				out("repo", "output-2", 90), cost("repo", "cost-2", 80),
				both("repo", "leader", 100, 100), cost("repo", "cost-1", 90), out("repo", "output-3", 80),
			},
			limit: 5,
			want:  []string{"leader", "cost-1", "output-2", "cost-2", "output-3"},
		},
		{
			name: "identity tie breakers",
			items: []insights.PlanOutlier{
				cost("repo-b", "cost", 10), out("repo-a", "zulu", 10), cost("repo-a", "cost", 10),
				out("repo-b", "alpha", 10), out("repo-a", "alpha", 10),
			},
			limit: 5,
			want:  []string{"alpha", "cost", "zulu", "cost", "alpha"},
		},
		{
			name: "fills from unexhausted ranking",
			items: []insights.PlanOutlier{
				cost("repo", "c3", 30), cost("repo", "c1", 50), out("repo", "only-output", 100),
				cost("repo", "c4", 20), cost("repo", "c2", 40), cost("repo", "c5", 10),
			},
			limit: 5,
			want:  []string{"only-output", "c1", "c2", "c3", "c4"},
		},
		{
			name:  "fewer than cap",
			items: []insights.PlanOutlier{cost("repo", "cost", 5), out("repo", "output", 10)},
			limit: 5,
			want:  []string{"output", "cost"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			original := slices.Clone(test.items)
			got, total := selectDigestOutliers(test.items, test.limit)
			gotPlans := make([]string, 0, len(got))
			for _, item := range got {
				gotPlans = append(gotPlans, item.PlanID)
			}
			if !slices.Equal(gotPlans, test.want) {
				t.Fatalf("selected plans = %v, want %v", gotPlans, test.want)
			}
			if total != len(test.items) {
				t.Fatalf("total = %d, want %d", total, len(test.items))
			}
			if !slices.Equal(test.items, original) {
				t.Fatalf("selector mutated input: got %+v, want %+v", test.items, original)
			}
		})
	}
}

func TestRenderOutliersDigestDisclosureAndUncappedReport(t *testing.T) {
	items := make([]insights.PlanOutlier, 0, digestMaxOutlierPlans+2)
	for i, planID := range []string{"g", "f", "e", "d", "c", "b", "a"} {
		items = append(items, insights.PlanOutlier{PlanID: planID, OutputTokens: int64(i), OutputTokensOutlier: true})
	}

	for _, test := range []struct {
		name       string
		items      []insights.PlanOutlier
		want       string
		wantAbsent string
	}{
		{name: "one omitted", items: items[:6], want: "Showing 5 of 6 outlier plans; 1 outlier plan omitted."},
		{name: "multiple omitted", items: items, want: "Showing 5 of 7 outlier plans; 2 outlier plans omitted."},
		{name: "none omitted", items: items[:5], wantAbsent: "omitted"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var rendered bytes.Buffer
			projection := insightsProjection{report: insights.Report{OutlierPlans: test.items}, options: InsightsOptions{Format: InsightsFormatDigest}}
			if err := projection.renderOutliers(&rendered); err != nil {
				t.Fatal(err)
			}
			if test.want != "" && !strings.Contains(rendered.String(), test.want) {
				t.Fatalf("digest missing %q:\n%s", test.want, rendered.String())
			}
			if test.wantAbsent != "" && strings.Contains(rendered.String(), test.wantAbsent) {
				t.Fatalf("digest unexpectedly contains %q:\n%s", test.wantAbsent, rendered.String())
			}
		})
	}

	var report bytes.Buffer
	projection := insightsProjection{report: insights.Report{OutlierPlans: items}, options: InsightsOptions{Format: InsightsFormatReport}}
	if err := projection.renderOutliers(&report); err != nil {
		t.Fatal(err)
	}
	for _, item := range items {
		if !strings.Contains(report.String(), "  "+item.PlanID+":") {
			t.Errorf("uncapped report missing plan %q:\n%s", item.PlanID, report.String())
		}
	}
}

func TestRenderSignalContextByScopeAndFormat(t *testing.T) {
	latest := time.Date(2026, 8, 18, 14, 30, 5, 0, time.FixedZone("fixture", -7*60*60))
	report := insights.Report{
		Signals: insights.SignalCounts{SessionTimeout: 3, SliceResumeFailed: 1, VerificationCommandInvalid: 2},
		SignalEvidence: insights.SignalEvidence{
			SessionTimeout:             insights.SignalObservation{Count: 3, Plans: 2, Repositories: 2, MissingTimestamps: 1, LatestTimestamp: &latest},
			SliceResumeAttempted:       insights.SignalObservation{Count: 4, Plans: 1, Repositories: 1, LatestTimestamp: &latest},
			SliceResumeFailed:          insights.SignalObservation{Count: 1, Plans: 1, Repositories: 1},
			VerificationCommandInvalid: insights.SignalObservation{Count: 2, Plans: 2, Repositories: 1},
		},
	}

	for _, test := range []struct {
		name       string
		options    InsightsOptions
		want       []string
		wantAbsent string
	}{
		{
			name:    "repository report uses timestamps and omits repository breadth",
			options: InsightsOptions{Scope: InsightsScopeRepository, Format: InsightsFormatReport},
			want: []string{
				"session_timeout: 3 — observed across 2 plans; latest timestamped occurrence 2026-08-18T21:30:05Z; timestamps unavailable for 1 of 3 events",
				"slice_resume_attempted: 4 — observed across 1 plan; latest 2026-08-18T21:30:05Z\n  slice_resume_failed: 1",
				"verification_command_invalid: 2 — observed across 2 plans; latest occurrence unavailable (historical events lack timestamps)",
				"plan_commit_guard: 0",
			},
			wantAbsent: "repositories",
		},
		{
			name:    "all-repository digest uses dates and repository breadth",
			options: InsightsOptions{Scope: InsightsScopeAllRepositories, Format: InsightsFormatDigest},
			want: []string{
				"session_timeout: 3 — observed across 2 plans / 2 repositories; latest timestamped occurrence 2026-08-18; timestamps unavailable for 1 of 3 events",
				"slice_resume_attempted: 4 — observed across 1 plan / 1 repository; latest 2026-08-18\n- slice_resume_failed: 1",
				"slice_resume_failed: 1 — observed across 1 plan / 1 repository; latest occurrence unavailable (historical events lack timestamps)",
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			var out bytes.Buffer
			projection := insightsProjection{report: report, options: test.options}
			if err := projection.renderSignals(&out); err != nil {
				t.Fatal(err)
			}
			for _, want := range test.want {
				if !strings.Contains(out.String(), want) {
					t.Errorf("signals missing %q:\n%s", want, out.String())
				}
			}
			if test.wantAbsent != "" && strings.Contains(out.String(), test.wantAbsent) {
				t.Errorf("signals unexpectedly contain %q:\n%s", test.wantAbsent, out.String())
			}
		})
	}
}

func TestInsightsDigestTruncationPreservesUTF8AtBoundaries(t *testing.T) {
	for _, test := range []struct {
		name string
		text string
		max  int
		want string
	}{
		{name: "exact ASCII boundary", text: "abcd", max: 4, want: "abcd"},
		{name: "ASCII over boundary", text: "abcde", max: 4, want: "abcd"},
		{name: "exact multibyte boundary", text: "a界", max: 4, want: "a界"},
		{name: "inside multibyte rune", text: "a界b", max: 3, want: "a"},
		{name: "zero boundary", text: "界", max: 0, want: ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := truncateUTF8(test.text, test.max)
			if got != test.want {
				t.Fatalf("truncateUTF8(%q, %d) = %q, want %q", test.text, test.max, got, test.want)
			}
			if !utf8.ValidString(got) {
				t.Fatalf("result %q is not valid UTF-8", got)
			}
		})
	}
}

func TestInsightsDigestTextCapNormalizesAndPreservesUTF8(t *testing.T) {
	got := limitDigestText("  alpha\n beta  ")
	if got != "alpha beta" {
		t.Fatalf("normalized text = %q", got)
	}

	got = limitDigestText(strings.Repeat("界", digestMaxTextBytes))
	if len(got) > digestMaxTextBytes || !strings.HasSuffix(got, "…") || !utf8.ValidString(got) {
		t.Fatalf("bounded text length=%d valid=%t suffix=%t", len(got), utf8.ValidString(got), strings.HasSuffix(got, "…"))
	}
}
