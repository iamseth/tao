package run

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/iamseth/tao/internal/commandrunner"
	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/internal/plan/verification"
)

const sliceVerificationCommandTimeout = 10 * time.Minute

type SliceVerificationRequest struct {
	ExecutionRoot string
	Verification  plan.Verification
}

// SliceVerifier only executes declarations and returns observed evidence. It
// does not persist lifecycle state, bind an agent session, or authorize commits.
type SliceVerifier struct {
	CommandRunner commandrunner.Runner
	Now           func() time.Time
}

// SliceVerificationError presents bounded local gate diagnostics. Cause is for
// programmatic inspection only; its unbounded/raw text is never printed here.
type SliceVerificationError struct {
	CommandIndex int
	Command      string
	CWD          string
	FailureKind  plan.FinalVerificationFailureKind
	Details      string
	Cause        error
}

func (e *SliceVerificationError) Error() string {
	return fmt.Sprintf("slice verification gate %d (%s) failed: %s in %s\n%s",
		e.CommandIndex, verificationPresentation(string(e.FailureKind), 64),
		verificationPresentation(e.Command, 512), verificationPresentation(e.CWD, 512),
		verificationPresentation(e.Details, plan.MaxVerificationDetailsBytes))
}

func (e *SliceVerificationError) Unwrap() error { return e.Cause }

func (v SliceVerifier) Verify(ctx context.Context, request SliceVerificationRequest) ([]plan.VerificationRun, error) {
	return v.verify(ctx, request, sliceVerificationCommandTimeout)
}

// The duration seam is private and used only by short subprocess tests.
func (v SliceVerifier) verify(ctx context.Context, request SliceVerificationRequest, timeout time.Duration) ([]plan.VerificationRun, error) {
	root, cwds, err := sliceVerificationDirectories(request)
	if err != nil {
		return nil, err
	}
	if v.CommandRunner == nil {
		v.CommandRunner = commandrunner.DefaultLocal
	}
	if v.Now == nil {
		v.Now = time.Now
	}
	var runs []plan.VerificationRun
	for i, command := range request.Verification.Commands {
		if err := ctx.Err(); err != nil {
			return runs, sliceVerificationContextError(i+1, command, cwds[i], err)
		}
		if err := sliceVerificationCheckCWD(i+1, command, cwds[i]); err != nil {
			return runs, err
		}
		run, cause := v.attempt(ctx, command, cwds[i], i+1, timeout)
		runs = append(runs, run)
		if run.Result == "passed" {
			continue
		}
		if run.FailureKind == plan.FinalVerificationFailureKindInvalidCommand && ctx.Err() == nil {
			candidate, ok := verification.MechanicalCorrection(root, verification.Run{Command: command, CWD: run.CWD, Details: run.Details})
			if ok {
				if err := ctx.Err(); err != nil {
					return runs, sliceVerificationContextError(i+1, command, candidate.CWD, err)
				}
				if err := sliceVerificationCheckCWD(i+1, candidate.Command, candidate.CWD); err != nil {
					return runs, err
				}
				run, cause = v.attempt(ctx, candidate.Command, candidate.CWD, i+1, timeout)
				run.OriginalCommand = command
				runs = append(runs, run)
				if run.Result == "passed" {
					continue
				}
			}
		}
		return runs, &SliceVerificationError{CommandIndex: run.CommandIndex, Command: run.Command, CWD: run.CWD, FailureKind: run.FailureKind, Details: run.Details, Cause: cause}
	}
	if err := ctx.Err(); err != nil {
		return runs, sliceVerificationContextError(0, "", root, err)
	}
	return runs, nil
}

func (v SliceVerifier) attempt(ctx context.Context, command, cwd string, index int, timeout time.Duration) (plan.VerificationRun, error) {
	commandCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var stdout, stderr verificationTail
	started := v.Now()
	runErr := v.CommandRunner(commandCtx, cwd, "sh", []string{"-c", command}, &stdout, &stderr)
	duration := max(v.Now().Sub(started).Milliseconds(), 0)
	run := plan.VerificationRun{
		Command: command, CWD: cwd, CommandIndex: index, Source: plan.VerificationSourceTao,
		Result: "failed", DurationMilliseconds: &duration,
		Details:         verificationOutputDetails(&stdout, &stderr),
		OutputDigest:    verificationOutputDigest(&stdout, &stderr),
		OutputTruncated: stdout.truncated || stderr.truncated,
	}
	if runErr == nil && commandCtx.Err() == nil {
		zero := 0
		run.ExitCode = &zero
		run.Result = "passed"
		return run, nil
	}
	run.FailureKind, run.ExitCode = classifyFinalVerificationFailure(commandCtx, runErr)
	// Reuse the existing diagnostic classifier without changing final verification.
	// Never classify presentation-sanitized text or use suggestions as authority.
	if commandCtx.Err() == nil {
		diagnostic := string(stdout.bytes()) + "\n" + string(stderr.bytes())
		classification := verification.ClassifyRun(cwd, verification.Run{Command: command, CWD: cwd, Details: diagnostic})
		switch {
		case errors.Is(runErr, exec.ErrNotFound), errors.Is(runErr, os.ErrNotExist), classification.Code == "verification_command_not_found":
			run.FailureKind = plan.FinalVerificationFailureKindToolMissing
		case classification.Invalid:
			run.FailureKind = plan.FinalVerificationFailureKindInvalidCommand
		}
	}
	cause := runErr
	if commandCtx.Err() != nil {
		cause = commandCtx.Err()
	}
	if cause != nil {
		// Keep launch/cancellation errors actionable even when the process printed nothing.
		suffix := "\nrunner: " + verificationPresentation(cause.Error(), 1024)
		run.Details = verificationPresentation(run.Details, plan.MaxVerificationDetailsBytes-len(suffix)) + suffix
	}
	return run, cause
}

