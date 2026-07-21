package run

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/iamseth/tao/internal/gitops"
	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/internal/view"
)

type reloadDetailFunc func(ctx context.Context, detail *plan.PlanDetail) (*plan.PlanDetail, error)

type detailExecutor struct {
	reload        reloadDetailFunc
	out           io.Writer
	execution     runExecution
	sliceExecutor SliceExecutor
	finalizer     Finalizer
	runs          int
	continued     bool
}

func executeDetail(ctx context.Context, detail *plan.PlanDetail, reload reloadDetailFunc, out io.Writer, options Options) error {
	return executeDetailWithExecution(ctx, detail, reload, out, runExecutionFromOptions(options))
}

func executeDetailWithExecution(ctx context.Context, detail *plan.PlanDetail, reload reloadDetailFunc, out io.Writer, execution runExecution) error {
	if out == nil {
		out = execution.Dependencies.OutputWriter
	}
	if execution.Dependencies.OutputWriter == nil {
		execution.Dependencies.OutputWriter = out
	}
	resolveExecutorDefaults(&execution)
	executor := detailExecutor{
		reload:        reload,
		out:           out,
		execution:     execution,
		sliceExecutor: execution.Dependencies.SliceExecutor,
		finalizer:     newFinalizer(out, execution),
	}
	return executor.execute(ctx, detail)
}

func (e *detailExecutor) execute(ctx context.Context, detail *plan.PlanDetail) error {
	for {
		if err := e.continueBlocked(detail); err != nil {
			return err
		}
		derived := plan.Derive(detail, time.Time{})
		capabilities := derived.Capabilities
		complete, err := e.finalizer.FinalizeIfComplete(ctx, e.runs, detail, capabilities)
		if complete || err != nil {
			return err
		}
		stopped, err := e.stopIfMaxSlicesReached(derived)
		if stopped || err != nil {
			return err
		}

		if !capabilities.CanRun {
			return runDisabledError(capabilities)
		}
		reloaded, err := e.selectedSliceRunner().Run(ctx, detail, derived)
		if err != nil {
			return err
		}
		e.execution.ExecutionBoundary = nil
		detail = reloaded
	}
}

func (e *detailExecutor) continueBlocked(detail *plan.PlanDetail) error {
	if !e.execution.Config.Continue || e.continued {
		return nil
	}
	if err := continueBlockedPlan(e.execution, detail, now(e.execution).UTC()); err != nil {
		return err
	}
	e.continued = true
	return nil
}

func (e *detailExecutor) stopIfMaxSlicesReached(derived plan.DerivedPlan) (bool, error) {
	if e.execution.Config.MaxSlices <= 0 || e.runs < e.execution.Config.MaxSlices {
		return false, nil
	}
	return true, writef(e.out, "Stopped after %d slice(s); next pending slice: %s\n", e.runs, nextSliceLabel(derived.NextSliceID))
}

func (e *detailExecutor) selectedSliceRunner() SelectedSliceRunner {
	return SelectedSliceRunner{
		reload:            e.reload,
		out:               e.out,
		execution:         e.execution,
		sliceExecutor:     e.sliceExecutor,
		rootResolver:      executionRootResolver(e.execution),
		boundaryAction:    e.execution.ExecutionBoundary,
		incrementRunCount: func() { e.runs++ },
	}
}

type SelectedSliceRunner struct {
	reload            reloadDetailFunc
	out               io.Writer
	execution         runExecution
	sliceExecutor     SliceExecutor
	rootResolver      ExecutionRootResolver
	boundaryAction    *ExecutionBoundaryAction
	incrementRunCount func()
}

