package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/iamseth/tao/internal/commandrunner"
	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/internal/run"
	"github.com/iamseth/tao/internal/runqueue"
	"github.com/iamseth/tao/internal/runtimeconfig"
	"github.com/iamseth/tao/internal/taodata"
	planview "github.com/iamseth/tao/internal/view"
)

var queueCommand = commandMetadata{
	name:                  "queue",
	minPrefix:             "q",
	usageLines:            []string{"queue (q) add <plan-id-or-slug-or-path>...", "queue (q) start [--max-parallel N] [--auto-rework] [--max-rework-attempts N]", "queue (q) status [--all]", "queue (q) stop|dequeue <plan-id-or-slug-or-path>"},
	completionDescription: "Manage the durable run queue",
	long:                  "Manage Tao's durable run queue for the current repository. Add runnable plans, drain them through the selected agent, inspect queue state, or remove queued work.",
	examples: "  tao queue add 20260628-1618-kubectl-style-help\n" +
		"  tao queue start --auto-rework --max-rework-attempts 5\n" +
		"  tao queue status --all\n" +
		"  tao queue dequeue my-plan",
	subcommands: []commandSubcommand{
		{
			name:        "add",
			description: "Add one or more plans to the durable run queue",
			completion: completionContext{
				positional: completionPositional{index: 1, label: "plan", completer: completeRunnablePlanIDs, repeat: true},
			},
		},
		{
			name:          "start",
			description:   "Drain the queue and run queued plans",
			registerFlags: registerQueueStartFlags,
			completion: completionContext{flagValues: map[string]completionFlagValue{
				"auto-rework":         {kind: completionValueBoolean, label: "boolean", values: []string{"true", "false"}},
				"max-parallel":        {kind: completionValueCount, label: "count"},
				"max-rework-attempts": {kind: completionValueCount, label: "count"},
			}},
		},
		{
			name:          "status",
			description:   "Show the durable run queue snapshot",
			registerFlags: registerQueueStatusFlags,
		},
		{
			name:        "stop",
			aliases:     []commandSubcommandAlias{"dequeue"},
			description: "Remove a plan from the queue",
			completion: completionContext{
				positional: completionPositional{index: 1, label: "plan", completer: completePlanIDs},
			},
		},
	},
	registerFlags: registerQueueFlags,
	repository:    repositoryDefault,
	execute: func(c commandContext) error {
		return c.app.queue(c.ctx, c.repo, c.args)
	},
}

const queueUsage = "usage: tao queue add|start|status|stop|dequeue"

type queueRepository interface {
	run.Repository
	ListPlans(ctx context.Context, filter plan.PlanFilter) ([]plan.PlanSummary, error)
}

type queueRuntime struct {
	options         run.ResolvedRunOptions
	notifyCommand   string
	skipPermissions bool
	statusReporter  run.StatusReporter
}

var newQueueExecutor = func(repo run.Repository, out io.Writer, options run.Options) runqueue.Executor {
	service := run.NewService(repo, out, options)
	return service.Execute
}

var newQueuePlanOwner = func(repo run.Repository, out io.Writer, options run.Options) runqueue.PlanOwner {
	service := run.NewService(repo, out, options)
	return service.WithPlanRunLock
}

var newQueueRecoveryReviewer = func(repo run.Repository, out io.Writer, options run.Options) runqueue.RecoveryReviewer {
	service := run.NewService(repo, out, options)
	return service.ResumeReview
}

func (a App) queue(ctx context.Context, repo queueRepository, args []string) error {
	if repo == nil {
		return errors.New("queue requires a plan repository")
	}
	if len(args) == 0 {
		return errors.New(queueUsage)
	}
	switch args[0] {
	case "add":
		return a.queueAdd(ctx, repo, args[1:])
	case "start":
		return a.queueStart(ctx, repo, args[1:])
	case "status":
		return a.queueStatus(ctx, repo, args[1:])
	case "stop", "dequeue":
		return a.queueDequeue(ctx, repo, args[1:], args[0])
	default:
		return fmt.Errorf("unknown queue command %q", args[0])
	}
}

