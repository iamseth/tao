package cli

import (
	"context"
	"flag"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/iamseth/tao/internal/insights"
)

const (
	digestMaxBuckets      = 5
	digestMaxExemplars    = 1
	digestMaxReworkPlans  = 5
	digestMaxOutlierPlans = 5
	digestMaxTextBytes    = 160
	digestMaxBytes        = 4096
)

var insightsCommand = commandMetadata{
	name:                  "insights",
	minPrefix:             "insi",
	usageLines:            []string{"insights (insi) [--digest]"},
	completionDescription: "Show repository failure and telemetry insights",
	long:                  "Summarize failure patterns, rework loops, operational events, and agent usage across the current repository's plan history. Use --digest for compact deterministic Markdown suitable for planning prompts.",
	examples: "  tao insights\n" +
		"  tao insights --digest",
	registerFlags: registerInsightsFlags,
	repository:    repositoryDefault,
	execute: func(c commandContext) error {
		return c.app.insights(c.ctx, c.repo, c.args)
	},
}

func registerInsightsFlags(fs *flag.FlagSet) {
	fs.Bool("digest", false, "write compact deterministic Markdown")
}

func (a App) insights(ctx context.Context, repo insights.PlanLister, args []string) error {
	fs, positional, err := a.parseArgs("insights", args, registerInsightsFlags)
	if err != nil {
		return err
	}
	if err := requireNoArgs(positional, "usage: tao insights [--digest]"); err != nil {
		return err
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
