package view

import (
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/iamseth/tao/internal/insights"
)

const (
	digestMaxBuckets                = 5
	digestMaxExemplars              = 1
	digestMaxReworkPlans            = 5
	digestMaxOutlierPlans           = 5
	digestMaxTextBytes              = 160
	digestMaxBytes                  = 4096
	allDigestMaxSources             = 8
	allDigestMaxSignals             = 3
	allDigestMaxPatternRepositories = 3
	allReportMaxSignals             = 20
)

// InsightsScope selects whether repository qualification and coverage are rendered.
type InsightsScope string

const (
	InsightsScopeRepository      InsightsScope = "repository"
	InsightsScopeAllRepositories InsightsScope = "all-repositories"
)

// InsightsFormat selects the full text report or bounded Markdown digest.
type InsightsFormat string

const (
	InsightsFormatReport InsightsFormat = "report"
	InsightsFormatDigest InsightsFormat = "digest"
)

// InsightsOptions defines the presentation variant for an insights report.
type InsightsOptions struct {
	Scope  InsightsScope
	Format InsightsFormat
}

// RenderInsights renders an aggregated report through the selected presentation.
func RenderInsights(out io.Writer, report insights.Report, options InsightsOptions) error {
	if out == nil {
		return errors.New("insights output writer is required")
	}
	if options.Scope != InsightsScopeRepository && options.Scope != InsightsScopeAllRepositories {
		return fmt.Errorf("invalid insights scope %q", options.Scope)
	}
	if options.Format != InsightsFormatReport && options.Format != InsightsFormatDigest {
		return fmt.Errorf("invalid insights format %q", options.Format)
	}

	projection := projectInsights(report, options)
	return projection.render(out)
}

type insightsSection uint8

const (
	insightsSectionCoverage insightsSection = iota
	insightsSectionPatterns
	insightsSectionRework
	insightsSectionSignals
	insightsSectionTelemetry
	insightsSectionOutliers
	insightsSectionRecentLogs
)

// insightsProjection is the common ordered presentation model for every scope
// and format. Rendering consumes the same report-backed sections and varies only
// their labels, limits, and line layout.
type insightsProjection struct {
	report   insights.Report
	options  InsightsOptions
	sections []insightsSection
}

func projectInsights(report insights.Report, options InsightsOptions) insightsProjection {
	sections := []insightsSection{
		insightsSectionPatterns,
		insightsSectionRework,
		insightsSectionSignals,
		insightsSectionTelemetry,
		insightsSectionOutliers,
	}
	if options.Scope == InsightsScopeAllRepositories {
		sections = append([]insightsSection{insightsSectionCoverage}, sections...)
		sections = append(sections, insightsSectionRecentLogs)
		if options.Format == InsightsFormatDigest {
			sections[len(sections)-2], sections[len(sections)-1] = sections[len(sections)-1], sections[len(sections)-2]
		}
	}
	return insightsProjection{report: report, options: options, sections: sections}
}

func (p insightsProjection) render(out io.Writer) error {
	target := out
	var digest strings.Builder
	if p.options.Format == InsightsFormatDigest {
		target = &digest
	}
	stop, err := p.renderHeader(target)
	if err != nil {
		return err
	}
	if !stop {
		for _, section := range p.sections {
			if err := p.renderSection(target, section); err != nil {
				return err
			}
		}
	}
	if p.options.Format == InsightsFormatDigest {
		_, err = io.WriteString(out, limitDigest(digest.String()))
	}
	return err
}