func (a App) queueAdd(ctx context.Context, repo queueRepository, args []string) error {
	if len(args) == 0 {
		return errors.New("usage: tao queue add <plan-id-or-slug-or-path> [<plan-id-or-slug-or-path> ...]")
	}
	runtime, err := a.queueRuntimeFromEnv()
	if err != nil {
		return err
	}
	manager, store, err := a.newQueueSnapshotManager(ctx)
	if err != nil {
		return err
	}
	manager.SetDrainingPaused(true)
	manager.SetQueueValidator(queueRunValidator(repo))
	for _, input := range args {
		detail, err := repo.ResolvePlan(ctx, input)
		if err != nil {
			return err
		}
		request := run.Request{Input: detail.State.Plan.ID, ResolvedRunOptions: runtime.options}
		if err := run.CheckRequestCanStart(detail, request); err != nil {
			return err
		}
		entry, err := manager.Enqueue(request)
		if err != nil {
			return err
		}
		if err := writef(a.Out, "Queued %s\n", entry.PlanID); err != nil {
			return err
		}
	}
	return store.SaveSnapshot(manager.Queue())
}

func (a App) queueStart(ctx context.Context, repo queueRepository, args []string) error {
	runtime, err := a.queueRuntimeFromEnv()
	if err != nil {
		return err
	}
	maxParallel, policy, err := parseQueueStartArgs(a, args)
	if err != nil {
		return err
	}
	return a.startQueueDrain(ctx, repo, queueDrainOptions{maxParallel: maxParallel, runtime: runtime, autoReworkPolicy: policy})
}

type queueDrainOptions struct {
	maxParallel      int
	runtime          queueRuntime
	activeOnly       bool
	autoReworkPolicy runtimeconfig.AutoReworkPolicy
	reworkRestart    bool
}

func (a App) startQueueDrain(ctx context.Context, repo queueRepository, options queueDrainOptions) error {
	manager, store, err := a.newQueueManager(ctx, repo, options)
	if err != nil {
		return err
	}
	if err := manager.RecoverInterruptedRuns(); err != nil {
		return err
	}

	stopSignals := runqueue.WatchStopSignals(manager)
	defer stopSignals()

	restartResult, err := enqueueStoppedAutoReworkPlans(ctx, repo, manager, options)
	if err != nil {
		return err
	}
	result, err := runqueue.Reconcile(ctx, queueReconcileLister(repo, options.activeOnly), manager, options.runtime.options)
	if err != nil {
		return err
	}
	result.Runnable += restartResult.Runnable
	result.Enqueued += restartResult.Enqueued
	result.AlreadyQueued += restartResult.AlreadyQueued
	if err := writef(a.Out, "Reconciled queue: %d runnable, %d enqueued, %d already queued\n", result.Runnable, result.Enqueued, result.AlreadyQueued); err != nil {
		return err
	}
	if !manager.StopRequested() {
		manager.SetDrainingPaused(false)
		manager.Drain()
	}

	waitErr := manager.WaitForDrain(ctx)
	snapshot := manager.Queue()
	saveErr := store.SaveSnapshot(snapshot)
	if waitErr != nil {
		return waitErr
	}
	if saveErr != nil {
		return saveErr
	}
	if manager.StopRequested() {
		if err := writeln(a.Out, "Queue drain stopped after in-flight runs finished."); err != nil {
			return err
		}
	} else if err := writeln(a.Out, "Queue drain completed."); err != nil {
		return err
	}
	summary := a.queueDrainSummary(ctx, repo, snapshot)
	if err := writef(a.Out, "Final batch summary: %s\n", formatBatchSummary(summary)); err != nil {
		return err
	}
	if err := renderQueueDrainFailures(a.Out, snapshot); err != nil {
		return err
	}
	runqueue.NotifyBatchComplete(ctx, options.runtime.notifyCommand, summary, a.queueNotifyRunner(), a.Err, a.queueWarningf)
	return nil
}

func registerQueueFlags(fs *flag.FlagSet) {
	registerQueueStartFlags(fs)
	registerQueueStatusFlags(fs)
}

func registerQueueStartFlags(fs *flag.FlagSet) {
	enabled, attempts, _ := reworkEnvDefaults()
	fs.Int("max-parallel", 1, "maximum concurrent plan runs; values >1 are not cross-plan-conflict-safe from the CLI")
	fs.Bool("auto-rework", enabled, "automatically rework plans with requested changes")
	fs.Int("max-rework-attempts", attempts, "maximum automatic rework cycles (0 disables)")
}

