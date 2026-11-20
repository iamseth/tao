package run

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/iamseth/tao/internal/commandrunner"
	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/internal/verifyoutput"
)

// Contract observations constrain comparison, not arbitrary command effects.
func finalContractCWD(root, relative string) (string, error) {
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", err
	}
	cwd, err := filepath.EvalSymlinks(filepath.Join(root, relative))
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(resolvedRoot, cwd)
	if err != nil || !filepath.IsLocal(rel) {
		return "", fmt.Errorf("contract cwd escapes execution root")
	}
	info, err := os.Stat(cwd)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("contract cwd is not a directory")
	}
	return filepath.Join(root, relative), nil
}

func (f Finalizer) stableFinalContractHead(ctx context.Context, detail *plan.PlanDetail, root, head, digest string) bool {
	if ctx.Err() != nil || detail.State.Plan.FinalVerificationContract == nil || detail.State.Workspace == nil || detail.State.Workspace.HeadSHA != head {
		return false
	}
	current, err := detail.State.Plan.FinalVerificationContract.Digest()
	if err != nil || current != digest {
		return false
	}
	if detail.State.Plan.MergeCommitIntent != nil || detail.State.Plan.PullRequestIntent != nil {
		return false
	}
	for _, slice := range detail.Slices.Slices {
		if slice.CommitIntent != nil && slice.Completion == nil {
			return false
		}
	}
	git := gitClient(f.execution, root)
	branch, err := git.CurrentBranch(ctx)
	if err != nil || strings.TrimSpace(branch) == "" || strings.TrimSpace(branch) != detail.State.Workspace.Branch {
		return false
	}
	status, err := git.StatusPorcelain(ctx)
	if err != nil || strings.TrimSpace(status) != "" {
		return false
	}
	live, err := git.RevParse(ctx, "HEAD")
	return err == nil && strings.TrimSpace(live) == head
}

// runContractGate retains only bounded output, a digest and observed exits. A
// failed observation never becomes regression authority, including after a pass.
func (f Finalizer) runContractGate(ctx context.Context, root, head, pathEnv string, c plan.FinalVerificationContract, pins map[string]string) (plan.FinalVerificationComparisonRun, string, error, error) {
	run := plan.FinalVerificationComparisonRun{HeadSHA: head, CWD: c.CWD}
	cwd, err := finalContractCWD(root, c.CWD)
	if err != nil {
		return run, "", nil, fmt.Errorf("contract cwd: %w", err)
	}
	before, err := observeFinalVerificationPrerequisites(ctx, root, pathEnv, c.Prerequisites)
	if err != nil {
		return run, "", nil, err
	}
	if os.Getenv("PATH") != pathEnv {
		return run, "", nil, ErrFinalPrerequisiteUnstable
	}
	var stdout, stderr bytes.Buffer
	gateCtx := commandrunner.WithGateEnv(commandrunner.WithVerificationCache(ctx, root), pins)
	runErr := f.execution.Dependencies.CommandRunner(gateCtx, cwd, "sh", []string{"-c", c.Command}, &stdout, &stderr)
	output := combineFinalVerificationOutput(stdout.String(), stderr.String())
	sum := sha256.Sum256([]byte(output))
	run.OutputDigest = hex.EncodeToString(sum[:])
	run.Result = finalVerificationFailed
	run.ExitCode = finalVerificationExitCode(runErr)
	if runErr == nil {
		zero := 0
		run.ExitCode = &zero
		run.Result = finalVerificationPassed
	}
	after, err := observeFinalVerificationPrerequisites(ctx, root, pathEnv, c.Prerequisites)
	if err != nil {
		return run, output, runErr, err
	}
	if !slices.Equal(before, after) || os.Getenv("PATH") != pathEnv {
		return run, output, runErr, ErrFinalPrerequisiteUnstable
	}
	run.Observations = after
	return run, output, runErr, nil
}

