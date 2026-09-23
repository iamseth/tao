package commit

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/iamseth/tao/internal/gitops"
)

const (
	proposalFileLimit = 32 * 1024
	sha256HexLength   = sha256.Size * 2
)

// ErrNoAllowedChanges reports a safe no-op rather than authorizing an empty
// standalone commit.
var ErrNoAllowedChanges = errors.New("nothing to commit: no allowed changes")

// StandaloneProposal is the bounded untrusted handoff from an active agent.
// ContextFingerprint binds the proposal to an exact prior preflight.
type StandaloneProposal struct {
	ContextFingerprint string `json:"context_fingerprint"`
	Proposal
}

// StandaloneGit is the complete Git boundary for drift-safe standalone commit
// finalization.
type StandaloneGit interface {
	ContextGit
	Add(context.Context, ...string) error
	RestoreStaged(context.Context, ...string) error
	HasStagedChanges(context.Context) (bool, error)
	Commit(context.Context, string) error
}

// StandaloneOptions leaves publication disabled unless explicitly requested.
type StandaloneOptions struct {
	Push bool
}

type StandaloneResult struct {
	Result
	Pushed *StandalonePush
}

// StandalonePush records the branch and destination checked before mutation.
type StandalonePush struct {
	Branch      string                     `json:"branch"`
	Destination gitops.TrackingDestination `json:"destination"`
}

type standalonePushGit interface {
	CurrentBranch(context.Context) (string, error)
	TrackingDestination(context.Context, string) (gitops.TrackingDestination, error)
	PushExact(context.Context, gitops.TrackingDestination, string) error
}

// PreflightStandalonePush is read-only and shared by context and finalization.
func PreflightStandalonePush(ctx context.Context, git ContextGit) (StandalonePush, error) {
	publisher, ok := git.(standalonePushGit)
	if !ok {
		return StandalonePush{}, errors.New("standalone push requires publication-capable Git")
	}
	branch, err := publisher.CurrentBranch(ctx)
	if err != nil {
		return StandalonePush{}, fmt.Errorf("resolve standalone push branch: %w", err)
	}
	if branch == "" {
		return StandalonePush{}, errors.New("standalone push requires an attached branch; HEAD is detached")
	}
	destination, err := publisher.TrackingDestination(ctx, branch)
	if err != nil {
		return StandalonePush{}, fmt.Errorf("preflight standalone push: %w", err)
	}
	return StandalonePush{Branch: branch, Destination: destination}, nil
}

// StandalonePushError reports partial success without discarding commit identity.
type StandalonePushError struct {
	Commit Result
	Target StandalonePush
	Err    error
}

func (e *StandalonePushError) Error() string {
	return fmt.Sprintf("local commit %s remains; push to %s did not complete: %v; inspect the local branch and upstream, resolve the failure, then manually push this exact commit to that destination without force (do not rerun commit)", e.Commit.SHA, e.Target.Destination, e.Err)
}

func (e *StandalonePushError) Unwrap() error { return e.Err }

// ReadStandaloneProposal reads exactly one bounded JSON proposal object.
func ReadStandaloneProposal(path string) (StandaloneProposal, error) {
	file, err := os.Open(path) // #nosec G304 -- explicit proposal path selected by the caller.
	if err != nil {
		return StandaloneProposal{}, fmt.Errorf("open standalone commit proposal: %w", err)
	}
	defer func() { _ = file.Close() }()
	contents, err := io.ReadAll(io.LimitReader(file, proposalFileLimit+1))
	if err != nil {
		return StandaloneProposal{}, fmt.Errorf("read standalone commit proposal: %w", err)
	}
	if len(contents) > proposalFileLimit {
		return StandaloneProposal{}, fmt.Errorf("standalone commit proposal exceeds %d bytes", proposalFileLimit)
	}
	decoder := json.NewDecoder(bytes.NewReader(contents))
	decoder.DisallowUnknownFields()
	var proposal StandaloneProposal
	if err := decoder.Decode(&proposal); err != nil {
		return StandaloneProposal{}, fmt.Errorf("decode standalone commit proposal: %w", err)
	}
	if err := requireJSONEOF(decoder); err != nil {
		return StandaloneProposal{}, err
	}
	if !validContextFingerprint(proposal.ContextFingerprint) {
		return StandaloneProposal{}, errors.New("standalone commit proposal requires a 64-character lowercase hexadecimal context_fingerprint")
	}
	if err := ValidateProposal(proposal.Proposal); err != nil {
		return StandaloneProposal{}, fmt.Errorf("validate standalone commit proposal: %w", err)
	}
	return proposal, nil
}

func validContextFingerprint(fingerprint string) bool {
	if len(fingerprint) != sha256HexLength || fingerprint != strings.ToLower(fingerprint) {
		return false
	}
	_, err := hex.DecodeString(fingerprint)
	return err == nil
}

func requireJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("decode standalone commit proposal: multiple JSON values")
		}
		return fmt.Errorf("decode standalone commit proposal: %w", err)
	}
	return nil
}