func parseQueueStartArgs(a App, args []string) (int, runtimeconfig.AutoReworkPolicy, error) {
	_, _, envErr := reworkEnvDefaults()
	if envErr != nil {
		return 0, runtimeconfig.AutoReworkPolicy{}, envErr
	}
	fs, positional, err := a.parseArgs("queue start", args, registerQueueStartFlags)
	if err != nil {
		return 0, runtimeconfig.AutoReworkPolicy{}, err
	}
	if err := requirePositionals(positional, 0, "usage: tao queue start [--max-parallel N] [--auto-rework] [--max-rework-attempts N]"); err != nil {
		return 0, runtimeconfig.AutoReworkPolicy{}, err
	}
	maxParallel := flagIntValue(fs, "max-parallel")
	if maxParallel < 1 {
		return 0, runtimeconfig.AutoReworkPolicy{}, errors.New("--max-parallel must be at least 1")
	}
	// The drain manager validates this policy against each persisted request.
	// Using the current environment here would miss queue entries added with a
	// different review option.
	policy, err := runtimeconfig.ResolveAutoReworkPolicy(flagBoolValue(fs, "auto-rework"), flagIntValue(fs, "max-rework-attempts"), true)
	if err != nil {
		return 0, runtimeconfig.AutoReworkPolicy{}, err
	}
	return maxParallel, policy, nil
}

func registerQueueStatusFlags(fs *flag.FlagSet) {
	fs.Bool("all", false, "show complete persisted queue history")
}

func (a App) queueStatus(ctx context.Context, repo queueRepository, args []string) error {
	fs, positional, err := a.parseArgs("queue status", args, registerQueueStatusFlags)
	if err != nil {
		return err
	}
	if err := requirePositionals(positional, 0, "usage: tao queue status [--all]"); err != nil {
		return err
	}
	manager, _, err := a.newQueueSnapshotManager(ctx)
	if err != nil {
		return err
	}
	summaries, err := repo.ListPlans(ctx, plan.PlanFilter{})
	if err != nil {
		return err
	}
	view := buildQueueStatusView(manager.Queue(), summaries, a.now(), flagBoolValue(fs, "all"))
	return renderQueueStatus(a.Out, view, outputSupportsColor(a.Out))
}

func (a App) queueDequeue(ctx context.Context, repo queueRepository, args []string, name string) error {
	usage := "usage: tao queue " + name + " <plan-id-or-slug-or-path>"
	if err := requirePositionals(args, 1, usage); err != nil {
		return err
	}
	planID := resolveQueuePlanID(ctx, repo, args[0])
	manager, store, err := a.newQueueSnapshotManager(ctx)
	if err != nil {
		return err
	}
	entry, err := manager.Dequeue(planID)
	if err != nil {
		return err
	}
	if err := store.SaveSnapshot(manager.Queue()); err != nil {
		return err
	}
	return writef(a.Out, "Dequeued %s\n", entry.PlanID)
}

func (a App) newQueueManager(ctx context.Context, repo queueRepository, drain queueDrainOptions) (*runqueue.Manager, runqueue.Store, error) {
	store, err := queueStore(ctx)
	if err != nil {
		return nil, nil, err
	}
	options := run.Options{
		ExecutionConfig: run.ExecutionConfig{ResolvedRunOptions: drain.runtime.options, SkipPermissions: drain.runtime.skipPermissions},
		RunDependencies: run.RunDependencies{CommandRunner: a.CommandRunner, ProcessStarter: a.ProcessStarter, StatusReporter: drain.runtime.statusReporter, SessionLogWriter: a.Out, Now: a.now},
	}
	manager, err := runqueue.NewManager(runqueue.ManagerConfig{
		Context:             ctx,
		Executor:            newQueueExecutor(repo, a.Out, options),
		Clock:               a.now,
		Store:               store,
		Validator:           queueRunValidator(repo),
		RecoveryInspector:   runqueue.NewRecoveryInspector(repo),
		RecoveryReviewer:    newQueueRecoveryReviewer(repo, a.Out, options),
		AutoReworkPolicy:    drain.autoReworkPolicy,
		AutoReworker:        planAutoReworkerWithRestart(repo, a.now, drain.reworkRestart),
		PlanOwner:           newQueuePlanOwner(repo, a.Out, options),
		MaxParallelRuns:     drain.maxParallel,
		StartDrainingPaused: true,
	})
	if err != nil {
		return nil, nil, err
	}
	return manager, store, nil
}

