package merge

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/iamseth/tao/internal/commandrunner"
	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/internal/textbound"
	"github.com/iamseth/tao/internal/verifydetect"
)

const envMergeVerifyCommand = "TAO_MERGE_VERIFY_COMMAND"

type mergeVerifyCommandSource int

const (
	mergeVerifySourceNoVerify mergeVerifyCommandSource = iota
	mergeVerifySourceOption
	mergeVerifySourceEnv
	mergeVerifySourceDetected
	mergeVerifySourceNoDetection
)

type mergeVerifyCommandInputs struct {
	envCommand       string
	envSet           bool
	detectedCommands []string
}

type mergeVerifyCommandResolution struct {
	command  string
	source   mergeVerifyCommandSource
	repoRoot string
}

func (r mergeVerifyCommandResolution) skippedNoDetection() bool {
	return r.source == mergeVerifySourceNoDetection
}

type mergeVerifySnapshot struct {
	defaultBranch string
	preMergeSHA   string
}

type VerifyFailedError struct {
	Command       string
	RepoRoot      string
	Output        string
	Cause         error
	CleanupErrors []error
}

func (e *VerifyFailedError) Error() string {
	message := ErrVerifyFailed.Error()
	if command := strings.TrimSpace(e.Command); command != "" {
		message += fmt.Sprintf(" for %q", command)
	}
	if output := strings.TrimSpace(e.Output); output != "" {
		firstLine, _, _ := strings.Cut(output, "\n")
		message += ": " + firstLine
	}
	if len(e.CleanupErrors) > 0 {
		parts := make([]string, 0, len(e.CleanupErrors))
		for _, err := range e.CleanupErrors {
			if err != nil {
				parts = append(parts, err.Error())
			}
		}
		if len(parts) > 0 {
			message += "; rollback failed: " + strings.Join(parts, "; ")
		}
	}
	return message
}

func (e *VerifyFailedError) Is(target error) bool {
	return target == ErrVerifyFailed
}

func (e *VerifyFailedError) Unwrap() error {
	return e.Cause
}

func (s Service) Verify(ctx context.Context, detail *plan.PlanDetail, defaultBranch string, preMergeSHA string, options Options) error {
	resolution, err := resolveMergeVerifyCommandForDetail(detail, options)
	if err != nil {
		return err
	}
	if resolution.command == "" {
		s.logMergeVerifySkipped(resolution)
		return nil
	}
	return s.runMergeVerify(ctx, detail, mergeVerifySnapshot{defaultBranch: strings.TrimSpace(defaultBranch), preMergeSHA: strings.TrimSpace(preMergeSHA)}, resolution.command)
}

func captureMergeVerifySnapshot(ctx context.Context, git GitClient, detail *plan.PlanDetail) (mergeVerifySnapshot, error) {
	defaultBranch, err := resolveDefaultBranch(ctx, git, detail)
	if err != nil {
		return mergeVerifySnapshot{}, err
	}
	preMergeSHA, err := git.RevParse(ctx, defaultBranch)
	if err != nil {
		return mergeVerifySnapshot{}, fmt.Errorf("capture pre-merge SHA for %s: %w", defaultBranch, err)
	}
	preMergeSHA = strings.TrimSpace(preMergeSHA)
	if preMergeSHA == "" {
		return mergeVerifySnapshot{}, fmt.Errorf("capture pre-merge SHA for %s: empty revision", defaultBranch)
	}
	return mergeVerifySnapshot{defaultBranch: defaultBranch, preMergeSHA: preMergeSHA}, nil
}

func (s Service) runMergeVerify(ctx context.Context, detail *plan.PlanDetail, snapshot mergeVerifySnapshot, command string) error {
	repoRoot, err := mergeVerifyRepoRoot(detail)
	if err != nil {
		return err
	}
	if snapshot.defaultBranch == "" {
		return fmt.Errorf("merge verification default branch is missing")
	}
	if snapshot.preMergeSHA == "" {
		return fmt.Errorf("merge verification pre-merge SHA is missing")
	}
	git, err := s.gitClient()
	if err != nil {
		return err
	}
	output, runErr := s.runMergeVerifyAtRoot(ctx, repoRoot, command)
	if runErr != nil {
		return s.verifyFailed(ctx, git, snapshot, command, repoRoot, output, runErr)
	}
	return nil
}

const mergeVerifyOutputLimit = 32 * 1024

