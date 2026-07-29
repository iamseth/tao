package run

import (
	"context"
	"time"

	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/internal/runstatus"
	"github.com/iamseth/tao/internal/taodata"
)

const maxRunStatusLabelRunes = 32

// StatusReporter is the optional run-boundary reporter. Implementations must be
// best-effort: reporting failures must not fail or delay the wrapped run.
type StatusReporter interface {
	Track(status string, fn func() error) error
}

// StatusInvocation is the stable identity and start time for one top-level run
// ownership boundary. It is operational context only, never lifecycle evidence.
type StatusInvocation struct {
	RepoID    string
	RepoName  string
	PlanID    string
	PlanTitle string
	StartedAt time.Time
}

// PhaseReporter receives observable orchestration transitions. Implementations
// must suppress publication failures so reporting cannot change run outcomes.
type PhaseReporter interface {
	ReportPhase(phase runstatus.Phase, slice *runstatus.SliceDetail)
}

// InvocationStatusReporter optionally extends the legacy Track seam with live
// phase and heartbeat support. Implementations begin the invocation in the
// waiting-for-ownership phase before calling fn. Keeping it separate preserves
// alternate and test reporters that implement only StatusReporter.
type InvocationStatusReporter interface {
	TrackInvocation(status string, invocation StatusInvocation, fn func(PhaseReporter) error) error
}

const (
	PhaseWaitingForOwnership runstatus.Phase = "waiting_for_ownership"
	PhasePreparingExecution  runstatus.Phase = "preparing_execution"
	PhaseRunningSlice        runstatus.Phase = "running_slice"
	PhaseFinalVerification   runstatus.Phase = "final_verification"
	PhaseReview              runstatus.Phase = "review"
	PhaseAutomaticRework     runstatus.Phase = "automatic_rework"
)

type statusInvocationContext struct {
	planID    string
	publisher PhaseReporter
}

type statusInvocationContextKey struct{}

func trackRunStatus(ctx context.Context, reporter StatusReporter, detail *plan.PlanDetail, startedAt time.Time, fn func(context.Context) error) error {
	planID := detail.State.Plan.ID
	if active, ok := ctx.Value(statusInvocationContextKey{}).(statusInvocationContext); ok && active.planID == planID {
		return fn(ctx)
	}
	status := runStatusLabel(planID)
	invocation := statusInvocation(detail, startedAt)
	if extended, ok := reporter.(InvocationStatusReporter); ok {
		return extended.TrackInvocation(status, invocation, func(publisher PhaseReporter) error {
			statusCtx := context.WithValue(ctx, statusInvocationContextKey{}, statusInvocationContext{planID: planID, publisher: publisher})
			return fn(statusCtx)
		})
	}
	statusCtx := context.WithValue(ctx, statusInvocationContextKey{}, statusInvocationContext{planID: planID})
	if reporter == nil {
		return fn(statusCtx)
	}
	return reporter.Track(status, func() error { return fn(statusCtx) })
}

// ReportPhase publishes a best-effort phase when ctx belongs to an enhanced
// status invocation. It deliberately has no return value so status cannot
// authorize recovery or alter the wrapped operation.
func ReportPhase(ctx context.Context, phase runstatus.Phase, slice *runstatus.SliceDetail) {
	if ctx == nil {
		return
	}
	active, _ := ctx.Value(statusInvocationContextKey{}).(statusInvocationContext)
	if active.publisher != nil {
		active.publisher.ReportPhase(phase, slice)
	}
}

func statusInvocation(detail *plan.PlanDetail, startedAt time.Time) StatusInvocation {
	if startedAt.IsZero() {
		startedAt = time.Now()
	}
	root := detail.State.Repo.Root
	return StatusInvocation{
		RepoID:    taodata.RepoID(root),
		RepoName:  detail.State.Repo.Name,
		PlanID:    detail.State.Plan.ID,
		PlanTitle: detail.State.Plan.Title,
		StartedAt: startedAt.UTC(),
	}
}

func runStatusLabel(planID string) string {
	name, ok := plan.PlanSlug(planID)
	if !ok {
		name = planID
	}
	return capRunes("run "+name, maxRunStatusLabelRunes)
}

func capRunes(value string, max int) string {
	if max <= 0 {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= max {
		return value
	}
	return string(runes[:max])
}