func (a App) newQueueSnapshotManager(ctx context.Context) (*runqueue.Manager, runqueue.Store, error) {
	store, err := queueStore(ctx)
	if err != nil {
		return nil, nil, err
	}
	manager, err := runqueue.NewWithStore(ctx, nil, a.Now, store)
	if err != nil {
		return nil, nil, err
	}
	return manager, store, nil
}

func queueStore(ctx context.Context) (runqueue.Store, error) {
	registry := taodata.NewRegistry("")
	repo, err := registry.Current(ctx)
	if err != nil {
		return nil, err
	}
	return runqueue.NewFileStorePaths(registry.QueuePath(repo), registry.QueueLogPath(repo)), nil
}

func (a App) queueRuntimeFromEnv() (queueRuntime, error) {
	defaults, err := cliEnvDefaults()
	if err != nil {
		return queueRuntime{}, err
	}
	return newQueueRuntime(defaults, runtimeconfig.RunOptionsPatch{}, defaults.SkipPermissions, a.StatusReporter)
}

func newQueueRuntime(defaults envDefaults, overrides runtimeconfig.RunOptionsPatch, skipPermissions bool, reporter run.StatusReporter) (queueRuntime, error) {
	config, err := defaults.runConfig(overrides)
	if err != nil {
		return queueRuntime{}, err
	}
	return queueRuntime{options: config.ResolvedOptions(), notifyCommand: defaults.NotifyCommand, skipPermissions: skipPermissions, statusReporter: reporter}, nil
}

func enqueueStoppedAutoReworkPlans(ctx context.Context, repo queueRepository, manager *runqueue.Manager, options queueDrainOptions) (runqueue.ReconcileResult, error) {
	return runqueue.ReconcileStoppedAutoRework(ctx, repo, manager, runqueue.StoppedAutoReworkOptions{
		Policy:        options.autoReworkPolicy,
		ActiveOnly:    options.activeOnly,
		ReworkRestart: options.reworkRestart,
		RunOptions:    options.runtime.options,
	})
}

func queueReconcileLister(repo queueRepository, activeOnly bool) runqueue.PlanLister {
	if !activeOnly {
		return repo
	}
	return activeOnlyQueueLister{repo: repo}
}

type activeOnlyQueueLister struct {
	repo queueRepository
}

func (l activeOnlyQueueLister) ListPlans(ctx context.Context, filter plan.PlanFilter) ([]plan.PlanSummary, error) {
	filter.ActiveOnly = true
	return l.repo.ListPlans(ctx, filter)
}

func queueRunValidator(repo queueRepository) runqueue.Validator {
	return func(ctx context.Context, request run.Request) error {
		detail, err := repo.ResolvePlan(ctx, request.Input)
		if err != nil {
			return err
		}
		if detail == nil {
			return fmt.Errorf("plan %s not found", request.Input)
		}
		if detail.State.Plan.ID != "" {
			request.Input = detail.State.Plan.ID
		}
		return run.CheckRequestCanStart(detail, request)
	}
}

func resolveQueuePlanID(ctx context.Context, repo queueRepository, input string) string {
	if repo == nil {
		return input
	}
	detail, err := repo.ResolvePlan(ctx, input)
	if err == nil && detail != nil && detail.State.Plan.ID != "" {
		return detail.State.Plan.ID
	}
	return input
}

func (a App) queueDrainSummary(ctx context.Context, repo queueRepository, snapshot runqueue.QueueSnapshot) runqueue.BatchSummary {
	summaries, err := repo.ListPlans(ctx, plan.PlanFilter{})
	if err != nil {
		a.queueWarningf("could not load plan summaries for reviewed count: %v", err)
		return runqueue.Summarize(snapshot, nil)
	}
	return runqueue.Summarize(snapshot, runqueue.ReviewedPlanLookup(summaries))
}