func (r SelectedSliceRunner) Run(ctx context.Context, detail *plan.PlanDetail, derived plan.DerivedPlan) (*plan.PlanDetail, error) {
	slice := derived.NextSlice
	action, err := r.selectedBoundaryAction(slice.ID)
	if err != nil {
		return nil, err
	}
	if !action.AllowAgentHandoff {
		return nil, interruptedSliceRunError(slice.ID, action)
	}
	before := plan.SnapshotProgress(detail)
	logPath := plan.LogPath(detail.Dir)
	validation, err := r.validateSelectedSlice(detail, slice.ID, validationExecutionRoot(r.execution.ExecutionRoot))
	if err != nil {
		return nil, err
	}
	executionRoot, err := r.executionRoot(ctx, detail)
	if err != nil {
		return nil, err
	}
	executionRoot = absoluteExecutionRoot(executionRoot)

	resuming := false
	switch action.EffectiveDisposition {
	case InterruptedSliceNewStart:
		if action.RepairRequirement != ExecutionBoundaryRepairNone {
			return nil, fmt.Errorf("slice %s new-start action has incompatible repair requirement %q", slice.ID, action.RepairRequirement)
		}
		if err := preflightAutomaticSliceStart(ctx, r.execution, executionRoot); err != nil {
			return nil, err
		}
		if err := startSlice(ctx, r.execution, detail, slice.ID, now(r.execution).UTC(), executionRoot); err != nil {
			return nil, fmt.Errorf("mark slice %s started: %w", slice.ID, err)
		}
	case InterruptedSliceCleanStartRepair:
		if action.RepairRequirement != ExecutionBoundaryRepairSliceStart {
			return nil, fmt.Errorf("slice %s clean-start action does not authorize slice-start repair", slice.ID)
		}
		confirmed, inspectErr := r.reinspectRecoveryAction(ctx, detail, action, InterruptedSliceCleanStartRepair)
		if inspectErr != nil {
			return nil, inspectErr
		}
		if err := repairAutomaticSliceStart(r.execution, detail, slice.ID, confirmed); err != nil {
			return nil, fmt.Errorf("repair slice %s execution boundary: %w", slice.ID, err)
		}
		resuming = true
	case InterruptedSliceResume:
		if action.RepairRequirement != ExecutionBoundaryRepairNone {
			return nil, fmt.Errorf("slice %s resume action has incompatible repair requirement %q", slice.ID, action.RepairRequirement)
		}
		if err := repairMissingSliceStartedEvent(r.execution, detail, slice.ID); err != nil {
			return nil, fmt.Errorf("repair slice %s start event: %w", slice.ID, err)
		}
		resuming = true
	default:
		return nil, interruptedSliceRunError(slice.ID, action)
	}

	if resuming {
		action, err = r.reinspectRecoveryAction(ctx, detail, action, InterruptedSliceResume)
		if err != nil {
			return nil, err
		}
	}
	if !action.AllowAgentHandoff {
		return nil, interruptedSliceRunError(slice.ID, action)
	}

	verb := "Running"
	resumeAttempt := 0
	if resuming {
		verb = "Resuming"
		resumeAttempt = nextSliceResumeAttempt(detail.Events, slice.ID)
		preserved := "no current changes"
		if len(action.Diagnostics.Facts.ChangedPaths) > 0 {
			preserved = "preserving " + strings.Join(action.Diagnostics.Facts.ChangedPaths, ", ")
		}
		if err := writef(r.out, "%s slice %s at recorded %s@%s (%s)...\n", verb, slice.ID, action.Diagnostics.Facts.Branch, action.Diagnostics.Facts.Head, preserved); err != nil {
			return nil, err
		}
	} else if err := writef(r.out, "%s slice %s...\n", verb, slice.ID); err != nil {
		return nil, err
	}

	runPacket, err := r.renderRunPacket(detail, executionRoot, resuming, resumeAttempt)
	if err != nil {
		return nil, err
	}
	if err := r.recordRunContext(detail, slice.ID, runPacket, validation); err != nil {
		return nil, err
	}
	if resuming {
		if err := r.recordSliceResumeAttempt(detail, slice.ID, resumeAttempt); err != nil {
			return nil, err
		}
	}
	run := SliceRun{PlanDir: absolutePlanDir(detail.Dir), SliceID: slice.ID, LogPath: logPath, RunPacket: runPacket, RepoRoot: executionRoot, Resuming: resuming, ResumeAttempt: resumeAttempt}
	if err := r.runExecutor(ctx, run); err != nil {
		if resuming {
			r.recordSliceResumeFailure(detail, slice.ID, resumeAttempt, err)
		}
		return nil, fmt.Errorf("%s failed while running slice %s; see %s: %w", agentLabel(r.execution.Config.Agent), slice.ID, logPath, err)
	}
	reloaded, err := r.reload(ctx, detail)
	if err != nil {
		return nil, err
	}
	if err := r.validateSliceProgress(reloaded, before, slice.ID, logPath); err != nil {
		return nil, err
	}
	if err := r.validateAutomaticSliceBoundary(ctx, reloaded, slice.ID, executionRoot); err != nil {
		return nil, err
	}
	r.recordRunCompleted()
	if err := writef(r.out, "Slice completed: %s\n", slice.ID); err != nil {
		return nil, err
	}
	return reloaded, nil
}