func (p insightsProjection) renderHeader(out io.Writer) (bool, error) {
	all := p.options.Scope == InsightsScopeAllRepositories
	digest := p.options.Format == InsightsFormatDigest
	if digest {
		title := "# Tao Insights Digest"
		if all {
			title = "# Tao All-Repository Insights Digest"
		}
		if err := writeln(out, title); err != nil {
			return false, err
		}
	}
	if !all && p.report.PlansScanned == 0 && p.report.PlansSkipped == 0 {
		prefix := ""
		if digest {
			prefix = "\n"
		}
		return true, writeln(out, prefix+"No plan history.")
	}
	if all {
		coverage := p.report.RepositoryCoverage
		if digest {
			return false, writef(out, "\n- Repositories: %d registered; %d scanned, %d empty, %d unreadable, %d skipped\n- Plans: %d scanned, %d skipped\n", len(coverage.Repositories), coverage.Scanned, coverage.Empty, coverage.Unreadable, coverage.Skipped, p.report.PlansScanned, p.report.PlansSkipped)
		}
		if err := writef(out, "All-repository insights (%d registered; %d scanned, %d empty, %d unreadable, %d skipped)\n", len(coverage.Repositories), coverage.Scanned, coverage.Empty, coverage.Unreadable, coverage.Skipped); err != nil {
			return false, err
		}
		return false, writef(out, "Plans: %d scanned, %d skipped\n", p.report.PlansScanned, p.report.PlansSkipped)
	}
	if digest {
		return false, writef(out, "\n- Plans: %d scanned, %d skipped\n", p.report.PlansScanned, p.report.PlansSkipped)
	}
	return false, writef(out, "Repository insights (%d plans scanned, %d skipped)\n", p.report.PlansScanned, p.report.PlansSkipped)
}

func (p insightsProjection) renderSection(out io.Writer, section insightsSection) error {
	switch section {
	case insightsSectionCoverage:
		return p.renderCoverage(out)
	case insightsSectionPatterns:
		return p.renderPatterns(out)
	case insightsSectionRework:
		return p.renderRework(out)
	case insightsSectionSignals:
		return p.renderSignals(out)
	case insightsSectionTelemetry:
		return p.renderTelemetry(out)
	case insightsSectionOutliers:
		return p.renderOutliers(out)
	case insightsSectionRecentLogs:
		digest := p.options.Format == InsightsFormatDigest
		limit := allReportMaxSignals
		if digest {
			limit = allDigestMaxSignals
		}
		return renderRecentLogSignals(out, p.report.RecentLogs, limit, digest)
	default:
		return fmt.Errorf("unknown insights section %d", section)
	}
}

func (p insightsProjection) renderCoverage(out io.Writer) error {
	coverage := p.report.RepositoryCoverage
	digest := p.options.Format == InsightsFormatDigest
	if digest {
		if err := writeln(out, "\n## Repository coverage"); err != nil {
			return err
		}
		if len(coverage.Repositories) == 0 {
			return writeln(out, "- None registered")
		}
		for _, source := range coverage.Repositories[:min(len(coverage.Repositories), allDigestMaxSources)] {
			if err := writef(out, "- `%s`: %s\n", limitDigestText(repositoryLabel(source.RepositoryName, source.RepositoryID)), source.Status); err != nil {
				return err
			}
		}
		if len(coverage.Repositories) > allDigestMaxSources {
			if err := writef(out, "- … %d more repositories\n", len(coverage.Repositories)-allDigestMaxSources); err != nil {
				return err
			}
		}
		return writeCoverageWarnings(out, coverage)
	}
	if err := writeln(out, "\nRepository coverage:"); err != nil {
		return err
	}
	if len(coverage.Repositories) == 0 {
		return writeln(out, "  none registered")
	}
	for _, source := range coverage.Repositories {
		if err := writef(out, "  %s: %s\n", repositoryLabel(source.RepositoryName, source.RepositoryID), source.Status); err != nil {
			return err
		}
	}
	return writeCoverageWarnings(out, coverage)
}