func (a App) queueNotifyRunner() commandrunner.Runner {
	if a.CommandRunner != nil {
		return a.CommandRunner
	}
	return commandrunner.DefaultLocal
}

func (a App) queueWarningf(format string, args ...any) {
	out := a.Err
	if out == nil {
		out = io.Discard
	}
	_ = writef(out, "warning: "+format+"\n", args...)
}

const (
	queueStatusRunningGroup   = "Running"
	queueStatusQueuedGroup    = "Queued"
	queueStatusFailedGroup    = "Failed"
	queueStatusSucceededGroup = "Recently Succeeded"

	queueStatusFailureWindow = 24 * time.Hour
	queueStatusSuccessWindow = time.Hour
)

type queueStatusView struct {
	Groups  []queueStatusGroup
	Summary runqueue.BatchSummary
	Visible int
	Hidden  int
}

type queueStatusGroup struct {
	Name string
	Rows []queueStatusRow
}

type queueStatusRow struct {
	Entry   runqueue.QueueEntry
	Label   string
	Age     string
	Elapsed string
	Details string
}

// buildQueueStatusView derives display-only queue state. It intentionally works
// from value copies so filtering can never prune or reorder the durable snapshot.
func buildQueueStatusView(snapshot runqueue.QueueSnapshot, summaries []plan.PlanSummary, now time.Time, all bool) queueStatusView {
	visibleEntries := make([]runqueue.QueueEntry, 0, len(snapshot.Entries))
	for _, entry := range snapshot.Entries {
		if all || queueStatusEntryVisible(entry, now) {
			visibleEntries = append(visibleEntries, entry)
		}
	}

	labels := queueStatusPlanLabels(visibleEntries, summaries)
	groups := []queueStatusGroup{
		{Name: queueStatusRunningGroup},
		{Name: queueStatusQueuedGroup},
		{Name: queueStatusFailedGroup},
		{Name: queueStatusSucceededGroup},
	}
	for _, entry := range visibleEntries {
		group := queueStatusGroupIndex(entry.Status)
		groups[group].Rows = append(groups[group].Rows, queueStatusRow{
			Entry:   entry,
			Label:   labels[entry.PlanID],
			Age:     queueStatusEntryAge(entry, now),
			Elapsed: queueStatusEntryElapsed(entry, now),
			Details: queueEntryDetails(entry),
		})
	}

	nonempty := groups[:0]
	for _, group := range groups {
		if len(group.Rows) > 0 {
			nonempty = append(nonempty, group)
		}
	}
	visibleSnapshot := runqueue.QueueSnapshot{Entries: visibleEntries}
	return queueStatusView{
		Groups:  nonempty,
		Summary: runqueue.Summarize(visibleSnapshot, runqueue.ReviewedPlanLookup(summaries)),
		Visible: len(visibleEntries),
		Hidden:  len(snapshot.Entries) - len(visibleEntries),
	}
}

func queueStatusEntryVisible(entry runqueue.QueueEntry, now time.Time) bool {
	switch entry.Status {
	case runqueue.QueueStatusPending, runqueue.QueueStatusRunning:
		return true
	case runqueue.QueueStatusSucceeded:
		return queueStatusFinishedWithin(entry.FinishedAt, now, queueStatusSuccessWindow)
	case runqueue.QueueStatusFailed, runqueue.QueueStatusSkipped:
		return queueStatusFinishedWithin(entry.FinishedAt, now, queueStatusFailureWindow)
	default:
		return queueStatusFinishedWithin(entry.FinishedAt, now, queueStatusFailureWindow)
	}
}

func queueStatusFinishedWithin(finished *time.Time, now time.Time, window time.Duration) bool {
	if finished == nil || finished.IsZero() {
		return true
	}
	return now.Sub(*finished) <= window
}

func queueStatusGroupIndex(status runqueue.QueueStatus) int {
	switch status {
	case runqueue.QueueStatusRunning:
		return 0
	case runqueue.QueueStatusPending:
		return 1
	case runqueue.QueueStatusSucceeded:
		return 3
	default:
		return 2
	}
}