func sliceVerificationContextError(index int, command, cwd string, cause error) error {
	kind := plan.FinalVerificationFailureKindCancelled
	if errors.Is(cause, context.DeadlineExceeded) {
		kind = plan.FinalVerificationFailureKindTimeout
	}
	return &SliceVerificationError{CommandIndex: index, Command: command, CWD: cwd, FailureKind: kind, Details: cause.Error(), Cause: cause}
}

// Steps supply cwd context only. Resolve the entire declaration before launching
// anything; exact duplicate commands share an unambiguous canonical cwd but keep
// their separate one-based indices. These path checks are not a shell sandbox.
func sliceVerificationDirectories(request SliceVerificationRequest) (string, []string, error) {
	invalid := func(index int, command, cwd, detail string) error {
		return &SliceVerificationError{CommandIndex: index, Command: command, CWD: cwd, FailureKind: plan.FinalVerificationFailureKindInvalidCommand, Details: detail}
	}
	root, err := canonicalVerificationDirectory(request.ExecutionRoot)
	if err != nil {
		return "", nil, invalid(0, "", request.ExecutionRoot, "invalid execution root: "+err.Error())
	}
	indices := make(map[string]int)
	for i, command := range request.Verification.Commands {
		if !validSliceVerificationText(command) {
			return "", nil, invalid(i+1, command, root, "command must be non-empty, bounded UTF-8 text without NUL")
		}
		indices[command] = i + 1
	}
	mapped := make(map[string]string)
	for _, step := range request.Verification.Steps {
		index, ok := indices[step.Command]
		if !ok {
			return "", nil, invalid(0, step.Command, step.CWD, "verification.steps may not add commands absent from verification.commands")
		}
		path := step.CWD
		if path == "" {
			path = root
		} else if !filepath.IsAbs(path) {
			path = filepath.Join(root, path)
		}
		cwd, err := canonicalVerificationDirectory(path)
		if err != nil {
			return "", nil, invalid(index, step.Command, path, "invalid step cwd: "+err.Error())
		}
		relative, err := filepath.Rel(root, cwd)
		if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
			return "", nil, invalid(index, step.Command, cwd, "step cwd escapes execution root")
		}
		if prior, ok := mapped[step.Command]; ok && prior != cwd {
			return "", nil, invalid(index, step.Command, cwd, "conflicting step cwd mappings")
		}
		mapped[step.Command] = cwd
	}
	cwds := make([]string, len(request.Verification.Commands))
	for i, command := range request.Verification.Commands {
		cwds[i] = mapped[command]
		if cwds[i] == "" {
			cwds[i] = root
		}
	}
	return root, cwds, nil
}

// An earlier gate may have removed or replaced a later gate's directory. Check
// again at dispatch, without claiming to prevent concurrent filesystem races.
func sliceVerificationCheckCWD(index int, command, cwd string) error {
	canonical, err := canonicalVerificationDirectory(cwd)
	if err != nil || canonical != cwd {
		return &SliceVerificationError{CommandIndex: index, Command: command, CWD: cwd, FailureKind: plan.FinalVerificationFailureKindInvalidCommand, Details: "canonical working directory changed or disappeared; repair the declared cwd before retrying"}
	}
	return nil
}

func canonicalVerificationDirectory(path string) (string, error) {
	if !filepath.IsAbs(path) || !validSliceVerificationText(path) {
		return "", fmt.Errorf("directory must be an absolute, bounded UTF-8 path")
	}
	canonical, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(canonical)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("not a directory")
	}
	if !validSliceVerificationText(canonical) {
		return "", fmt.Errorf("invalid canonical path")
	}
	return canonical, nil
}

func validSliceVerificationText(text string) bool {
	return len(text) <= plan.MaxVerificationCommandBytes && utf8.ValidString(text) && strings.TrimSpace(text) != "" && !strings.ContainsRune(text, 0)
}
