package taodata

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/iamseth/tao/internal/gitops"
)

const (
	RepoHealthOK            = "ok"
	RepoHealthMissingRoot   = "missing_root"
	RepoHealthNotGitRepo    = "not_git_repo"
	RepoHealthMetadataError = "metadata_error"
)

// RepoHealth describes the non-destructive health state of a registered repo.
type RepoHealth struct {
	Status  string
	Message string
	Error   bool
}

// RepoHealthChecker classifies repository roots using injectable filesystem and git probes.
type RepoHealthChecker struct {
	Stat      func(string) (os.FileInfo, error)
	GitInside func(context.Context, string) error
}

func (c RepoHealthChecker) Check(ctx context.Context, repo Repo) RepoHealth {
	root := strings.TrimSpace(repo.Root)
	if root == "" {
		return RepoHealth{Status: RepoHealthMissingRoot, Message: "repo root is empty", Error: true}
	}
	stat := c.Stat
	if stat == nil {
		stat = os.Stat
	}
	info, err := stat(root)
	if err != nil {
		if os.IsNotExist(err) {
			return RepoHealth{Status: RepoHealthMissingRoot, Message: "repo root does not exist", Error: true}
		}
		return RepoHealth{Status: RepoHealthMissingRoot, Message: fmt.Sprintf("repo root cannot be read: %v", err), Error: true}
	}
	if !info.IsDir() {
		return RepoHealth{Status: RepoHealthNotGitRepo, Message: "repo root is not a directory", Error: true}
	}
	gitInside := c.GitInside
	if gitInside == nil {
		gitInside = gitInsideWorkTree
	}
	if err := gitInside(ctx, root); err != nil {
		return RepoHealth{Status: RepoHealthNotGitRepo, Message: "repo root is not a Git worktree", Error: true}
	}
	return RepoHealth{Status: RepoHealthOK, Message: "ok", Error: false}
}

func metadataErrorHealth(err error) RepoHealth {
	return RepoHealth{Status: RepoHealthMetadataError, Message: fmt.Sprintf("repo metadata cannot be read: %v", err), Error: true}
}

func gitInsideWorkTree(ctx context.Context, root string) error {
	inside, err := gitops.NewClient(root, nil).InsideWorkTree(ctx)
	if err != nil {
		return err
	}
	if !inside {
		return fmt.Errorf("not a worktree")
	}
	return nil
}