func (f Finalizer) verifyContractBranch(ctx context.Context, detail *plan.PlanDetail, root string, comparisonAllowed bool) error {
	c := *detail.State.Plan.FinalVerificationContract
	v := plan.FinalVerification{Command: c.Command, CWD: filepath.Join(root, c.CWD), Result: finalVerificationFailed, VerifiedAt: now(f.execution).UTC()}
	digest, contractErr := c.Digest()
	v.ContractDigest = digest
	if detail.State.Workspace != nil {
		v.HeadSHA = detail.State.Workspace.HeadSHA
	}
	if contractErr == nil {
		_, contractErr = finalContractCWD(root, c.CWD)
	}
	if contractErr != nil {
		v.FailureKind = plan.FinalVerificationFailureKindInvalidContract
		return f.finishContractVerification(detail, v, contractErr)
	}
	pins, err := runtimeEnv(f.execution.Config.RuntimeEnv).GateEnv()
	if err != nil {
		return err
	}
	head, err := gitClient(f.execution, root).RevParse(ctx, "HEAD")
	if err != nil {
		return err
	}
	v.HeadSHA = strings.TrimSpace(head)
	if v.HeadSHA == "" {
		return fmt.Errorf("resolve final verification HEAD: empty revision")
	}
	pathEnv := os.Getenv("PATH")
	initiallyStable := f.stableFinalContractHead(ctx, detail, root, v.HeadSHA, digest)
	first, output, runErr, observationErr := f.runContractGate(ctx, root, v.HeadSHA, pathEnv, c, pins)
	v.Details, v.OutputTruncated = boundedFinalVerificationDetails(output)
	v.ExitCode = first.ExitCode
	if observationErr != nil {
		v.FailureKind = plan.FinalVerificationFailureKindUnknown
		if errors.Is(observationErr, ErrFinalPrerequisiteMissing) {
			v.FailureKind = plan.FinalVerificationFailureKindPrerequisiteMissing
		}
		return f.finishContractVerification(detail, v, observationErr)
	}
	if runErr == nil {
		v.Result = finalVerificationPassed
	} else {
		v.FailureKind, v.ExitCode = classifyConservativeFinalVerificationFailure(ctx, runErr)
	}
	comparisonAllowed = comparisonAllowed && f.execution.Config.CommitPolicy == CommitPolicySlice && f.execution.Config.ExecutionMode == ExecutionModeIsolated && plan.AnalyzeRunCapabilities(detail).Complete && initiallyStable && c.ComparisonPermitted && len(c.Prerequisites) > 0 && len(c.PermittedEffects) > 0 &&
		!f.execution.Request.Reverify && !f.execution.Request.RepairVerification && plan.VerificationRepairAttemptCount(detail) < plan.VerificationRepairAttemptCap &&
		f.stableFinalContractHead(ctx, detail, root, v.HeadSHA, digest)
	if runErr != nil && v.FailureKind == plan.FinalVerificationFailureKindUnknown && comparisonAllowed {
		f.compareContractFailure(ctx, detail, root, pathEnv, c, pins, &v, first)
	}
	if v.Comparison != nil {
		observations, err := observeFinalVerificationPrerequisites(ctx, root, pathEnv, c.Prerequisites)
		if err != nil || !slices.Equal(observations, first.Observations) || !f.stableFinalContractHead(ctx, detail, root, v.HeadSHA, digest) {
			v.Comparison = nil
			v.Result = finalVerificationFailed
			v.FailureKind = plan.FinalVerificationFailureKindUnknown
		}
	}
	if v.Result == finalVerificationPassed {
		runErr = nil
	}
	return f.finishContractVerification(detail, v, runErr)
}