func (r SelectedSliceRunner) selectedBoundaryAction(sliceID string) (ExecutionBoundaryAction, error) {
	if r.boundaryAction == nil {
		return ExecutionBoundaryAction{
			Disposition: InterruptedSliceNewStart, EffectiveDisposition: InterruptedSliceNewStart,
			Diagnostics:               ExecutionBoundaryDiagnostics{Facts: InterruptedSliceFacts{SliceID: sliceID}},
			RepairRequirement:         ExecutionBoundaryRepairNone,
			AllowWorkspacePreparation: true, AllowAgentHandoff: true,
		}, nil
	}
	action := *r.boundaryAction
	if action.Diagnostics.Facts.SliceID != sliceID {
		return ExecutionBoundaryAction{}, fmt.Errorf("execution-boundary action is for slice %s, selected slice is %s", action.Diagnostics.Facts.SliceID, sliceID)
	}
	return action, nil
}

func (r SelectedSliceRunner) reinspectRecoveryAction(ctx context.Context, detail *plan.PlanDetail, expected ExecutionBoundaryAction, want InterruptedSliceDisposition) (ExecutionBoundaryAction, error) {
	actual, err := (ExecutionBoundaryController{}).InspectSelected(ctx, ExecutionBoundaryDurableFacts{
		Detail: detail, ContinueBlocked: r.execution.Config.Continue,
	}, r.execution)
	if err != nil {
		return ExecutionBoundaryAction{}, err
	}
	if actual == nil {
		return ExecutionBoundaryAction{}, fmt.Errorf("slice %s recovery context disappeared before agent handoff", expected.Diagnostics.Facts.SliceID)
	}
	if actual.EffectiveDisposition != want {
		return ExecutionBoundaryAction{}, fmt.Errorf("slice %s recovery action changed from %s to %s before agent handoff: %s", expected.Diagnostics.Facts.SliceID, want, actual.EffectiveDisposition, actual.Diagnostics.Reason)
	}
	if !expected.sameLiveBoundary(*actual) {
		return ExecutionBoundaryAction{}, fmt.Errorf("slice %s recovery boundary changed before agent handoff (branch, HEAD, worktree status, or Git operation drifted)", expected.Diagnostics.Facts.SliceID)
	}
	if !actual.AllowAgentHandoff {
		return ExecutionBoundaryAction{}, interruptedSliceRunError(expected.Diagnostics.Facts.SliceID, *actual)
	}
	return *actual, nil
}

func (r SelectedSliceRunner) renderRunPacket(detail *plan.PlanDetail, workingRoot string, resuming bool, resumeAttempt int) (string, error) {
	return plan.RenderRunPacket(detail, plan.RunPacketOptions{
		CommitPolicy:  r.execution.Config.CommitPolicy.String(),
		ExecutionMode: r.execution.Config.ExecutionMode.String(),
		WorkingRoot:   workingRoot,
		Resuming:      resuming,
		ResumeAttempt: resumeAttempt,
	})
}

func nextSliceResumeAttempt(events []plan.Event, sliceID string) int {
	attempt := 1
	for _, event := range events {
		if event.Type == plan.EventTypeSliceResumeAttempted && event.SliceID == sliceID && event.Attempts >= attempt {
			attempt = event.Attempts + 1
		}
	}
	return attempt
}

