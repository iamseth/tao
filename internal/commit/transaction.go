package commit

import (
	"context"
	"errors"
	"fmt"
)

// ErrNoStagedChanges marks refusal before commit creation because staging is empty.
var ErrNoStagedChanges = errors.New("prepared commit requires staged changes")

// ErrCommitCreated marks a successful commit whose HEAD could not be resolved.
// Callers must not treat this as a commit-creation failure or roll back the commit.
var ErrCommitCreated = errors.New("resolve prepared commit")

// PreparedGit is the narrow Git boundary required to create a prepared commit.
type PreparedGit interface {
	HasStagedChanges(ctx context.Context) (bool, error)
	Commit(ctx context.Context, message string) error
	RevParse(ctx context.Context, rev string) (string, error)
}

// Result identifies a newly created commit.
type Result struct {
	SHA     string
	Subject string
}

// CommitPrepared validates and commits an exact final message. Callers retain
// ownership of path selection and staging policy.
func CommitPrepared(ctx context.Context, git PreparedGit, message string) (Result, error) {
	if err := ValidateMessage(message); err != nil {
		return Result{}, fmt.Errorf("validate prepared commit message: %w", err)
	}
	return CommitStaged(ctx, git, message)
}

// CommitStaged commits an exact message without validating it. Callers retain
// ownership of message policy, path selection, and staging policy.
func CommitStaged(ctx context.Context, git PreparedGit, message string) (Result, error) {
	if git == nil {
		return Result{}, fmt.Errorf("prepared commit requires Git")
	}
	staged, err := git.HasStagedChanges(ctx)
	if err != nil {
		return Result{}, fmt.Errorf("inspect prepared commit: %w", err)
	}
	if !staged {
		return Result{}, fmt.Errorf("%w", ErrNoStagedChanges)
	}
	if err := git.Commit(ctx, message); err != nil {
		return Result{}, fmt.Errorf("create prepared commit: %w", err)
	}
	sha, err := git.RevParse(ctx, "HEAD")
	if err != nil {
		return Result{}, fmt.Errorf("%w: %w", ErrCommitCreated, err)
	}
	if sha == "" {
		return Result{}, fmt.Errorf("%w: empty HEAD", ErrCommitCreated)
	}
	return Result{SHA: sha, Subject: messageSubject(message)}, nil
}
