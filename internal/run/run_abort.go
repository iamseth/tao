package run

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"time"

	"github.com/iamseth/tao/internal/agent/logrecord"
	"github.com/iamseth/tao/internal/plan"
)

func classifyRunAbort(err error) string {
	var leak ControlCheckoutLeakError
	var leakPointer *ControlCheckoutLeakError
	if errors.As(err, &leak) || errors.As(err, &leakPointer) {
		return plan.RunAbortKindControlCheckoutLeak
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return plan.RunAbortKindCanceled
	}
	return plan.RunAbortKindOther
}

func (s Service) journalRunAbort(ctx context.Context, detail *plan.PlanDetail, err error) {
	if err == nil || errors.Is(err, ErrCannotStart) || errors.Is(err, plan.ErrRunLocked) {
		return
	}
	planDir := detail.Dir
	event := plan.Event{
		Type:      plan.EventTypeRunAborted,
		Timestamp: now(s.dependencies).UTC(),
		PlanID:    filepath.Base(planDir),
		AbortKind: classifyRunAbort(err),
		Message:   plan.BoundRunAbortMessage(err.Error()),
	}
	// Reuse the entry snapshot: recovery-loading fresh optional metadata can wait
	// indefinitely on .mutation.lock. This context is diagnostic, not authority.
	if detail.State.Plan.ID != "" {
		event.PlanID = detail.State.Plan.ID
	}
	if detail.State.Plan.CurrentSlice != nil {
		event.SliceID = *detail.State.Plan.CurrentSlice
	}
	if workspace := detail.State.Workspace; workspace != nil && workspace.Path != "" && s.dependencies.CommandRunner != nil {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 250*time.Millisecond)
		defer cancel()
		var head strings.Builder
		if commandErr := s.dependencies.CommandRunner(ctx, workspace.Path, "git", []string{"rev-parse", "HEAD"}, &head, io.Discard); commandErr == nil {
			event.HeadSHA = strings.TrimSpace(head.String())
		}
	}
	_ = s.repo.AppendEvent(planDir, event)
	log, logErr := s.repo.OpenLogAppend(planDir)
	if logErr != nil || log == nil {
		return
	}
	defer func() { _ = log.Close() }()
	_ = logrecord.Write(log, logrecord.Record{Type: logrecord.TypeDiagnostic, Content: "tao run exited (" + event.AbortKind + "): " + event.Message})
}