func (r SelectedSliceRunner) recordSliceResumeAttempt(detail *plan.PlanDetail, sliceID string, attempt int) error {
	event := plan.Event{
		Type:      plan.EventTypeSliceResumeAttempted,
		Timestamp: now(r.execution).UTC(),
		PlanID:    detail.State.Plan.ID,
		SliceID:   sliceID,
		Agent:     auditAgent(r.execution.Config.Agent),
		Attempts:  attempt,
		Message:   fmt.Sprintf("Started interrupted slice resume attempt %d", attempt),
	}
	if err := r.execution.Dependencies.EventAppender.AppendEvent(detail.Dir, event); err != nil {
		return fmt.Errorf("record slice resume attempt before agent handoff: %w", err)
	}
	detail.Events = append(detail.Events, event)
	return nil
}

func (r SelectedSliceRunner) recordSliceResumeFailure(detail *plan.PlanDetail, sliceID string, attempt int, providerErr error) {
	event := plan.Event{
		Type:      plan.EventTypeSliceResumeFailed,
		Timestamp: now(r.execution).UTC(),
		PlanID:    detail.State.Plan.ID,
		SliceID:   sliceID,
		Agent:     auditAgent(r.execution.Config.Agent),
		Attempts:  attempt,
		Reason:    providerErr.Error(),
		Message:   fmt.Sprintf("Interrupted slice resume attempt %d failed", attempt),
	}
	if err := r.execution.Dependencies.EventAppender.AppendEvent(detail.Dir, event); err != nil {
		_ = writef(r.out, "Warning: record slice resume failure: %v\n", err)
		return
	}
	detail.Events = append(detail.Events, event)
}

func (r SelectedSliceRunner) executionRoot(ctx context.Context, detail *plan.PlanDetail) (string, error) {
	resolver := r.rootResolver
	if resolver == nil {
		resolver = executionRootResolver(r.execution)
	}
	return resolver.ResolveExecutionRoot(ctx, detail)
}

func (r SelectedSliceRunner) runExecutor(ctx context.Context, run SliceRun) error {
	executor := r.sliceExecutor
	if executor == nil {
		executor = r.execution.Dependencies.SliceExecutor
	}
	return executor.RunSlice(ctx, run)
}

func (r SelectedSliceRunner) recordRunCompleted() {
	if r.incrementRunCount != nil {
		r.incrementRunCount()
	}
}

func (r SelectedSliceRunner) validateSelectedSlice(detail *plan.PlanDetail, sliceID string, executionRoot string) (plan.VerificationValidationResult, error) {
	validation := plan.ValidateSelectedSliceVerificationAtRoot(detail, executionRoot)
	if len(validation.Findings) > 0 {
		if err := view.RenderVerificationFindings(r.out, validation.Findings); err != nil {
			return validation, err
		}
	}
	if err := view.RenderAgentBudgetWarnings(r.out, plan.AgentBudgetWarnings(detail)); err != nil {
		return validation, err
	}
	if validation.HasErrors() {
		return validation, fmt.Errorf("slice %s failed verification preflight", sliceID)
	}
	return validation, nil
}

func validationExecutionRoot(root string) string {
	if root == "" {
		return ""
	}
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		return ""
	}
	return root
}

func (r SelectedSliceRunner) recordRunContext(detail *plan.PlanDetail, sliceID string, runPacket string, validation plan.VerificationValidationResult) error {
	appender := r.execution.Dependencies.EventAppender
	if appender == nil {
		return nil
	}
	warnings := 0
	for _, finding := range validation.Findings {
		if finding.Severity == plan.VerificationFindingWarning {
			warnings++
		}
	}
	event := plan.Event{
		Type:      plan.EventTypeRunContext,
		Timestamp: now(r.execution).UTC(),
		PlanID:    detail.State.Plan.ID,
		SliceID:   sliceID,
		Agent:     auditAgent(r.execution.Config.Agent),
		// The commit policy is recorded so a later standalone `tao review --run`
		// can gate worktree cleanliness on how the plan actually ran rather than
		// on that invocation's configured default.
		CommitPolicy:      r.execution.Config.CommitPolicy.String(),
		RunPacketProvided: runPacket != "",
		GuardrailWarnings: warnings,
		Message:           "Recorded run context telemetry",
	}
	if err := appender.AppendEvent(detail.Dir, event); err != nil {
		return fmt.Errorf("record run context telemetry: %w", err)
	}
	return nil
}

