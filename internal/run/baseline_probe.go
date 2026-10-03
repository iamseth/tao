package run

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"strings"

	"github.com/iamseth/tao/internal/commandrunner"
	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/internal/plandelta"
	"github.com/iamseth/tao/internal/verifydetect"
	"github.com/iamseth/tao/internal/verifyoutput"
)

// probeBaselineFailure is best-effort evidence collection, never repair authority.
func (f Finalizer) probeBaselineFailure(ctx context.Context, detail *plan.PlanDetail, executionRoot string, head plan.FinalVerification, combinedOutput string) *plan.FinalVerificationBaseline {
	if detail == nil || head.FailureKind != plan.FinalVerificationFailureKindCode || plan.VerificationRepairAttemptCount(detail) >= plan.VerificationRepairAttemptCap {
		return nil
	}
	tests := verifyoutput.FailingTests(combinedOutput)
	packages := verifyoutput.FailingPackages(combinedOutput)
	paths := verifyoutput.FailingPaths(combinedOutput)
	// Extractors cap each set at 64 entries without reporting truncation. A
	// saturated set cannot prove that every failure is outside plan ownership.
	// Decline even an exactly-full set rather than probe with incomplete scope.
	if len(packages) >= 64 || len(paths) >= 64 {
		return nil
	}
	if len(packages) == 0 && len(paths) == 0 {
		return nil
	}
	detector, ok := verifydetect.OpenRoot(executionRoot)
	if !ok {
		return nil
	}
	var dirs []string
	for _, pkg := range packages {
		dir, ok := detector.GoPackageDir(pkg)
		if !ok {
			return nil
		}
		dirs = append(dirs, dir)
	}
	// Missing ownership evidence is not a successfully computed empty set.
	ownershipBase := plan.PlanOwnershipBase(detail)
	if ownershipBase == "" {
		return nil
	}
	ownedFiles, err := gitClient(f.execution, executionRoot).ChangedFilesExact(ctx, ownershipBase+"..HEAD")
	if err != nil {
		return nil
	}
	for _, file := range ownedFiles {
		if slices.Contains(paths, file) {
			return nil
		}
		for _, dir := range dirs {
			if dir == "." || strings.HasPrefix(file, dir+"/") {
				return nil
			}
		}
	}
	base := plandelta.ResolveBase(ctx, f.execution.Dependencies.reviewGitFactory(executionRoot), detail.State)
	if base.SHA == "" || base.SHA == head.HeadSHA {
		return nil
	}
	var overlap []string
	err = gitClient(f.execution, executionRoot).WithDetachedCheckout(ctx, base.SHA, func(dir string) error {
		var stdout, stderr bytes.Buffer
		runErr := f.execution.Dependencies.CommandRunner(commandrunner.WithVerificationCache(ctx, dir), dir, "sh", []string{"-c", head.Command}, &stdout, &stderr)
		if runErr == nil || errors.Is(runErr, context.DeadlineExceeded) || errors.Is(runErr, context.Canceled) {
			return nil
		}
		kind, _ := classifyFinalVerificationFailure(ctx, runErr)
		if kind != plan.FinalVerificationFailureKindCode {
			return nil
		}
		output := combineFinalVerificationOutput(stdout.String(), stderr.String())
		baseSignatures := append(verifyoutput.FailingTests(output), verifyoutput.FailingPackages(output)...)
		for _, signature := range append(tests, packages...) {
			if slices.Contains(baseSignatures, signature) {
				overlap = append(overlap, signature)
			}
		}
		return nil
	})
	if err != nil || ctx.Err() != nil || len(overlap) == 0 {
		return nil
	}
	slices.Sort(overlap)
	overlap = slices.Compact(overlap)
	if len(overlap) > 64 {
		overlap = overlap[:64]
	}
	return &plan.FinalVerificationBaseline{SHA: base.SHA, Source: string(base.Source), Result: finalVerificationFailed, Signatures: overlap}
}
