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
	if err := ValidateProposal(proposal.Proposal); err != nil {
		return Result{}, fmt.Errorf("validate standalone commit proposal: %w", err)
	}
	message, err := Format(proposal.Proposal)
	if err != nil {
		return Result{}, fmt.Errorf("format standalone commit proposal: %w", err)
	}
	return finalizeStandalone(ctx, git, repoRoot, proposal.ContextFingerprint, message)
}

// FinalizeStandaloneMessage supports the explicit full-message compatibility
// override. It has no preflight handoff, but still uses one live safety snapshot
// and the same central staging and prepared-commit authority.
func FinalizeStandaloneMessage(ctx context.Context, git StandaloneGit, repoRoot, message string) (Result, error) {
	if err := validateStandaloneOverride(message); err != nil {
		return Result{}, fmt.Errorf("validate standalone commit message: %w", err)
	}
	return finalizeStandalone(ctx, git, repoRoot, "", message)
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

func finalizeStandalone(ctx context.Context, git StandaloneGit, repoRoot, expectedFingerprint, message string) (Result, error) {
	if git == nil {
		return Result{}, errors.New("standalone commit finalization requires Git")
	}
	live, err := BuildStandaloneContext(ctx, git, repoRoot)
	if err != nil {
		return Result{}, err
	}
	if expectedFingerprint != "" && expectedFingerprint != live.Fingerprint {
		return Result{}, fmt.Errorf("standalone commit context is stale: expected %s, live %s", expectedFingerprint, live.Fingerprint)
	}
	var stagedRejected []string
	for _, rejected := range live.RejectedPaths {
		if rejected.Reason == "ambiguous git status entry" {
			return Result{}, fmt.Errorf("standalone commit cannot safely stage ambiguous status entry %q", rejected.Path)
		}
		if rejected.Staged {
			stagedRejected = append(stagedRejected, rejected.Path)
		}
	}
	if len(stagedRejected) > 0 {
		if err := git.RestoreStaged(ctx, stagedRejected...); err != nil {
			return Result{}, fmt.Errorf("unstage rejected standalone commit paths: %w", err)
		}
	}
	if len(live.AllowedPaths) == 0 {
		return Result{}, ErrNoAllowedChanges
	}
	if err := git.Add(ctx, live.AllowedPaths...); err != nil {
		return Result{}, fmt.Errorf("stage standalone commit paths: %w", err)
	}
	return CommitPrepared(ctx, git, message)
}