func (r SelectedSliceRunner) validateSliceProgress(detail *plan.PlanDetail, before plan.ProgressSnapshot, sliceID string, logPath string) error {
	derived := plan.Derive(detail, time.Time{})
	capabilities := derived.Capabilities
	if !capabilities.Complete && !capabilities.CanRun {
		return fmt.Errorf("agent stopped on slice %s; see %s: %w", sliceID, logPath, runDisabledError(capabilities))
	}
	if !plan.ProgressedSince(detail, before) && !capabilities.Complete {
		return fmt.Errorf("agent exited successfully but plan did not progress; see %s", logPath)
	}
	if !capabilities.Complete && !plan.SliceCompleted(detail, sliceID) {
		return fmt.Errorf("agent exited successfully but slice %s was not completed; see %s", sliceID, logPath)
	}
	return nil
}

func startSlice(ctx context.Context, execution runExecution, detail *plan.PlanDetail, sliceID string, timestamp time.Time, executionRoot string) error {
	var boundary *plan.SliceExecutionStart
	if execution.Config.CommitPolicy == CommitPolicySlice {
		git := gitClient(execution, executionRoot)
		branch, err := git.CurrentBranch(ctx)
		if err != nil {
			return fmt.Errorf("capture slice execution branch: %w", err)
		}
		if branch == "" {
			return fmt.Errorf("capture slice execution branch: git branch --show-current returned empty branch")
		}
		head, err := git.RevParse(ctx, "HEAD")
		if err != nil {
			return fmt.Errorf("capture slice execution head: %w", err)
		}
		strategy := plan.WorkspaceStrategyWorktree
		if execution.Config.ExecutionMode == ExecutionModeCurrent {
			strategy = plan.WorkspaceStrategyCurrent
		}
		boundary = &plan.SliceExecutionStart{
			Branch:            branch,
			Head:              head,
			CommitPolicy:      execution.Config.CommitPolicy.String(),
			WorkspaceStrategy: strategy,
		}
	}
	record, err := planMutationRecord(execution, detail)
	if err != nil {
		return err
	}
	commitPolicy := execution.Config.CommitPolicy.String()
	if boundary != nil {
		return record.StartSliceWithRunBoundary(sliceID, executionRoot, commitPolicy, execution.StartingDirtyPaths, *boundary, timestamp)
	}
	return record.StartSliceWithRunCommitPolicy(sliceID, executionRoot, commitPolicy, execution.StartingDirtyPaths, timestamp)
}

func repairMissingSliceStartedEvent(execution runExecution, detail *plan.PlanDetail, sliceID string) error {
	for _, event := range detail.Events {
		if event.Type == plan.EventTypeSliceStarted && event.SliceID == sliceID {
			return nil
		}
	}
	slice := interruptedSlice(detail, sliceID)
	if slice == nil || slice.Timing.StartedAt == nil {
		return fmt.Errorf("recorded slice start time is missing")
	}
	record, err := planMutationRecord(execution, detail)
	if err != nil {
		return err
	}
	return record.RepairMissingSliceStartedEvent(sliceID, slice.Timing.StartedAt.UTC())
}

func repairAutomaticSliceStart(execution runExecution, detail *plan.PlanDetail, sliceID string, action ExecutionBoundaryAction) error {
	facts := action.Diagnostics.Facts
	boundary := plan.SliceExecutionStart{
		Branch:            facts.Branch,
		Head:              facts.Head,
		CommitPolicy:      facts.CommitPolicy,
		WorkspaceStrategy: facts.WorkspaceStrategy,
	}
	record, err := planMutationRecord(execution, detail)
	if err != nil {
		return err
	}
	startedAt := detail.State.UpdatedAt
	if slice := interruptedSlice(detail, sliceID); slice != nil && slice.Timing.StartedAt != nil {
		startedAt = *slice.Timing.StartedAt
	} else if detail.State.Plan.Timing.LastActivityAt != nil {
		startedAt = *detail.State.Plan.Timing.LastActivityAt
	}
	return record.RepairSliceStartWithRunBoundary(sliceID, facts.RecordedRoot, facts.CommitPolicy, execution.StartingDirtyPaths, boundary, startedAt)
}