// FinalizeStandaloneProposal rechecks every preflight identity component before
// staging, then centrally formats and creates the exact prepared commit.
func FinalizeStandaloneProposal(ctx context.Context, git StandaloneGit, repoRoot string, proposal StandaloneProposal) (Result, error) {
	result, err := FinalizeStandaloneProposalWithOptions(ctx, git, repoRoot, proposal, StandaloneOptions{})
	return result.Result, err
}

func FinalizeStandaloneProposalWithOptions(ctx context.Context, git StandaloneGit, repoRoot string, proposal StandaloneProposal, options StandaloneOptions) (StandaloneResult, error) {
	if err := ValidateProposal(proposal.Proposal); err != nil {
		return StandaloneResult{}, fmt.Errorf("validate standalone commit proposal: %w", err)
	}
	message, err := Format(proposal.Proposal)
	if err != nil {
		return StandaloneResult{}, fmt.Errorf("format standalone commit proposal: %w", err)
	}
	return finalizeStandalone(ctx, git, repoRoot, proposal.ContextFingerprint, message, options)
}

// FinalizeStandaloneMessage supports the explicit full-message compatibility
// override. It has no preflight handoff, but still uses one live safety snapshot
// and the same central staging and prepared-commit authority.
func FinalizeStandaloneMessage(ctx context.Context, git StandaloneGit, repoRoot, message string) (Result, error) {
	result, err := FinalizeStandaloneMessageWithOptions(ctx, git, repoRoot, message, StandaloneOptions{})
	return result.Result, err
}

func FinalizeStandaloneMessageWithOptions(ctx context.Context, git StandaloneGit, repoRoot, message string, options StandaloneOptions) (StandaloneResult, error) {
	if err := validateStandaloneOverride(message); err != nil {
		return StandaloneResult{}, fmt.Errorf("validate standalone commit message: %w", err)
	}
	return finalizeStandalone(ctx, git, repoRoot, "", message, options)
}

func validateStandaloneOverride(message string) error {
	if err := ValidateMessage(message); err != nil {
		return err
	}
	for line := range strings.SplitSeq(message, "\n") {
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(line)), "tao-") {
			return errors.New("standalone message must not supply reserved Tao-* trailers")
		}
	}
	return nil
}

func finalizeStandalone(ctx context.Context, git StandaloneGit, repoRoot, expectedFingerprint, message string, options StandaloneOptions) (StandaloneResult, error) {
	if git == nil {
		return StandaloneResult{}, errors.New("standalone commit finalization requires Git")
	}
	var target StandalonePush
	if options.Push {
		var err error
		target, err = PreflightStandalonePush(ctx, git)
		if err != nil {
			return StandaloneResult{}, err
		}
	}
	live, err := BuildStandaloneContext(ctx, git, repoRoot)
	if err != nil {
		return StandaloneResult{}, err
	}
	if expectedFingerprint != "" && expectedFingerprint != live.Fingerprint {
		return StandaloneResult{}, fmt.Errorf("standalone commit context is stale: expected %s, live %s", expectedFingerprint, live.Fingerprint)
	}
	var stagedRejected []string
	for _, rejected := range live.RejectedPaths {
		if rejected.Reason == "ambiguous git status entry" {
			return StandaloneResult{}, fmt.Errorf("standalone commit cannot safely stage ambiguous status entry %q", rejected.Path)
		}
		if rejected.Staged {
			stagedRejected = append(stagedRejected, rejected.Path)
		}
	}
	if len(stagedRejected) > 0 {
		if err := git.RestoreStaged(ctx, stagedRejected...); err != nil {
			return StandaloneResult{}, fmt.Errorf("unstage rejected standalone commit paths: %w", err)
		}
	}
	if len(live.AllowedPaths) == 0 {
		return StandaloneResult{}, ErrNoAllowedChanges
	}
	if err := git.Add(ctx, live.AllowedPaths...); err != nil {
		return StandaloneResult{}, fmt.Errorf("stage standalone commit paths: %w", err)
	}
	committed, err := CommitPrepared(ctx, git, message)
	result := StandaloneResult{Result: committed}
	if err != nil || !options.Push {
		return result, err
	}
	if err := publishStandalone(ctx, git, target, committed.SHA, live.Head); err != nil {
		return result, &StandalonePushError{Commit: committed, Target: target, Err: err}
	}
	result.Pushed = &target
	return result, nil
}

func publishStandalone(ctx context.Context, git StandaloneGit, target StandalonePush, sha, previousHead string) error {
	current, err := PreflightStandalonePush(ctx, git)
	if err != nil {
		return err
	}
	if current != target {
		return errors.New("standalone push branch or upstream changed after commit")
	}
	head, err := git.RevParse(ctx, "HEAD")
	if err != nil {
		return fmt.Errorf("recheck standalone push HEAD: %w", err)
	}
	if sha == previousHead || head != sha {
		return errors.New("standalone push HEAD does not match the newly created commit")
	}
	return git.(standalonePushGit).PushExact(ctx, target.Destination, sha)
}
