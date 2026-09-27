package cli

import (
	"context"
	"errors"
	"flag"

	"github.com/iamseth/tao/internal/insights"
	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/internal/plannerroute"
	"github.com/iamseth/tao/internal/taodata"
	"github.com/iamseth/tao/internal/view"
)

var insightsCommand = commandMetadata{
	name:                  "insights",
	minPrefix:             "insi",
	usageLines:            []string{"insights (insi) [--digest|--scorecard] [--all-repos]"},
	completionDescription: "Show repository failure and telemetry insights",
	long:                  "Summarize failure patterns, rework loops, operational events, and agent usage across plan history. Use --all-repos to read every registered repository's data-home history, including repositories whose source root is missing. Use --digest for compact deterministic Markdown suitable for planning prompts, or --scorecard for planner cohorts, coverage, and downstream outcomes (not a causal ranking).",
	examples: "  tao insights\n" +
		"  tao insights --digest\n" +
		"  tao insights --all-repos --digest\n" +
		"  tao insights --scorecard\n" +
		"  tao insights --all-repos --scorecard",
	registerFlags: registerInsightsFlags,
	repository:    repositoryDefault,
	execute: func(c commandContext) error {
		return c.app.insights(c.ctx, c.repo, c.plansDir, c.args)
	},
}

func registerInsightsFlags(fs *flag.FlagSet) {
	fs.Bool("digest", false, "write compact deterministic Markdown")
	fs.Bool("scorecard", false, "write the planner scorecard")
	fs.Bool("all-repos", false, "include plan history from all registered repositories")
}

func (a App) insights(ctx context.Context, repo insights.PlanLister, plansDir string, args []string) error {
	fs, positional, err := a.parseArgs("insights", args, registerInsightsFlags)
	if err != nil {
		return err
	}
	if err := requireNoArgs(positional, "usage: tao insights [--digest|--scorecard] [--all-repos]"); err != nil {
		return err
	}
	if flagBoolValue(fs, "scorecard") && flagBoolValue(fs, "digest") {
		return errors.New("--scorecard cannot be combined with --digest")
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
		var routes insights.RouteLister
		// Explicit plan stores need not belong to the current repository.
		if plansDir == "" {
			registry := a.registry()
			registered, lookupErr := registry.Current(ctx)
			if lookupErr == nil {
				routes = insightRoutes{plannerroute.NewStore(registry.PlannerRoutesDir(registered))}
			} else if errors.Is(lookupErr, context.Canceled) || errors.Is(lookupErr, context.DeadlineExceeded) {
				return lookupErr
			}
		}
		report, err = insights.AggregateWithRoutes(ctx, repo, routes)
	}
	if err != nil {
		return err
	}
	format := view.InsightsFormatReport
	if flagBoolValue(fs, "digest") {
		format = view.InsightsFormatDigest
	}
	if flagBoolValue(fs, "scorecard") {
		format = view.InsightsFormatScorecard
	}
	return view.RenderInsights(a.Out, report, view.InsightsOptions{Scope: scope, Format: format})
}

type insightRoutes struct{ store *plannerroute.Store }

func (r insightRoutes) ListRoutes(ctx context.Context) ([]plannerroute.Record, []string, error) {
	return r.store.List(ctx)
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
		sources = append(sources, insights.RepositorySource{ID: store.ID, Name: store.Name, Plans: plans, Routes: insightRoutes{plannerroute.NewStore(store.PlannerRoutesDir)}})
	}
	return sources, nil
}
