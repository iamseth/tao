package gitops

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"strings"
)

// Worktree describes one entry from git worktree list --porcelain.
type Worktree struct {
	Path   string
	Branch string
	HEAD   string
}

// WorktreeStatus describes branch, HEAD, and dirty state for a worktree path.
type WorktreeStatus struct {
	Branch string
	HEAD   string
	Dirty  bool
}

// AddWorktree adds a git worktree. If createBranch is true, branch is created from baseBranch.
func (c Client) AddWorktree(ctx context.Context, path string, branch string, baseBranch string, createBranch bool) error {
	if createBranch {
		return c.run(ctx, "worktree", "add", "-b", branch, path, baseBranch)
	}
	return c.run(ctx, "worktree", "add", path, branch)
}

// RemoveWorktree removes a git worktree. If force is true, Git's --force flag is added.
func (c Client) RemoveWorktree(ctx context.Context, path string, force bool) error {
	args := []string{"worktree", "remove"}
	if force {
		args = append(args, "--force")
	}
	args = append(args, path)
	return c.run(ctx, args...)
}

// RebaseWorktree rebases exactly upstream..HEAD in the branch checked out at
// path onto onto. Git must stop rather than silently omit an equivalent or
// newly-empty commit.
func (c Client) RebaseWorktree(ctx context.Context, path, onto, upstream string) error {
	return c.runAt(ctx, path,
		"-c", "commit.gpgSign=false",
		"rebase", "--no-autostash", "--no-update-refs", "--reapply-cherry-picks", "--empty=stop",
		"--onto", onto, upstream,
	)
}

// RebaseAbortWorktree aborts an in-progress rebase in the worktree at path.
func (c Client) RebaseAbortWorktree(ctx context.Context, path string) error {
	return c.runAt(ctx, path, "rebase", "--abort")
}

// WorktreeStatus returns branch, HEAD, and dirty state for a worktree path.
func (c Client) WorktreeStatus(ctx context.Context, path string) (WorktreeStatus, error) {
	branch, err := c.outputAt(ctx, path, "branch", "--show-current")
	if err != nil {
		return WorktreeStatus{}, err
	}
	head, err := c.outputAt(ctx, path, "rev-parse", "HEAD")
	if err != nil {
		return WorktreeStatus{}, err
	}
	porcelain, err := c.outputAt(ctx, path, "status", "--porcelain")
	if err != nil {
		return WorktreeStatus{}, err
	}
	return WorktreeStatus{Branch: branch, HEAD: head, Dirty: porcelain != ""}, nil
}

// MainWorktreeRoot resolves repository identity, not the launch or execution root.
// Git lists the main worktree first; -z preserves paths without C-style quoting
// or whitespace loss. The entry is resolved through show-toplevel because
// submodule entries can name their Git directory instead of their checkout.
// A bare repository has no main checkout, so its Git directory is never used
// as an identity root. If discovery fails or the main
// entry is unusable, show-toplevel falls back to the current checkout (and fails
// in a bare repository without a checkout). GIT_DIR/GIT_WORK_TREE overrides are
// honored by Git: this helper does not isolate discovery from those overrides.
// It deliberately does not infer a checkout from the common Git directory.
func (c Client) MainWorktreeRoot(ctx context.Context) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	out, err := c.rawOutput(ctx, "worktree", "list", "--porcelain", "-z")
	if ctxErr := ctx.Err(); ctxErr != nil {
		return "", ctxErr
	}
	if err == nil {
		entry, _, _ := strings.Cut(out, "\x00\x00")
		var root string
		bare := false
		for field := range strings.SplitSeq(entry, "\x00") {
			if path, ok := strings.CutPrefix(field, "worktree "); ok {
				root = path
			}
			if field == "bare" {
				bare = true
			}
		}
		if root != "" && !bare {
			checkout, resolveErr := c.rawOutputAt(ctx, root, "rev-parse", "--show-toplevel")
			if ctxErr := ctx.Err(); ctxErr != nil {
				return "", ctxErr
			}
			if resolveErr == nil && strings.TrimSuffix(checkout, "\n") != "" {
				return strings.TrimSuffix(checkout, "\n"), nil
			}
		}
	}
	out, err = c.rawOutput(ctx, "rev-parse", "--show-toplevel")
	if ctxErr := ctx.Err(); ctxErr != nil {
		return "", ctxErr
	}
	if err != nil {
		return "", err
	}
	root := strings.TrimSuffix(out, "\n")
	if root == "" {
		return "", fmt.Errorf("git show-toplevel returned no checkout")
	}
	return root, nil
}

// Worktrees returns the repository's worktrees parsed from git worktree porcelain output.
func (c Client) Worktrees(ctx context.Context) ([]Worktree, error) {
	out, err := c.output(ctx, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, err
	}
	return ParseWorktreePorcelain(out), nil
}

// WorktreeForBranch returns the unique worktree holding branch, if any. Git
// itself enforces branch uniqueness, but reporting ambiguity avoids guessing if
// repository metadata is corrupt.
func (c Client) WorktreeForBranch(ctx context.Context, branch string) (Worktree, bool, error) {
	worktrees, err := c.Worktrees(ctx)
	if err != nil {
		return Worktree{}, false, err
	}
	var found Worktree
	for _, worktree := range worktrees {
		if worktree.Branch != branch {
			continue
		}
		if found.Path != "" && filepath.Clean(found.Path) != filepath.Clean(worktree.Path) {
			return Worktree{}, false, fmt.Errorf("branch %q is checked out in multiple worktrees", branch)
		}
		found = worktree
	}
	return found, found.Path != "", nil
}

// ParseWorktreePorcelain parses git worktree list --porcelain output.
func ParseWorktreePorcelain(output string) []Worktree {
	var result []Worktree
	var current Worktree
	for line := range strings.SplitSeq(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			if current.Path != "" {
				result = append(result, current)
			}
			current = Worktree{}
			continue
		}
		key, value, _ := strings.Cut(line, " ")
		switch key {
		case "worktree":
			current.Path = value
		case "HEAD":
			current.HEAD = value
		case "branch":
			current.Branch = strings.TrimPrefix(value, "refs/heads/")
		}
	}
	if current.Path != "" {
		result = append(result, current)
	}
	return result
}

func (c Client) outputAt(ctx context.Context, repoPath string, args ...string) (string, error) {
	out, err := c.rawOutputAt(ctx, repoPath, args...)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

func (c Client) rawOutputAt(ctx context.Context, repoPath string, args ...string) (string, error) {
	var stdout strings.Builder
	var stderr strings.Builder
	if err := c.gitAt(ctx, repoPath, args, &stdout, &stderr); err != nil {
		return "", commandError(args, err, stderr.String())
	}
	return stdout.String(), nil
}

func (c Client) runAt(ctx context.Context, repoPath string, args ...string) error {
	var stdout strings.Builder
	var stderr strings.Builder
	if err := c.gitAt(ctx, repoPath, args, &stdout, &stderr); err != nil {
		return commandError(args, err, stderr.String())
	}
	return nil
}

func (c Client) gitAt(ctx context.Context, repoPath string, args []string, stdout io.Writer, stderr io.Writer) error {
	gitArgs := append([]string{"-C", repoPath}, args...)
	return c.runner(ctx, "", "git", gitArgs, stdout, stderr)
}
