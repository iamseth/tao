package cli

import (
	"context"
	"errors"
	"flag"

	"github.com/iamseth/tao/internal/insights"
	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/internal/taodata"
	"github.com/iamseth/tao/internal/view"
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
	scope := view.InsightsScopeRepository
	var report insights.Report
	if allRepos {
		report, err = insights.AggregateSources(ctx, catalogInsightSources{
			registry:   taodata.NewRegistry(""),
			repository: a.repository,
		})
		scope = view.InsightsScopeAllRepositories
	} else {
		report, err = insights.Aggregate(ctx, repo)
	}
	if err != nil {
		return err
	}
	format := view.InsightsFormatReport
	if flagBoolValue(fs, "digest") {
		format = view.InsightsFormatDigest
	}
	return view.RenderInsights(a.Out, report, view.InsightsOptions{Scope: scope, Format: format})
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
