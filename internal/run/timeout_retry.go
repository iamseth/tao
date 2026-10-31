package run

import (
	"context"
	"fmt"
	"path"
	"reflect"
	"slices"
	"strings"
	"sync/atomic"

	"github.com/iamseth/tao/internal/plan"
)

type timeoutAllowanceKey struct{}

// Owned by the invocation context, not the service, slice, or durable journal.
func withTimeoutAllowance(ctx context.Context) context.Context {
	if ctx.Value(timeoutAllowanceKey{}) != nil {
		return ctx
	}
	return context.WithValue(ctx, timeoutAllowanceKey{}, &atomic.Bool{})
}

func timeoutAllowance(ctx context.Context) *atomic.Bool {
	allowance, _ := ctx.Value(timeoutAllowanceKey{}).(*atomic.Bool)
	return allowance
}

func (r SelectedSliceRunner) preflightTimeoutResume(ctx context.Context, detail *plan.PlanDetail, original plan.Slice, originalBase, executionRoot string) (plan.VerificationValidationResult, ExecutionBoundaryAction, error) {
	refuse := func(err error) (plan.VerificationValidationResult, ExecutionBoundaryAction, error) {
		return plan.VerificationValidationResult{}, ExecutionBoundaryAction{}, err
	}
	if err := ctx.Err(); err != nil {
		return refuse(err)
	}
	if allowance := timeoutAllowance(ctx); allowance == nil || allowance.Load() {
		return refuse(fmt.Errorf("invocation timeout continuation allowance exhausted"))
	}
	if err := CheckRequestCanStart(detail, Request{}); err != nil {
		return refuse(err)
	}
	if detail.State.Status != plan.StatusInProgress {
		return refuse(fmt.Errorf("timeout continuation requires an in-progress plan"))
	}
	selected := interruptedSlice(detail, original.ID)
	if selected == nil || selected.BlockerNote != "" || !reflect.DeepEqual(selected.Verification, original.Verification) || !reflect.DeepEqual(selected.ExecutionStart, original.ExecutionStart) {
		return refuse(fmt.Errorf("selected slice declarations or execution boundary changed after timeout"))
	}
	// Continue is explicit operator authority for entry only, never for an automatic retry.
	r.execution.Config.Continue = false
	validation, action, err := r.preflightTransportResume(ctx, detail, original.ID, executionRoot)
	if err != nil {
		return validation, action, err
	}
	if !timeoutStatusUnambiguous(action.live.PorcelainStatus) {
		return refuse(fmt.Errorf("timeout continuation status is ambiguous"))
	}
	base := plan.PlanOwnershipBase(detail)
	if base == "" || base != originalBase {
		return refuse(fmt.Errorf("timeout continuation requires a recorded ownership base"))
	}
	owned, err := gitClient(r.execution, executionRoot).ChangedFilesExact(ctx, base+"..HEAD")
	if err != nil {
		return refuse(fmt.Errorf("determine timeout continuation ownership: %w", err))
	}
	if !timeoutPathsInScope(action.Diagnostics.Facts.ChangedPaths, selected.ExpectedFiles, owned) {
		return refuse(fmt.Errorf("timeout continuation changes are outside verified slice ownership"))
	}
	action, err = r.reinspectRecoveryAction(ctx, detail, action, InterruptedSliceResume)
	if err != nil {
		return validation, action, err
	}
	if err := ctx.Err(); err != nil {
		return refuse(err)
	}
	return validation, action, nil
}

func timeoutStatusUnambiguous(status string) bool {
	for line := range strings.SplitSeq(status, "\n") {
		if line == "" {
			continue
		}
		if len(line) < 4 || line[2] != ' ' || !strings.ContainsRune(" MADT?", rune(line[0])) || !strings.ContainsRune(" MADT?", rune(line[1])) || line[3:] != strings.TrimSpace(line[3:]) {
			return false
		}
	}
	return true
}

func timeoutSliceSnapshot(selected *plan.Slice) plan.Slice {
	if selected == nil {
		return plan.Slice{}
	}
	snapshot := *selected
	snapshot.Verification.Commands = slices.Clone(selected.Verification.Commands)
	snapshot.Verification.Steps = slices.Clone(selected.Verification.Steps)
	snapshot.Verification.ManualChecks = slices.Clone(selected.Verification.ManualChecks)
	if selected.ExecutionStart != nil {
		boundary := *selected.ExecutionStart
		snapshot.ExecutionStart = &boundary
	}
	return snapshot
}

func timeoutPathsInScope(changed, expected, owned []string) bool {
	valid := func(p string) bool {
		return p != "" && p != "." && p != ".." && !strings.HasPrefix(p, "../") && !strings.HasPrefix(p, "/") && !strings.ContainsAny(p, "\\:") && path.Clean(p) == p && !strings.ContainsFunc(p, func(r rune) bool { return r < ' ' || r == 127 })
	}
	for _, p := range owned {
		if !valid(p) {
			return false
		}
	}
	for _, p := range changed {
		if !valid(p) {
			return false
		}
		if slices.Contains(owned, p) {
			continue
		}
		if !slices.ContainsFunc(expected, func(e string) bool { return plan.PathsOverlap(p, e) }) {
			return false
		}
	}
	return true
}
