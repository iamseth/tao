package run

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/iamseth/tao/internal/commandrunner"
	"github.com/iamseth/tao/internal/gitops"
	"github.com/iamseth/tao/internal/plan"
)

// PreviewFinalVerificationCorrection holds run ownership while refreshing durable
// metadata and observing the execution worktree. It never executes the gate.
func PreviewFinalVerificationCorrection(ctx context.Context, record *plan.PlanRecord, request plan.FinalVerificationCorrectionRequest, runner CommandRunner) (*plan.PlanDetail, plan.FinalVerificationCorrectionPreview, error) {
	var detail *plan.PlanDetail
	var preview plan.FinalVerificationCorrectionPreview
	err := withCorrectionWorkspace(ctx, record, request, runner, func(fresh *plan.PlanDetail) error {
		detail = fresh
		var err error
		preview, err = plan.PreviewFinalVerificationCorrection(fresh, request)
		return err
	})
	return detail, preview, err
}

// CorrectFinalVerification authorizes only the exact previewed transition. Git
// checks are passive; the record owns mutation-lock compare-and-set and replay.
func CorrectFinalVerification(ctx context.Context, record *plan.PlanRecord, request plan.FinalVerificationCorrectionRequest, runner CommandRunner) error {
	return withCorrectionWorkspace(ctx, record, request, runner, func(detail *plan.PlanDetail) error {
		if request.Confirmation == "" {
			return fmt.Errorf("confirmation required; request a new preview")
		}
		if err := correctionWorkspace(ctx, detail, request, runner); err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := record.CorrectFinalVerification(request); err != nil {
			return fmt.Errorf("%w; request a new preview", err)
		}
		return nil
	})
}

func withCorrectionWorkspace(ctx context.Context, record *plan.PlanRecord, request plan.FinalVerificationCorrectionRequest, runner CommandRunner, operation func(*plan.PlanDetail) error) error {
	if record == nil || record.Detail() == nil {
		return fmt.Errorf("correction requires plan record")
	}
	return plan.WithRunLock(ctx, record.Detail(), request.At, func(ctx context.Context) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		fresh, err := plan.NewFileRepository(filepath.Dir(record.Dir())).ResolvePlan(ctx, record.Dir())
		if err != nil {
			return err
		}
		if fresh.State.Plan.ID != record.Detail().State.Plan.ID {
			return fmt.Errorf("plan identity changed; request a new preview")
		}
		if err := correctionWorkspace(ctx, fresh, request, runner); err != nil {
			return err
		}
		return operation(fresh)
	})
}

func correctionWorkspace(ctx context.Context, detail *plan.PlanDetail, request plan.FinalVerificationCorrectionRequest, runner CommandRunner) error {
	w := detail.State.Workspace
	if w == nil || !filepath.IsAbs(w.Path) || w.Branch == "" || w.HeadSHA == "" || w.HeadSHA != request.ExpectedHead {
		return fmt.Errorf("workspace identity missing or stale; request a new preview")
	}
	root, err := filepath.EvalSymlinks(w.Path)
	if err != nil {
		return fmt.Errorf("execution workspace unavailable: %w", err)
	}
	git := gitops.NewReadOnlyClient(root, runner)
	top, err := git.RevParse(ctx, "--show-toplevel")
	if err != nil {
		return err
	}
	top, err = filepath.EvalSymlinks(strings.TrimSpace(top))
	if err != nil || top != root {
		return fmt.Errorf("execution workspace identity changed; request a new preview")
	}
	branch, err := correctionBranch(ctx, root, runner)
	if err != nil {
		return err
	}
	if branch != w.Branch {
		return fmt.Errorf("execution branch changed; request a new preview")
	}
	head, err := git.RevParse(ctx, "HEAD")
	if err != nil {
		return err
	}
	if head != request.ExpectedHead {
		return fmt.Errorf("execution HEAD changed; request a new preview")
	}
	status, err := git.StatusPorcelainAllUntracked(ctx)
	if err != nil {
		return err
	}
	if strings.TrimSpace(status) != "" {
		return fmt.Errorf("correction requires a clean execution worktree; preserve unfinished work")
	}
	return ctx.Err()
}

// Branch probing uses symbolic-ref, never branch mutation or external helpers.
func correctionBranch(ctx context.Context, root string, runner CommandRunner) (string, error) {
	if runner == nil {
		runner = commandrunner.DefaultLocal
	}
	output := &correctionOutput{}
	args := []string{"-C", root, "--no-optional-locks", "-c", "diff.autoRefreshIndex=false", "-c", "core.fsmonitor=false", "-c", "core.quotePath=false", "--literal-pathspecs", "symbolic-ref", "--short", "HEAD"}
	if err := runner(ctx, "", "git", args, output, io.Discard); err != nil {
		return "", err
	}
	return strings.TrimSpace(output.String()), nil
}

type correctionOutput struct{ bytes.Buffer }

func (b *correctionOutput) Write(p []byte) (int, error) {
	if b.Len()+len(p) > 4096 {
		return 0, fmt.Errorf("branch output exceeds bound")
	}
	return b.Buffer.Write(p)
}