func queueStatusPlanLabels(entries []runqueue.QueueEntry, summaries []plan.PlanSummary) map[string]string {
	byID := make(map[string]plan.PlanSummary, len(summaries))
	for _, summary := range summaries {
		byID[summary.ID] = summary
	}

	candidates := make(map[string]string, len(entries))
	candidateIDs := make(map[string]map[string]bool, len(entries))
	for _, entry := range entries {
		if _, seen := candidates[entry.PlanID]; seen {
			continue
		}
		label := queueStatusPlanLabel(entry.PlanID, byID)
		candidates[entry.PlanID] = label
		if candidateIDs[label] == nil {
			candidateIDs[label] = make(map[string]bool)
		}
		candidateIDs[label][entry.PlanID] = true
	}

	labels := make(map[string]string, len(candidates))
	for id, candidate := range candidates {
		if len(candidateIDs[candidate]) > 1 {
			labels[id] = id
		} else {
			labels[id] = candidate
		}
	}
	return labels
}

func queueStatusPlanLabel(planID string, summaries map[string]plan.PlanSummary) string {
	if summary, ok := summaries[planID]; ok {
		return listPlanLabel(summary)
	}
	if slug, ok := plan.PlanSlug(planID); ok {
		return slug
	}
	return planID
}

func queueStatusEntryAge(entry runqueue.QueueEntry, now time.Time) string {
	switch entry.Status {
	case runqueue.QueueStatusPending:
		return formatQueueStatusAge(entry.QueuedAt, now)
	case runqueue.QueueStatusRunning:
		return formatQueueStatusAgePointer(entry.StartedAt, now)
	default:
		return formatQueueStatusAgePointer(entry.FinishedAt, now)
	}
}

func formatQueueStatusAgePointer(value *time.Time, now time.Time) string {
	if value == nil {
		return "-"
	}
	return formatQueueStatusAge(*value, now)
}

func formatQueueStatusAge(value time.Time, now time.Time) string {
	if value.IsZero() {
		return "-"
	}
	age := now.Sub(value)
	if age <= 0 {
		return "now"
	}
	if age < time.Minute {
		return "<1m"
	}
	if age < time.Hour {
		return fmt.Sprintf("%dm", int(age/time.Minute))
	}
	if age < 24*time.Hour {
		return fmt.Sprintf("%dh", int(age/time.Hour))
	}
	return fmt.Sprintf("%dd", int(age/(24*time.Hour)))
}

func queueStatusEntryElapsed(entry runqueue.QueueEntry, now time.Time) string {
	if entry.StartedAt == nil || entry.StartedAt.IsZero() {
		return "-"
	}
	var finished time.Time
	if entry.Status == runqueue.QueueStatusRunning {
		finished = now
	} else {
		if entry.FinishedAt == nil || entry.FinishedAt.IsZero() {
			return "-"
		}
		finished = *entry.FinishedAt
	}
	duration := finished.Sub(*entry.StartedAt)
	if duration <= 0 {
		return "0s"
	}
	return plan.FormatDuration(duration)
}

func renderQueueStatus(out io.Writer, view queueStatusView, useColor bool) error {
	if err := writef(out, "Summary: %s\n", formatQueueStatusSummary(view)); err != nil {
		return err
	}
	if view.Hidden > 0 {
		if err := writef(out, "%s hidden; use `tao queue status --all` to show complete history.\n", queueHiddenResultCount(view.Hidden)); err != nil {
			return err
		}
	}
	if len(view.Groups) == 0 {
		if view.Hidden > 0 {
			return writeln(out, "No recent queue activity.")
		}
		return writeln(out, "No queued runs.")
	}

	rows := make([][]string, 0, view.Visible)
	for _, group := range view.Groups {
		for _, row := range group.Rows {
			rows = append(rows, []string{string(row.Entry.Status), row.Label})
		}
	}
	widths := planview.ColumnWidths(nil, rows)
	for _, group := range view.Groups {
		if err := writef(out, "\n%s (%d)\n", group.Name, len(group.Rows)); err != nil {
			return err
		}
		for _, row := range group.Rows {
			status := planview.Pad(string(row.Entry.Status), widths[0])
			if useColor {
				status = colorQueueStatus(status, row.Entry.Status)
			}
			if err := writef(out, "  %s  %s  %s%s\n", status, planview.Pad(row.Label, widths[1]), queueStatusActivity(row), queueStatusDetail(row)); err != nil {
				return err
			}
		}
	}
	return nil
}

