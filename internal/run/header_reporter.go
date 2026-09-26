package run

import (
	"context"
	"sync"
	"time"

	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/internal/runheader"
	"github.com/iamseth/tao/internal/runstatus"
	planview "github.com/iamseth/tao/internal/view"
)

// HeaderReporter observes live run presentation state. Reporting is strictly
// best-effort: implementations must not use it as lifecycle evidence.
type HeaderReporter interface {
	ReportHeader(runheader.State)
}

type headerInvocation struct {
	mu           sync.Mutex
	planID       string
	reporter     HeaderReporter
	state        runheader.State
	seenSessions map[string]bool
}

type headerInvocationContextKey struct{}

func trackRunHeader(ctx context.Context, reporter HeaderReporter, detail *plan.PlanDetail, config ExecutionConfig, startedAt time.Time, fn func(context.Context) error) error {
	if active := headerFromContext(ctx); active != nil && active.planID == detail.State.Plan.ID {
		active.refresh(detail, config)
		return fn(ctx)
	}
	if reporter == nil {
		return fn(ctx)
	}
	if startedAt.IsZero() {
		startedAt = time.Now()
	}
	active := &headerInvocation{
		planID:       detail.State.Plan.ID,
		reporter:     reporter,
		seenSessions: make(map[string]bool),
	}
	active.state = newHeaderState(detail, config, startedAt.UTC())
	active.seedSessions(detail)
	active.publish()
	return fn(context.WithValue(ctx, headerInvocationContextKey{}, active))
}

func headerFromContext(ctx context.Context) *headerInvocation {
	if ctx == nil {
		return nil
	}
	active, _ := ctx.Value(headerInvocationContextKey{}).(*headerInvocation)
	return active
}

// ReportHeader publishes one best-effort header snapshot. Nil reporters and
// panicking implementations are deliberately ignored so presentation cannot
// change a run outcome.
func ReportHeader(reporter HeaderReporter, state runheader.State) {
	if reporter == nil {
		return
	}
	defer func() { _ = recover() }()
	reporter.ReportHeader(state.Clone())
}

func (h *headerInvocation) publish() {
	if h == nil {
		return
	}
	h.mu.Lock()
	reporter := h.reporter
	state := h.state.Clone()
	h.mu.Unlock()
	ReportHeader(reporter, state)
}

func (h *headerInvocation) update(update func(*runheader.State)) {
	if h == nil {
		return
	}
	h.mu.Lock()
	update(&h.state)
	reporter := h.reporter
	state := h.state.Clone()
	h.mu.Unlock()
	ReportHeader(reporter, state)
}

func (h *headerInvocation) refresh(detail *plan.PlanDetail, config ExecutionConfig) {
	if h == nil || detail == nil {
		return
	}
	h.update(func(state *runheader.State) {
		startedAt := state.StartedAt
		maxReworkAttempts := state.MaxReworkAttempts
		phase := state.Phase
		*state = newHeaderState(detail, config, startedAt)
		state.MaxReworkAttempts = maxReworkAttempts
		state.Phase = phase
	})
}

func refreshHeader(ctx context.Context, detail *plan.PlanDetail, config ExecutionConfig) {
	if active := headerFromContext(ctx); active != nil {
		active.refresh(detail, config)
	}
}

func reportHeaderPhase(ctx context.Context, phase runstatus.Phase, slice *runstatus.SliceDetail) {
	active := headerFromContext(ctx)
	if active == nil {
		return
	}
	active.update(func(state *runheader.State) {
		state.Phase = phase
		state.CurrentSliceID = ""
		state.CurrentSliceTitle = ""
		if slice != nil {
			state.CurrentSliceID = slice.ID
			state.CurrentSliceTitle = slice.Title
		}
	})
}

func newHeaderState(detail *plan.PlanDetail, config ExecutionConfig, startedAt time.Time) runheader.State {
	derived := plan.Derive(detail, time.Time{})
	metrics := plan.SummarizeAgentTelemetry(detail).Totals
	rework := planview.ProjectShowRework(detail.Events)
	slices := make([]runheader.Slice, len(detail.Slices.Slices))
	for i, slice := range detail.Slices.Slices {
		slices[i] = runheader.Slice{ID: slice.ID, Title: slice.Title, Status: slice.Status}
	}
	currentID, currentTitle := derived.CurrentSliceID, ""
	if derived.CurrentSlice != nil {
		currentTitle = derived.CurrentSlice.Title
	}
	agent := agentLabel(config.Agent)
	recurringFile := ""
	if len(rework.RecurringFiles) > 0 {
		recurringFile = rework.RecurringFiles[0]
	}
	return runheader.State{
		RepoName:             detail.State.Repo.Name,
		PlanID:               detail.State.Plan.ID,
		PlanTitle:            detail.State.Plan.Title,
		Agent:                agent,
		ExecutionMode:        config.ExecutionMode.String(),
		Branch:               headerBranch(detail),
		ReviewEnabled:        config.ReviewEnabled,
		ReworkRound:          rework.Rounds,
		MaxReworkAttempts:    config.MaxReworkAttempts,
		RecurringFindingFile: recurringFile,
		Slices:               slices,
		CompletedCount:       derived.CompletedCount,
		TotalCount:           derived.TotalCount,
		Phase:                PhaseWaitingForOwnership,
		CurrentSliceID:       currentID,
		CurrentSliceTitle:    currentTitle,
		StartedAt:            startedAt,
		AgentSessionCount:    metrics.Sessions,
		TotalTokens:          metrics.TotalTokens,
		Cost:                 metrics.Cost,
		CostReported:         true,
	}
}

func headerBranch(detail *plan.PlanDetail) string {
	if detail.State.Workspace != nil && detail.State.Workspace.Branch != "" {
		return detail.State.Workspace.Branch
	}
	return detail.State.Repo.Branch
}

func (h *headerInvocation) seedSessions(detail *plan.PlanDetail) {
	for _, event := range plan.AgentMetricsEvents(detail.Events) {
		if event.Metrics.SessionID != "" {
			h.seenSessions[event.Metrics.SessionID] = true
		}
	}
}
