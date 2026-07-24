package commit

import (
	"context"
	"fmt"
)

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
	if git == nil {
		return Result{}, fmt.Errorf("prepared commit requires Git")
	}
	if err := ValidateMessage(message); err != nil {
		return Result{}, fmt.Errorf("validate prepared commit message: %w", err)
	}
	staged, err := git.HasStagedChanges(ctx)
	if err != nil {
		return Result{}, fmt.Errorf("inspect prepared commit: %w", err)
	}
	if !staged {
		return Result{}, fmt.Errorf("prepared commit requires staged changes")
	}
	if err := git.Commit(ctx, message); err != nil {
		return Result{}, fmt.Errorf("create prepared commit: %w", err)
	}
	sha, err := git.RevParse(ctx, "HEAD")
	if err != nil {
		return Result{}, fmt.Errorf("resolve prepared commit: %w", err)
	}
	if sha == "" {
		return Result{}, fmt.Errorf("resolve prepared commit: empty HEAD")
	}
	return Result{SHA: sha, Subject: messageSubject(message)}, nil
}