// runMergeVerifyAtRoot executes verification in an explicit worktree without
// applying single-plan rollback semantics. Batch integration owns its rollback.
func (s Service) runMergeVerifyAtRoot(ctx context.Context, repoRoot, command string) (string, error) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	err := s.commandRunner()(ctx, repoRoot, "sh", []string{"-c", command}, &stdout, &stderr)
	return boundMergeVerifyOutput(combineVerifyOutput(stdout.String(), stderr.String())), err
}

func boundMergeVerifyOutput(output string) string {
	bounded, _ := textbound.Tail(output, mergeVerifyOutputLimit)
	return bounded
}

func (s Service) verifyFailed(ctx context.Context, git GitClient, snapshot mergeVerifySnapshot, command string, repoRoot string, output string, cause error) error {
	cleanupErrs := make([]error, 0)
	if err := git.ResetHard(ctx, snapshot.preMergeSHA); err != nil {
		cleanupErrs = append(cleanupErrs, fmt.Errorf("reset %s to %s: %w", snapshot.defaultBranch, snapshot.preMergeSHA, err))
	}
	if err := git.Checkout(ctx, snapshot.defaultBranch); err != nil {
		cleanupErrs = append(cleanupErrs, fmt.Errorf("re-checkout default branch %s: %w", snapshot.defaultBranch, err))
	}
	return &VerifyFailedError{Command: command, RepoRoot: repoRoot, Output: output, Cause: cause, CleanupErrors: cleanupErrs}
}

func resolveMergeVerifyCommandForDetail(detail *plan.PlanDetail, options Options) (mergeVerifyCommandResolution, error) {
	envCommand, envSet := os.LookupEnv(envMergeVerifyCommand)
	if !mergeVerifyNeedsDetection(options, envSet) {
		return resolveMergeVerifyCommand(options, mergeVerifyCommandInputs{envCommand: envCommand, envSet: envSet}), nil
	}
	repoRoot, err := mergeVerifyRepoRoot(detail)
	if err != nil {
		return mergeVerifyCommandResolution{}, err
	}
	return resolveMergeVerifyCommandAtRoot(repoRoot, options), nil
}

func resolveMergeVerifyCommandAtRoot(repoRoot string, options Options) mergeVerifyCommandResolution {
	envCommand, envSet := os.LookupEnv(envMergeVerifyCommand)
	inputs := mergeVerifyCommandInputs{envCommand: envCommand, envSet: envSet}
	if mergeVerifyNeedsDetection(options, envSet) {
		if command := verifydetect.DetectCommand(repoRoot); command != "" {
			inputs.detectedCommands = []string{command}
		}
	}
	resolution := resolveMergeVerifyCommand(options, inputs)
	resolution.repoRoot = repoRoot
	return resolution
}

func mergeVerifyNeedsDetection(options Options, envSet bool) bool {
	return !options.NoVerify && options.VerifyCommand == "" && !envSet
}

func resolveMergeVerifyCommand(options Options, inputs mergeVerifyCommandInputs) mergeVerifyCommandResolution {
	if options.NoVerify {
		return mergeVerifyCommandResolution{source: mergeVerifySourceNoVerify}
	}
	if options.VerifyCommand != "" {
		return mergeVerifyCommandResolution{command: strings.TrimSpace(options.VerifyCommand), source: mergeVerifySourceOption}
	}
	if inputs.envSet {
		return mergeVerifyCommandResolution{command: strings.TrimSpace(inputs.envCommand), source: mergeVerifySourceEnv}
	}
	command := strings.Join(inputs.detectedCommands, " && ")
	if strings.TrimSpace(command) == "" {
		return mergeVerifyCommandResolution{source: mergeVerifySourceNoDetection}
	}
	return mergeVerifyCommandResolution{command: command, source: mergeVerifySourceDetected}
}

func mergeVerifyRepoRoot(detail *plan.PlanDetail) (string, error) {
	if detail == nil {
		return "", fmt.Errorf("merge plan detail is nil")
	}
	repoRoot := strings.TrimSpace(detail.State.Repo.Root)
	if repoRoot == "" {
		return "", fmt.Errorf("merge verification repo root is missing")
	}
	return repoRoot, nil
}

func (s Service) commandRunner() commandrunner.Runner {
	if s.Runner != nil {
		return s.Runner
	}
	return commandrunner.DefaultLocal
}

func combineVerifyOutput(stdout string, stderr string) string {
	if stdout == "" {
		return stderr
	}
	if stderr == "" {
		return stdout
	}
	if strings.HasSuffix(stdout, "\n") {
		return stdout + stderr
	}
	return stdout + "\n" + stderr
}
