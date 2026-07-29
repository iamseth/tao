package cli

import (
	"context"
	"errors"
	"flag"
	"io"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/iamseth/tao/internal/insights"
	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/internal/taodata"
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

var insightsCommand = commandMetadata{
	name:                  "insights",
	minPrefix:             "insi",
	usageLines:            []string{"insights (insi) [--digest] [--all-repos]"},
	completionDescription: "Show repository failure and telemetry insights",
	long:                  "Summarize failure patterns, rework loops, operational events, and agent usage across plan history. Use --all-repos to read every registered repository's data-home history, including repositories whose source root is missing. Use --digest for compact deterministic Markdown suitable for planning prompts.",
	examples: "  tao insights\n" +
		"  tao insights --digest\n" +
		"  tao insights --all-repos --digest",
	registerFlags: registerInsightsFlags,
	repository:    repositoryDefault,
	execute: func(c commandContext) error {
		return c.app.insights(c.ctx, c.repo, c.plansDir, c.args)
	},
}

func registerInsightsFlags(fs *flag.FlagSet) {
	fs.Bool("digest", false, "write compact deterministic Markdown")
	fs.Bool("all-repos", false, "include plan history from all registered repositories")
}

func (a App) insights(ctx context.Context, repo insights.PlanLister, plansDir string, args []string) error {
	fs, positional, err := a.parseArgs("insights", args, registerInsightsFlags)
	if err != nil {
		return err
	}
	if err := requireNoArgs(positional, "usage: tao insights [--digest] [--all-repos]"); err != nil {
		return err
	}
	allRepos := flagBoolValue(fs, "all-repos")
	if allRepos && plansDir != "" {
		return errors.New("--all-repos cannot be combined with --plans-dir")
	}
	if allRepos {
		report, aggregateErr := insights.AggregateSources(ctx, catalogInsightSources{
			registry:   taodata.NewRegistry(""),
			repository: a.repository,
		})
		if aggregateErr != nil {
			return aggregateErr
		}
		if flagBoolValue(fs, "digest") {
			return renderAllInsightsDigest(a.Out, report)
		}
		return renderAllInsightsReport(a.Out, report)
	}
	report, err := insights.Aggregate(ctx, repo)
	if err != nil {
		return err
	}
	if flagBoolValue(fs, "digest") {
		return renderInsightsDigest(a.Out, report)
	}
	return renderInsightsReport(a.Out, report)
}

type catalogInsightSources struct {
	registry   taodata.Registry
	repository func(string) Repository
}

func (s catalogInsightSources) ListInsightSources(ctx context.Context) ([]insights.RepositorySource, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	stores, err := s.registry.ListRepoPlanSources()
	if err != nil {
		return nil, err
	}
	sources := make([]insights.RepositorySource, 0, len(stores))
	for _, store := range stores {
		var plans insights.PlanLister = plan.NewFileRepository(store.PlansDir)
		if s.repository != nil {
			plans = s.repository(store.PlansDir)
		}
		sources = append(sources, insights.RepositorySource{ID: store.ID, Name: store.Name, Plans: plans})
	}
	return sources, nil
}

func renderAllInsightsReport(out io.Writer, report insights.Report) error {
	coverage := report.RepositoryCoverage
	if err := writef(out, "All-repository insights (%d registered; %d scanned, %d empty, %d unreadable, %d skipped)\n", len(coverage.Repositories), coverage.Scanned, coverage.Empty, coverage.Unreadable, coverage.Skipped); err != nil {
		return err
	}
	if err := writef(out, "Plans: %d scanned, %d skipped\n\nRepository coverage:\n", report.PlansScanned, report.PlansSkipped); err != nil {
		return err
	}
	if len(coverage.Repositories) == 0 {
		if err := writeln(out, "  none registered"); err != nil {
			return err
		}
	}
	for _, source := range coverage.Repositories {
		if err := writef(out, "  %s: %s\n", repositoryLabel(source.RepositoryName, source.RepositoryID), source.Status); err != nil {
			return err
		}
	}
	if err := writeCoverageWarnings(out, coverage); err != nil {
		return err
	}
	if err := writeln(out, "\nRepository-qualified failure patterns:"); err != nil {
		return err
	}
	if len(report.BlockedReasons) == 0 {
		if err := writeln(out, "  none"); err != nil {
			return err
		}
	}
	for _, bucket := range report.BlockedReasons {
		if err := writef(out, "  %s: %d\n", bucket.Reason, bucket.Count); err != nil {
			return err
		}
		for _, exemplar := range bucket.QualifiedExemplars {
			if err := writef(out, "    - %s: %s\n", repositoryLabel(exemplar.RepositoryName, exemplar.RepositoryID), exemplar.Value); err != nil {
				return err
			}
		}
	}
	if err := writeln(out, "\nRepository-qualified rework loops:"); err != nil {
		return err
	}
	if len(report.ReworkPlans) == 0 {
		if err := writeln(out, "  none"); err != nil {
			return err
		}
	}
	for _, item := range report.ReworkPlans {
		if err := writef(out, "  %s: %d rounds\n", qualifiedPlan(item.RepositoryName, item.RepositoryID, item.PlanID), item.Rounds); err != nil {
			return err
		}
	}
	if err := writeSignalCounts(out, "\nStructured event counters:", report.Signals, "  "); err != nil {
		return err
	}
	if err := writeln(out, "\nGlobal session telemetry:"); err != nil {
		return err
	}
	if err := writePercentiles(out, "  output tokens", report.OutputTokens, false); err != nil {
		return err
	}
	if err := writePercentiles(out, "  cost", report.Cost, true); err != nil {
		return err
	}
	if err := writeln(out, "\nRepository-qualified outlier plans:"); err != nil {
		return err
	}
	if len(report.OutlierPlans) == 0 {
		if err := writeln(out, "  none"); err != nil {
			return err
		}
	}
	for _, item := range report.OutlierPlans {
		if err := writef(out, "  %s: output_tokens=%d cost=$%.2f\n", qualifiedPlan(item.RepositoryName, item.RepositoryID, item.PlanID), item.OutputTokens, item.Cost); err != nil {
			return err
		}
	}
	return renderRecentLogSignals(out, report.RecentLogs, allReportMaxSignals, false)
}

func renderAllInsightsDigest(out io.Writer, report insights.Report) error {
	var digest strings.Builder
	coverage := report.RepositoryCoverage
	if err := writeln(&digest, "# Tao All-Repository Insights Digest"); err != nil {
		return err
	}
	if err := writef(&digest, "\n- Repositories: %d registered; %d scanned, %d empty, %d unreadable, %d skipped\n- Plans: %d scanned, %d skipped\n", len(coverage.Repositories), coverage.Scanned, coverage.Empty, coverage.Unreadable, coverage.Skipped, report.PlansScanned, report.PlansSkipped); err != nil {
		return err
	}
	if err := writeln(&digest, "\n## Repository coverage"); err != nil {
		return err
	}
	if len(coverage.Repositories) == 0 {
		if err := writeln(&digest, "- None registered"); err != nil {
			return err
		}
	}
	for _, source := range coverage.Repositories[:min(len(coverage.Repositories), allDigestMaxSources)] {
		if err := writef(&digest, "- `%s`: %s\n", limitDigestText(repositoryLabel(source.RepositoryName, source.RepositoryID)), source.Status); err != nil {
			return err
		}
	}
	if len(coverage.Repositories) > allDigestMaxSources {
		if err := writef(&digest, "- … %d more repositories\n", len(coverage.Repositories)-allDigestMaxSources); err != nil {
			return err
		}
	}
	if err := writeCoverageWarnings(&digest, coverage); err != nil {
		return err
	}
	if err := writeln(&digest, "\n## Repository-qualified patterns"); err != nil {
		return err
	}
	if len(report.BlockedReasons) == 0 {
		if err := writeln(&digest, "- None"); err != nil {
			return err
		}
	}
	for _, bucket := range report.BlockedReasons[:min(len(report.BlockedReasons), digestMaxBuckets)] {
		if err := writef(&digest, "- `%s`: %d", limitDigestText(bucket.Reason), bucket.Count); err != nil {
			return err
		}
		if evidence := repositoryEvidence(bucket); evidence != "" {
			if err := writef(&digest, " — repository evidence: %s", limitDigestText(evidence)); err != nil {
				return err
			}
		}
		if err := writeln(&digest, ""); err != nil {
			return err
		}
	}
	if err := writeln(&digest, "\n## Rework loops"); err != nil {
		return err
	}
	if len(report.ReworkPlans) == 0 {
		if err := writeln(&digest, "- None"); err != nil {
			return err
		}
	}
	for _, item := range report.ReworkPlans[:min(len(report.ReworkPlans), digestMaxReworkPlans)] {
		if err := writef(&digest, "- `%s`: %d rounds\n", limitDigestText(qualifiedPlan(item.RepositoryName, item.RepositoryID, item.PlanID)), item.Rounds); err != nil {
			return err
		}
	}
	if err := writeSignalCounts(&digest, "\n## Structured event counters", report.Signals, "- "); err != nil {
		return err
	}
	if err := writeln(&digest, "\n## Global session telemetry"); err != nil {
		return err
	}
	if err := writePercentiles(&digest, "- Output tokens", report.OutputTokens, false); err != nil {
		return err
	}
	if err := writePercentiles(&digest, "- Cost", report.Cost, true); err != nil {
		return err
	}
	if err := renderRecentLogSignals(&digest, report.RecentLogs, allDigestMaxSignals, true); err != nil {
		return err
	}
	if err := writeln(&digest, "\n## Repository-qualified outlier plans"); err != nil {
		return err
	}
	if len(report.OutlierPlans) == 0 {
		if err := writeln(&digest, "- None"); err != nil {
			return err
		}
	}
	for _, item := range report.OutlierPlans[:min(len(report.OutlierPlans), digestMaxOutlierPlans)] {
		if err := writef(&digest, "- `%s`: output_tokens=%d, cost=$%.2f\n", limitDigestText(qualifiedPlan(item.RepositoryName, item.RepositoryID, item.PlanID)), item.OutputTokens, item.Cost); err != nil {
			return err
		}
	}
	_, err := io.WriteString(out, limitDigest(digest.String()))
	return err
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

func renderInsightsReport(out io.Writer, report insights.Report) error {
	if report.PlansScanned == 0 && report.PlansSkipped == 0 {
		return writeln(out, "No plan history.")
	}
	if err := writef(out, "Repository insights (%d plans scanned, %d skipped)\n\n", report.PlansScanned, report.PlansSkipped); err != nil {
		return err
	}
	if err := writeln(out, "Failure patterns:"); err != nil {
		return err
	}
	if len(report.BlockedReasons) == 0 {
		if err := writeln(out, "  none"); err != nil {
			return err
		}
	}
	for _, bucket := range report.BlockedReasons {
		if err := writef(out, "  %s: %d\n", bucket.Reason, bucket.Count); err != nil {
			return err
		}
		for _, exemplar := range bucket.Exemplars {
			if err := writef(out, "    - %s\n", exemplar); err != nil {
				return err
			}
		}
	}
	if err := writeln(out, "\nRework-loop plans:"); err != nil {
		return err
	}
	if len(report.ReworkPlans) == 0 {
		if err := writeln(out, "  none"); err != nil {
			return err
		}
	}
	for _, item := range report.ReworkPlans {
		if err := writef(out, "  %s: %d rounds", item.PlanID, item.Rounds); err != nil {
			return err
		}
		if len(item.StoppedReasons) > 0 {
			if err := writef(out, " (%s)", strings.Join(item.StoppedReasons, "; ")); err != nil {
				return err
			}
		}
		if err := writeln(out, ""); err != nil {
			return err
		}
	}
	if err := writeSignalCounts(out, "\nEvent counters:", report.Signals, "  "); err != nil {
		return err
	}
	if err := writeln(out, "\nSession telemetry:"); err != nil {
		return err
	}
	if err := writePercentiles(out, "  output tokens", report.OutputTokens, false); err != nil {
		return err
	}
	if err := writePercentiles(out, "  cost", report.Cost, true); err != nil {
		return err
	}
	if err := writeln(out, "\nOutlier plans:"); err != nil {
		return err
	}
	if len(report.OutlierPlans) == 0 {
		return writeln(out, "  none")
	}
	for _, item := range report.OutlierPlans {
		if err := writef(out, "  %s: output_tokens=%d cost=$%.2f (output=%t, cost=%t)\n", item.PlanID, item.OutputTokens, item.Cost, item.OutputTokensOutlier, item.CostOutlier); err != nil {
			return err
		}
	}
	return nil
}

func renderInsightsDigest(out io.Writer, report insights.Report) error {
	var digest strings.Builder
	if err := renderInsightsDigestContent(&digest, report); err != nil {
		return err
	}
	_, err := io.WriteString(out, limitDigest(digest.String()))
	return err
}

func renderInsightsDigestContent(out io.Writer, report insights.Report) error {
	if err := writeln(out, "# Tao Insights Digest"); err != nil {
		return err
	}
	if report.PlansScanned == 0 && report.PlansSkipped == 0 {
		return writeln(out, "\nNo plan history.")
	}
	if err := writef(out, "\n- Plans: %d scanned, %d skipped\n", report.PlansScanned, report.PlansSkipped); err != nil {
		return err
	}
	if err := writeln(out, "\n## Failure patterns"); err != nil {
		return err
	}
	if len(report.BlockedReasons) == 0 {
		if err := writeln(out, "- None"); err != nil {
			return err
		}
	}
	for _, bucket := range report.BlockedReasons[:min(len(report.BlockedReasons), digestMaxBuckets)] {
		exemplars := bucket.Exemplars[:min(len(bucket.Exemplars), digestMaxExemplars)]
		if err := writef(out, "- `%s`: %d", limitDigestText(bucket.Reason), bucket.Count); err != nil {
			return err
		}
		if len(exemplars) > 0 {
			if err := writef(out, " — %s", limitDigestText(strings.Join(exemplars, "; "))); err != nil {
				return err
			}
		}
		if err := writeln(out, ""); err != nil {
			return err
		}
	}
	if err := writeln(out, "\n## Rework loops"); err != nil {
		return err
	}
	if len(report.ReworkPlans) == 0 {
		if err := writeln(out, "- None"); err != nil {
			return err
		}
	}
	for _, item := range report.ReworkPlans[:min(len(report.ReworkPlans), digestMaxReworkPlans)] {
		if err := writef(out, "- `%s`: %d rounds\n", limitDigestText(item.PlanID), item.Rounds); err != nil {
			return err
		}
	}
	if err := writeSignalCounts(out, "\n## Event counters", report.Signals, "- "); err != nil {
		return err
	}
	if err := writeln(out, "\n## Session telemetry"); err != nil {
		return err
	}
	if err := writePercentiles(out, "- Output tokens", report.OutputTokens, false); err != nil {
		return err
	}
	if err := writePercentiles(out, "- Cost", report.Cost, true); err != nil {
		return err
	}
	if err := writeln(out, "\n## Outlier plans"); err != nil {
		return err
	}
	if len(report.OutlierPlans) == 0 {
		return writeln(out, "- None")
	}
	for _, item := range report.OutlierPlans[:min(len(report.OutlierPlans), digestMaxOutlierPlans)] {
		if err := writef(out, "- `%s`: output_tokens=%d, cost=$%.2f\n", limitDigestText(item.PlanID), item.OutputTokens, item.Cost); err != nil {
			return err
		}
	}
	return nil
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
