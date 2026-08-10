package view

import (
	"bytes"
	"slices"
	"strings"
	"testing"
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