func (p insightsProjection) renderPatterns(out io.Writer) error {
	all := p.options.Scope == InsightsScopeAllRepositories
	digest := p.options.Format == InsightsFormatDigest
	heading, prefix, none := "\nFailure patterns:", "  ", "none"
	if all {
		heading = "\nRepository-qualified failure patterns:"
	}
	if digest {
		heading, prefix, none = "\n## Failure patterns", "- ", "None"
		if all {
			heading = "\n## Repository-qualified patterns"
		}
	}
	if err := writeln(out, heading); err != nil {
		return err
	}
	if len(p.report.BlockedReasons) == 0 {
		return writef(out, "%s%s\n", prefix, none)
	}
	buckets := p.report.BlockedReasons
	if digest {
		buckets = buckets[:min(len(buckets), digestMaxBuckets)]
	}
	for _, bucket := range buckets {
		if digest {
			if err := writef(out, "- `%s`: %d", limitDigestText(bucket.Reason), bucket.Count); err != nil {
				return err
			}
			if all {
				if evidence := repositoryEvidence(bucket); evidence != "" {
					if err := writef(out, " — repository evidence: %s", limitDigestText(evidence)); err != nil {
						return err
					}
				}
			} else if exemplars := bucket.Exemplars[:min(len(bucket.Exemplars), digestMaxExemplars)]; len(exemplars) > 0 {
				if err := writef(out, " — %s", limitDigestText(strings.Join(exemplars, "; "))); err != nil {
					return err
				}
			}
			if err := writeln(out, ""); err != nil {
				return err
			}
			continue
		}
		if err := writef(out, "  %s: %d\n", bucket.Reason, bucket.Count); err != nil {
			return err
		}
		if all {
			for _, exemplar := range bucket.QualifiedExemplars {
				if err := writef(out, "    - %s: %s\n", repositoryLabel(exemplar.RepositoryName, exemplar.RepositoryID), exemplar.Value); err != nil {
					return err
				}
			}
		} else {
			for _, exemplar := range bucket.Exemplars {
				if err := writef(out, "    - %s\n", exemplar); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func (p insightsProjection) renderRework(out io.Writer) error {
	all := p.options.Scope == InsightsScopeAllRepositories
	digest := p.options.Format == InsightsFormatDigest
	heading, prefix, none := "\nRework-loop plans:", "  ", "none"
	if all {
		heading = "\nRepository-qualified rework loops:"
	}
	if digest {
		heading, prefix, none = "\n## Rework loops", "- ", "None"
	}
	if err := writeln(out, heading); err != nil {
		return err
	}
	if len(p.report.ReworkPlans) == 0 {
		return writef(out, "%s%s\n", prefix, none)
	}
	items := p.report.ReworkPlans
	if digest {
		items = items[:min(len(items), digestMaxReworkPlans)]
	}
	for _, item := range items {
		label := item.PlanID
		if all {
			label = qualifiedPlan(item.RepositoryName, item.RepositoryID, item.PlanID)
		}
		if digest {
			if err := renderDigestReworkPlan(out, label, item); err != nil {
				return err
			}
			continue
		}
		if err := writef(out, "  %s: %d rounds", label, item.Rounds); err != nil {
			return err
		}
		if !all && len(item.StoppedReasons) > 0 {
			if err := writef(out, " (%s)", strings.Join(item.StoppedReasons, "; ")); err != nil {
				return err
			}
		}
		if err := writeln(out, ""); err != nil {
			return err
		}
	}
	return nil
}

func (p insightsProjection) renderSignals(out io.Writer) error {
	heading, prefix := "\nEvent counters:", "  "
	if p.options.Scope == InsightsScopeAllRepositories {
		heading = "\nStructured event counters:"
	}
	if p.options.Format == InsightsFormatDigest {
		heading, prefix = "\n## Event counters", "- "
		if p.options.Scope == InsightsScopeAllRepositories {
			heading = "\n## Structured event counters"
		}
	}
	return writeSignalCounts(out, heading, p.report.Signals, prefix)
}

func (p insightsProjection) renderTelemetry(out io.Writer) error {
	heading, outputLabel, costLabel := "\nSession telemetry:", "  output tokens", "  cost"
	if p.options.Scope == InsightsScopeAllRepositories {
		heading = "\nGlobal session telemetry:"
	}
	if p.options.Format == InsightsFormatDigest {
		heading, outputLabel, costLabel = "\n## Session telemetry", "- Output tokens", "- Cost"
		if p.options.Scope == InsightsScopeAllRepositories {
			heading = "\n## Global session telemetry"
		}
	}
	if err := writeln(out, heading); err != nil {
		return err
	}
	if err := writePercentiles(out, outputLabel, p.report.OutputTokens, false); err != nil {
		return err
	}
	return writePercentiles(out, costLabel, p.report.Cost, true)
}

func (p insightsProjection) renderOutliers(out io.Writer) error {
	all := p.options.Scope == InsightsScopeAllRepositories
	digest := p.options.Format == InsightsFormatDigest
	heading, prefix, none := "\nOutlier plans:", "  ", "none"
	if all {
		heading = "\nRepository-qualified outlier plans:"
	}
	if digest {
		heading, prefix, none = "\n## Outlier plans", "- ", "None"
		if all {
			heading = "\n## Repository-qualified outlier plans"
		}
	}
	if err := writeln(out, heading); err != nil {
		return err
	}
	if len(p.report.OutlierPlans) == 0 {
		return writef(out, "%s%s\n", prefix, none)
	}
	items := p.report.OutlierPlans
	if digest {
		items = items[:min(len(items), digestMaxOutlierPlans)]
	}
	for _, item := range items {
		label := item.PlanID
		if all {
			label = qualifiedPlan(item.RepositoryName, item.RepositoryID, item.PlanID)
		}
		if digest {
			if err := writef(out, "- `%s`: output_tokens=%d, cost=$%.2f\n", limitDigestText(label), item.OutputTokens, item.Cost); err != nil {
				return err
			}
		} else if all {
			if err := writef(out, "  %s: output_tokens=%d cost=$%.2f\n", label, item.OutputTokens, item.Cost); err != nil {
				return err
			}
		} else if err := writef(out, "  %s: output_tokens=%d cost=$%.2f (output=%t, cost=%t)\n", label, item.OutputTokens, item.Cost, item.OutputTokensOutlier, item.CostOutlier); err != nil {
			return err
		}
	}
	return nil
}

func writeCoverageWarnings(out io.Writer, coverage insights.RepositoryCoverage) error {
	wroteHeading := false
	for _, source := range coverage.Repositories {
		if source.Status != "skipped" && source.Status != "unreadable" {
			continue
		}
		if !wroteHeading {
			if err := writeln(out, "\nSkipped-source warnings:"); err != nil {
				return err
			}
			wroteHeading = true
		}
		if err := writef(out, "  - %s: %s plan store\n", repositoryLabel(source.RepositoryName, source.RepositoryID), source.Status); err != nil {
			return err
		}
	}
	return nil
}

func renderRecentLogSignals(out io.Writer, report insights.RecentLogReport, limit int, digest bool) error {
	coverage := report.Coverage
	heading := "\nRecent agent-log signals (cutoff: plan activity within the last 30 days):"
	prefix := "  "
	if digest {
		heading = "\n## Recent environment and tool signals\n- Cutoff: plan activity within the last 30 days"
		prefix = "- "
	}
	if err := writeln(out, heading); err != nil {
		return err
	}
	if err := writef(out, "%sCoverage: eligible=%d scanned=%d missing_recency=%d outside_window=%d missing=%d unreadable=%d unsupported=%d oversized=%d work_limited=%d\n", prefix, coverage.Eligible, coverage.Scanned, coverage.MissingRecency, coverage.OutsideWindow, coverage.Missing, coverage.Unreadable, coverage.Unsupported, coverage.Oversized, coverage.WorkLimited); err != nil {
		return err
	}
	sections := []struct {
		name    string
		signals []insights.LogSignal
	}{
		{"Missing executables", report.MissingExecutables},
		{"Tool usage", report.ToolUses},
		{"External systems", report.ExternalSystems},
	}
	for _, section := range sections {
		if err := writef(out, "%s%s:\n", prefix, section.name); err != nil {
			return err
		}
		if len(section.signals) == 0 {
			if err := writef(out, "%s  none\n", prefix); err != nil {
				return err
			}
			continue
		}
		for _, signal := range section.signals[:min(len(section.signals), limit)] {
			if err := writef(out, "%s  %s: %d occurrences across %d plans / %d repositories\n", prefix, limitDigestText(signal.Name), signal.Count, signal.PlanCount, signal.RepositoryCount); err != nil {
				return err
			}
			if !digest && len(signal.Exemplars) > 0 {
				exemplar := signal.Exemplars[0]
				if err := writef(out, "%s    - %s: %s\n", prefix, qualifiedPlan(exemplar.RepositoryName, exemplar.RepositoryID, exemplar.PlanID), exemplar.Excerpt); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func repositoryEvidence(bucket insights.ReasonBucket) string {
	labels := make([]string, 0, allDigestMaxPatternRepositories)
	for _, repository := range bucket.Repositories[:min(len(bucket.Repositories), allDigestMaxPatternRepositories)] {
		labels = append(labels, repositoryLabel(repository.RepositoryName, repository.RepositoryID))
	}
	if len(bucket.Repositories) > allDigestMaxPatternRepositories {
		labels = append(labels, "… "+strconv.Itoa(len(bucket.Repositories)-allDigestMaxPatternRepositories)+" more repositories")
	}
	if len(labels) > 0 {
		return strings.Join(labels, ", ")
	}

	// Preserve rendering for repository-qualified reports created by older callers.
	for _, exemplar := range bucket.QualifiedExemplars {
		label := repositoryLabel(exemplar.RepositoryName, exemplar.RepositoryID)
		if !slices.Contains(labels, label) {
			labels = append(labels, label)
		}
	}
	return strings.Join(labels, ", ")
}

func repositoryLabel(name, id string) string {
	if name == "" {
		return id
	}
	if id == "" || id == name {
		return name
	}
	return name + " [" + id + "]"
}

func qualifiedPlan(name, id, planID string) string {
	label := repositoryLabel(name, id)
	if label == "" {
		return planID
	}
	return label + "/" + planID
}

func renderDigestReworkPlan(out io.Writer, label string, item insights.ReworkPlan) error {
	if err := writef(out, "- `%s`: %d rounds", limitDigestText(label), item.Rounds); err != nil {
		return err
	}
	if len(item.StoppedReasons) > 0 {
		if err := writef(out, " — stopped: %s", limitDigestText(strings.Join(item.StoppedReasons, "; "))); err != nil {
			return err
		}
	}
	return writeln(out, "")
}

func limitDigestText(value string) string {
	value = strings.Join(strings.Fields(value), " ")
	if len(value) <= digestMaxTextBytes {
		return value
	}
	return truncateUTF8(value, digestMaxTextBytes-len("…")) + "…"
}

func limitDigest(value string) string {
	if len(value) <= digestMaxBytes {
		return value
	}
	const suffix = "\n… digest truncated\n"
	return truncateUTF8(value, digestMaxBytes-len(suffix)) + suffix
}

func truncateUTF8(value string, maxBytes int) string {
	if len(value) <= maxBytes {
		return value
	}
	value = value[:maxBytes]
	for !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value
}

func writeSignalCounts(out io.Writer, heading string, signals insights.SignalCounts, prefix string) error {
	if err := writeln(out, heading); err != nil {
		return err
	}
	rows := []struct {
		name  string
		count int
	}{
		{"session_timeout", signals.SessionTimeout},
		{"slice_resume_failed", signals.SliceResumeFailed},
		{"verification_command_invalid", signals.VerificationCommandInvalid},
		{"plan_commit_fallback", signals.PlanCommitFallback},
		{"plan_commit_guard", signals.PlanCommitGuard},
	}
	for _, row := range rows {
		if err := writef(out, "%s%s: %d\n", prefix, row.name, row.count); err != nil {
			return err
		}
	}
	return nil
}

func writePercentiles(out io.Writer, label string, values insights.Percentiles, cost bool) error {
	if cost {
		return writef(out, "%s (%d sessions): p50=$%.2f p90=$%.2f p95=$%.2f\n", label, values.Sessions, values.P50, values.P90, values.P95)
	}
	return writef(out, "%s (%d sessions): p50=%.0f p90=%.0f p95=%.0f\n", label, values.Sessions, values.P50, values.P90, values.P95)
}

func writef(out io.Writer, format string, args ...any) error {
	_, err := fmt.Fprintf(out, format, args...)
	return err
}

func writeln(out io.Writer, value string) error {
	_, err := fmt.Fprintln(out, value)
	return err
}
