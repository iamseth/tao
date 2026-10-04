package tui

import (
	"context"
	"errors"
	"fmt"

	"github.com/iamseth/tao/internal/monitor"
	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/internal/term"
	"github.com/iamseth/tao/internal/term/cells"
)

// PlanFixLauncher runs an interactive fix agent synchronously. The caller owns
// terminal handoff; returning conveys no plan lifecycle state.
type PlanFixLauncher interface {
	Launch(context.Context, monitor.Row) error
}

// Called only while readInput is parked on its resume handshake. Refresh, not
// the launcher's return, discovers any plan changes.
func (a App) fixSelectedPlan(ctx context.Context, state *loopState, key term.KeyEvent) (bool, error) {
	if key.Key != term.KeyRune || key.Rune != 'x' || state.showShortcuts || state.searchActive || state.confirm != nil || state.notePicker != nil || state.filterMenu != nil || state.noteDetail != nil {
		return false, nil
	}
	var row monitor.Row
	var ok bool
	if state.detail != nil {
		if state.detail.sliceOpen || state.detail.activeTab == detailTabChanges {
			return false, nil
		}
		row, ok = state.detail.row, true
	} else if state.activePage() == PagePlans {
		row, ok = state.selectedRow()
	}
	if !ok {
		return false, nil
	}
	feedback := a.Actions
	if feedback == nil {
		feedback = &state.planFixFeedback
	}
	feedback.messageKey = actionRowKey(row)
	feedback.message = ""
	if reason, eligible := planFixEligible(row); !eligible {
		feedback.message = reason
		return true, nil
	}
	if a.PlanFixLauncher == nil {
		feedback.message = "Plan fixing is unavailable."
		return true, nil
	}
	launchErr, restoreErr := a.launchPlanFix(ctx, row)
	if restoreErr != nil {
		return true, restoreErr
	}
	if err := enterTerminalState(a.Terminal, a.Output); err != nil {
		return true, fmt.Errorf("resume dashboard after plan fixing: %w", err)
	}
	size, err := a.Terminal.Size()
	if err != nil {
		return true, fmt.Errorf("read terminal size after plan fixing: %w", err)
	}
	state.size = size
	if state.detail != nil {
		state.detail.clampOffsets(size)
	}
	if launchErr != nil {
		feedback.message = "Plan fixing failed: " + cells.Truncate(singleLineDetail(launchErr.Error()), 240)
	}
	return true, nil
}

func planFixEligible(row monitor.Row) (reason string, ok bool) {
	if !actionableRow(row) {
		return "Plan is not actionable.", false
	}
	if row.Liveness == monitor.LivenessLive || row.RunLockPresent {
		return "Plan has a live run; wait for it to exit.", false
	}
	if row.MergeInProgress {
		return "Plan is in a merge batch.", false
	}
	if row.Status != plan.StatusBlocked && row.Status != plan.StatusVerificationFailed && len(row.AttentionReasons) == 0 {
		return "Plan is not stuck; next action: " + row.NextAction, false
	}
	return "", true
}

func (a App) launchPlanFix(ctx context.Context, row monitor.Row) (launchErr, terminalErr error) {
	// Keep an independent baseline so a killed or panicking child cannot supply
	// the next EnterRaw baseline after Restore clears the saved state.
	restoreBaseline := a.Terminal.PreserveBaseline()
	defer func() {
		if err := restoreBaseline(); err != nil {
			terminalErr = errors.Join(terminalErr, fmt.Errorf("restore terminal after plan fixing: %w", err))
		}
	}()
	if err := restoreTerminalState(a.Terminal, a.Output); err != nil {
		return nil, fmt.Errorf("suspend dashboard for plan fixing: %w", err)
	}
	return a.PlanFixLauncher.Launch(ctx, row), nil
}