func formatQueueStatusSummary(view queueStatusView) string {
	if view.Visible == 0 {
		return "0 visible"
	}
	statuses := view.Summary.Statuses
	parts := make([]string, 0, 5)
	if statuses.Running > 0 {
		parts = append(parts, queueStatusCount(statuses.Running, "running"))
	}
	if statuses.Pending > 0 {
		parts = append(parts, queueStatusCount(statuses.Pending, "queued"))
	}
	if statuses.Failed > 0 {
		parts = append(parts, queueStatusCount(statuses.Failed, "failed"))
	}
	if statuses.Skipped > 0 {
		parts = append(parts, queueStatusCount(statuses.Skipped, "skipped"))
	}
	if statuses.Succeeded > 0 {
		succeeded := queueStatusCount(statuses.Succeeded, "succeeded")
		succeeded += fmt.Sprintf(" (%d reviewed)", view.Summary.SucceededReviewed)
		parts = append(parts, succeeded)
	}
	known := statuses.Pending + statuses.Running + statuses.Succeeded + statuses.Failed + statuses.Skipped
	if unknown := view.Visible - known; unknown > 0 {
		parts = append(parts, queueStatusCount(unknown, "unknown"))
	}
	return fmt.Sprintf("%d visible (%s)", view.Visible, strings.Join(parts, ", "))
}

func queueStatusCount(count int, label string) string {
	return fmt.Sprintf("%d %s", count, label)
}

func queueHiddenResultCount(count int) string {
	if count == 1 {
		return "1 older result"
	}
	return fmt.Sprintf("%d older results", count)
}

func queueStatusActivity(row queueStatusRow) string {
	var activity string
	switch row.Entry.Status {
	case runqueue.QueueStatusPending:
		activity = queueStatusRelativeTime("queued", row.Age)
	case runqueue.QueueStatusRunning:
		activity = queueStatusRelativeTime("started", row.Age)
	default:
		activity = queueStatusRelativeTime("finished", row.Age)
	}
	if row.Elapsed != "-" && row.Entry.Status != runqueue.QueueStatusPending {
		activity += ", " + row.Elapsed + " elapsed"
	}
	return activity
}

func queueStatusRelativeTime(action, age string) string {
	switch age {
	case "-":
		return action + " time unknown"
	case "now":
		return action + " now"
	default:
		return action + " " + age + " ago"
	}
}

func queueStatusDetail(row queueStatusRow) string {
	if row.Details == "-" {
		return ""
	}
	label := "detail"
	switch {
	case row.Entry.Error != "":
		label = "error"
	case row.Entry.WaitReason != "":
		label = "waiting"
	case row.Entry.SkipReason != "":
		label = "reason"
	}
	return "  " + label + ": " + row.Details
}

func renderQueueDrainFailures(out io.Writer, snapshot runqueue.QueueSnapshot) error {
	for _, entry := range snapshot.Entries {
		if entry.Status != runqueue.QueueStatusFailed || entry.Error == "" {
			continue
		}
		if err := writef(out, "Failed %s:\n%s\n", entry.PlanID, entry.Error); err != nil {
			return err
		}
	}
	return nil
}

func formatBatchSummary(summary runqueue.BatchSummary) string {
	parts := []string{
		fmt.Sprintf("%d succeeded (%d reviewed)", summary.Statuses.Succeeded, summary.SucceededReviewed),
		fmt.Sprintf("%d failed", summary.Statuses.Failed),
		fmt.Sprintf("%d running", summary.Statuses.Running),
		fmt.Sprintf("%d pending", summary.Statuses.Pending),
	}
	if summary.Statuses.Skipped > 0 {
		parts = append(parts, fmt.Sprintf("%d skipped", summary.Statuses.Skipped))
	}
	return fmt.Sprintf("%s of %d", strings.Join(parts, ", "), summary.Total)
}

func queueEntryDetails(entry runqueue.QueueEntry) string {
	switch {
	case entry.Error != "":
		return entry.Error
	case entry.SkipReason != "":
		return entry.SkipReason
	case entry.WaitReason != "":
		return entry.WaitReason
	default:
		return "-"
	}
}