func (f Finalizer) compareContractFailure(ctx context.Context, detail *plan.PlanDetail, root, pathEnv string, c plan.FinalVerificationContract, pins map[string]string, v *plan.FinalVerification, first plan.FinalVerificationComparisonRun) {
	base := strings.TrimSpace(detail.State.Repo.BaseCommit)
	if base == "" || base == v.HeadSHA || ctx.Err() != nil {
		return
	}
	git := gitClient(f.execution, root)
	ancestor, err := git.IsAncestor(ctx, base, v.HeadSHA)
	if err != nil || !ancestor {
		return
	}
	retry, _, retryErr, observationErr := f.runContractGate(ctx, root, v.HeadSHA, pathEnv, c, pins)
	if observationErr != nil || !slices.Equal(first.Observations, retry.Observations) || !f.stableFinalContractHead(ctx, detail, root, v.HeadSHA, v.ContractDigest) {
		return
	}
	comparison := &plan.FinalVerificationComparison{ContractDigest: v.ContractDigest, BaseSHA: base, HeadSHA: v.HeadSHA, Runs: []plan.FinalVerificationComparisonRun{first, retry}}
	if retryErr == nil {
		v.Result = finalVerificationPassed
		v.FailureKind = ""
		v.ExitCode = retry.ExitCode
		v.Comparison = comparison // Diagnostic flake evidence never admits repair.
		if appender := f.execution.Dependencies.EventAppender; appender != nil {
			failed := *v
			failed.Result = finalVerificationFailed
			failed.FailureKind = plan.FinalVerificationFailureKindUnknown
			failed.ExitCode = first.ExitCode
			_ = appender.AppendEvent(detail.Dir, plan.Event{Type: "final_verification_flake", Timestamp: now(f.execution).UTC(), PlanID: detail.State.Plan.ID, Command: c.Command, HeadSHA: v.HeadSHA, Fingerprint: finalVerificationFingerprint(failed), ExitCode: first.ExitCode, FailureKind: failed.FailureKind, Reason: verifyoutput.Reason(v.Details, "same-head retry passed", 1000)})
		}
		return
	}
	if first.ExitCode == nil || retry.ExitCode == nil || *first.ExitCode != *retry.ExitCode || first.OutputDigest != retry.OutputDigest {
		return
	}
	var baseRun plan.FinalVerificationComparisonRun
	var baseOutput string
	var baseErr error
	err = git.WithDetachedCheckout(ctx, base, func(dir string) error {
		baseRun, baseOutput, baseErr, observationErr = f.runContractGate(ctx, dir, base, pathEnv, c, pins)
		if observationErr != nil {
			return observationErr
		}
		status, statusErr := gitClient(f.execution, dir).StatusPorcelain(ctx)
		if statusErr != nil {
			return statusErr
		}
		if strings.TrimSpace(status) != "" {
			return fmt.Errorf("base gate changed checkout")
		}
		live, headErr := gitClient(f.execution, dir).RevParse(ctx, "HEAD")
		if headErr != nil {
			return headErr
		}
		if strings.TrimSpace(live) != base {
			return fmt.Errorf("base gate moved HEAD")
		}
		return nil
	})
	if err != nil || !slices.Equal(first.Observations, baseRun.Observations) || !f.stableFinalContractHead(ctx, detail, root, v.HeadSHA, v.ContractDigest) {
		return
	}
	comparison.Runs = append(comparison.Runs, baseRun)
	v.Comparison = comparison
	if baseErr == nil {
		v.FailureKind = plan.FinalVerificationFailureKindCode
		return
	}
	kind, _ := classifyConservativeFinalVerificationFailure(ctx, baseErr)
	if kind != plan.FinalVerificationFailureKindUnknown {
		return
	}
	signatures := append(verifyoutput.FailingTests(v.Details), verifyoutput.FailingPackages(v.Details)...)
	baseline := append(verifyoutput.FailingTests(baseOutput), verifyoutput.FailingPackages(baseOutput)...)
	var overlap []string
	for _, signature := range signatures {
		if slices.Contains(baseline, signature) {
			overlap = append(overlap, signature)
		}
	}
	slices.Sort(overlap)
	overlap = slices.Compact(overlap)
	if len(overlap) > 64 {
		overlap = overlap[:64]
	}
	if len(overlap) > 0 {
		v.FailureKind = plan.FinalVerificationFailureKindBaseline
		v.Baseline = &plan.FinalVerificationBaseline{SHA: base, Source: "recorded_base", Result: finalVerificationFailed, Signatures: overlap}
	}
}

func (f Finalizer) finishContractVerification(detail *plan.PlanDetail, v plan.FinalVerification, cause error) error {
	if cause != nil && v.Details == "" {
		v.Details, v.OutputTruncated = boundedFinalVerificationDetails(cause.Error())
	}
	v.Fingerprint = finalVerificationFingerprint(v)
	if err := f.recordFinalVerification(detail, v); err != nil {
		return err
	}
	f.appendFinalVerificationEvent(detail, v, v.Details, now(f.execution).UTC(), nil)
	if v.Result == finalVerificationPassed {
		return writef(f.outputWriter(), "Final verification: passed (%s)\n", v.Command)
	}
	if err := f.appendVerificationRepairStoppedEvent(detail, v, now(f.execution).UTC()); err != nil {
		return err
	}
	return &FinalVerificationError{Verification: v, Cause: cause}
}