func preflightAutomaticSliceStart(ctx context.Context, execution runExecution, executionRoot string) error {
	if execution.Config.CommitPolicy != CommitPolicySlice {
		return nil
	}
	branch, err := gitClient(execution, executionRoot).CurrentBranch(ctx)
	if err != nil {
		return fmt.Errorf("inspect execution branch before automatic slice: %w", err)
	}
	if branch == "" || gitops.ProtectedBranch(branch) {
		return fmt.Errorf("automatic slice refused: unsafe execution branch %q", branch)
	}
	return requireCleanAutomaticSliceStart(ctx, execution, executionRoot)
}

func requireCleanAutomaticSliceStart(ctx context.Context, execution runExecution, executionRoot string) error {
	if execution.Config.CommitPolicy != CommitPolicySlice {
		return nil
	}
	status, err := gitClient(execution, executionRoot).StatusPorcelain(ctx)
	if err != nil {
		return fmt.Errorf("inspect worktree before automatic slice: %w", err)
	}
	if strings.TrimSpace(status) == "" {
		return nil
	}
	guidance := "commit or stash the changes before retrying"
	if execution.Config.ExecutionMode == ExecutionModeCurrent {
		guidance += ", or use commit policy none for explicitly manual ownership"
	}
	return fmt.Errorf("automatic slice requires a clean execution worktree; %s", guidance)
}

func (r SelectedSliceRunner) validateAutomaticSliceBoundary(ctx context.Context, detail *plan.PlanDetail, sliceID string, executionRoot string) error {
	if r.execution.Config.CommitPolicy != CommitPolicySlice {
		return nil
	}
	slice := completionSlice(detail, sliceID)
	if slice == nil || slice.ExecutionStart == nil {
		return fmt.Errorf("slice %s completed without a recorded execution boundary", sliceID)
	}
	if slice.Completion == nil || (slice.Completion.Outcome != plan.SliceCompletionCommitted && slice.Completion.Outcome != plan.SliceCompletionNoChanges) {
		return fmt.Errorf("slice %s completed without a committed or no_changes transaction outcome", sliceID)
	}
	git := gitClient(r.execution, executionRoot)
	branch, err := git.CurrentBranch(ctx)
	if err != nil {
		return fmt.Errorf("inspect completed slice branch: %w", err)
	}
	if branch != slice.ExecutionStart.Branch {
		return fmt.Errorf("slice %s changed execution branch from %q to %q", sliceID, slice.ExecutionStart.Branch, branch)
	}
	head, err := git.RevParse(ctx, "HEAD")
	if err != nil {
		return fmt.Errorf("inspect completed slice head: %w", err)
	}
	if head != slice.Completion.CommitSHA {
		return fmt.Errorf("slice %s execution branch advanced unexpectedly: HEAD is %s, transaction recorded %s", sliceID, head, slice.Completion.CommitSHA)
	}
	status, err := git.StatusPorcelain(ctx)
	if err != nil {
		return fmt.Errorf("inspect completed slice worktree: %w", err)
	}
	if strings.TrimSpace(status) != "" {
		return fmt.Errorf("slice %s transaction completed with leftover worktree changes", sliceID)
	}
	return nil
}

func absoluteExecutionRoot(root string) string {
	if root == "" {
		return ""
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return root
	}
	return abs
}

func continueBlockedPlan(execution runExecution, detail *plan.PlanDetail, timestamp time.Time) error {
	record, err := planMutationRecord(execution, detail)
	if err != nil {
		return err
	}
	return record.ContinueBlocked(timestamp)
}

func planMutationRecord(execution runExecution, detail *plan.PlanDetail) (PlanMutationRecord, error) {
	if execution.Dependencies.PlanRecordFactory != nil {
		record, err := execution.Dependencies.PlanRecordFactory(detail)
		if err != nil {
			return nil, err
		}
		if record == nil {
			return nil, fmt.Errorf("plan record is nil")
		}
		return record, nil
	}
	record, err := plan.NewPlanRecord("", detail)
	if err != nil {
		return nil, err
	}
	return record, nil
}
