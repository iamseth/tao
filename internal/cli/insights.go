package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"

	"github.com/iamseth/tao/internal/commandrunner"

	"github.com/iamseth/tao/internal/insights"
	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/internal/plannerroute"
	"github.com/iamseth/tao/internal/taodata"
	"github.com/iamseth/tao/internal/view"
)

var insightsCommand = commandMetadata{
	name:                  "insights",
	minPrefix:             "insi",
	usageLines:            []string{"insights (insi) [--digest|--scorecard|--trend [--json]] [--all-repos]"},
	completionDescription: "Show repository failure and telemetry insights",
	long:                  "Summarize failure patterns, rework loops, operational events, and agent usage across plan history. Use --all-repos to read every registered repository's data-home history, including repositories whose source root is missing. Use --digest for compact deterministic Markdown suitable for planning prompts, or --scorecard for planner cohorts, coverage, and downstream outcomes (not a causal ranking). Use --trend for monthly creation-cohort trends or --trend --json for dedicated JSON; trend cannot be combined with digest or scorecard.",
	examples: "  tao insights\n" +
		"  tao insights --digest\n" +
		"  tao insights --all-repos --digest\n" +
		"  tao insights --scorecard\n" +
		"  tao insights --all-repos --scorecard\n" +
		"  tao insights --trend\n" +
		"  tao insights --trend --json\n" +
		"  tao insights --all-repos --trend",
	registerFlags: registerInsightsFlags,
	repository:    repositoryDefault,
	execute: func(c commandContext) error {
		return c.app.insights(c.ctx, c.repo, c.plansDir, c.args)
	},
}

func registerInsightsFlags(fs *flag.FlagSet) {
	fs.Bool("trend", false, "write monthly creation-cohort trends")
	fs.Bool("json", false, "write dedicated JSON (requires --trend)")
	fs.Bool("digest", false, "write compact deterministic Markdown")
	fs.Bool("scorecard", false, "write the planner scorecard")
	fs.Bool("all-repos", false, "include plan history from all registered repositories")
}

func (a App) insights(ctx context.Context, repo insights.PlanLister, plansDir string, args []string) error {
	fs, positional, err := a.parseArgs("insights", args, registerInsightsFlags)
	if err != nil {
		return err
	}
	if err := requireNoArgs(positional, "usage: tao insights [--digest|--scorecard|--trend [--json]] [--all-repos]"); err != nil {
		return err
	}
	if flagBoolValue(fs, "scorecard") && flagBoolValue(fs, "digest") {
		return errors.New("--scorecard cannot be combined with --digest")
	}
	trend := flagBoolValue(fs, "trend")
	if trend && (flagBoolValue(fs, "digest") || flagBoolValue(fs, "scorecard")) {
		return errors.New("--trend cannot be combined with --digest or --scorecard")
	}
	if flagBoolValue(fs, "json") && !trend {
		return errors.New("--json requires --trend")
	}
	options := insights.Options{IncludeTrend: trend}
	allRepos := flagBoolValue(fs, "all-repos")
	if allRepos && plansDir != "" {
		return errors.New("--all-repos cannot be combined with --plans-dir")
	}
	scope := view.InsightsScopeRepository
	var report insights.Report
	if allRepos {
		report, err = insights.AggregateSourcesWithOptions(ctx, catalogInsightSources{
			registry:   taodata.NewRegistry(""),
			repository: a.repository,
			trend:      trend,
			runner:     a.CommandRunner,
		}, options)
		scope = view.InsightsScopeAllRepositories
	} else {
		var routes insights.RouteLister
		// Explicit plan stores need not belong to the current repository.
		if plansDir == "" {
			registry := a.registry()
			registered, lookupErr := registry.Current(ctx)
			if lookupErr == nil {
				routes = insightRoutes{plannerroute.NewStore(registry.PlannerRoutesDir(registered))}
				if trend {
					options.Defects = insights.NewGitDefectCollector(registered.ID, registered.Root, registry.NotesDir(registered), a.CommandRunner)
				}
			} else if errors.Is(lookupErr, context.Canceled) || errors.Is(lookupErr, context.DeadlineExceeded) {
				return lookupErr
			}
		}
		if trend && plansDir != "" {
			if _, err := fmt.Fprintln(a.Err, "Defect evidence unavailable: explicit --plans-dir has no registered repository binding; invocation checkout is not used."); err != nil {
				return err
			}
		}
		options.Routes = routes
		report, err = insights.AggregateWithOptions(ctx, repo, options)
	}
	if err != nil {
		return err
	}
	if trend && flagBoolValue(fs, "json") {
		return view.RenderInsightsTrendJSON(a.Out, *report.Trend, scope)
	}
	format := view.InsightsFormatReport
	if trend {
		format = view.InsightsFormatTrend
	}
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
	trend      bool
	runner     commandrunner.Runner
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
		source := insights.RepositorySource{ID: store.ID, Name: store.Name, Plans: plans, Routes: insightRoutes{plannerroute.NewStore(store.PlannerRoutesDir)}}
		if s.trend {
			// Damaged metadata must not hide independently readable plan history.
			root, notesDir := "", ""
			if registered, err := s.registry.ReadRepo(store.ID); err == nil {
				root, notesDir = registered.Root, s.registry.NotesDir(registered)
			}
			source.Defects = insights.NewGitDefectCollector(store.ID, root, notesDir, s.runner)
		}
		sources = append(sources, source)
	}
	return sources, nil
}
