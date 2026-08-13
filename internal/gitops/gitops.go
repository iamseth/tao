// Package gitops provides typed Git command helpers bound to a repository root.
package gitops

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/iamseth/tao/internal/commandrunner"
)

// Runner runs a local command and is compatible with Tao command-runner seams.
type Runner = commandrunner.Runner

// Client executes typed Git operations in a single repository.
type Client struct {
	repoRoot string
	runner   Runner
}

// NewClient returns a Git client bound to repoRoot.
func NewClient(repoRoot string, runner Runner) Client {
	if runner == nil {
		runner = defaultRunner
	}
	return Client{repoRoot: repoRoot, runner: runner}
}

func defaultRunner(ctx context.Context, cwd string, name string, args []string, stdout io.Writer, stderr io.Writer) error {
	return commandrunner.DefaultLocal(ctx, cwd, name, args, stdout, stderr)
}

// Root returns the working-copy directory the client is bound to. An empty
// string means Git operations run with -C "", which Git resolves to the
// process working directory at the time of the call.
func (c Client) Root() string {
	return c.repoRoot
}

// CurrentBranch returns the current branch name.
func (c Client) CurrentBranch(ctx context.Context) (string, error) {
	return c.output(ctx, "branch", "--show-current")
}

// RevParse returns the resolved revision.
func (c Client) RevParse(ctx context.Context, rev string) (string, error) {
	return c.output(ctx, "rev-parse", rev)
}

// UpdateRefCAS updates ref only when it still has oldSHA. An all-zero oldSHA
// requires the ref to be absent.
func (c Client) UpdateRefCAS(ctx context.Context, ref, newSHA, oldSHA string) error {
	return c.run(ctx, "update-ref", ref, newSHA, oldSHA)
}

// CommitMessage returns the complete message for a revision.
func (c Client) CommitMessage(ctx context.Context, rev string) (string, error) {
	return c.rawOutput(ctx, "log", "-1", "--format=%B", rev)
}

// MergeBase returns the merge-base SHA for two revisions.
func (c Client) MergeBase(ctx context.Context, a string, b string) (string, error) {
	return c.output(ctx, "merge-base", a, b)
}

// RemoteURL returns the configured URL for the origin remote.
func (c Client) RemoteURL(ctx context.Context) (string, error) {
	return c.output(ctx, "config", "--get", "remote.origin.url")
}

// InsideWorkTree reports whether the bound repository root is inside a Git work tree.
func (c Client) InsideWorkTree(ctx context.Context) (bool, error) {
	out, err := c.output(ctx, "rev-parse", "--is-inside-work-tree")
	if err != nil {
		return false, err
	}
	return out == "true", nil
}

// DefaultBranch detects the default branch using origin/HEAD, then local main/master.
func (c Client) DefaultBranch(ctx context.Context) (string, error) {
	branch, err := c.output(ctx, "symbolic-ref", "--quiet", "--short", "refs/remotes/origin/HEAD")
	if err == nil {
		if branch = strings.TrimPrefix(strings.TrimSpace(branch), "origin/"); branch != "" {
			return branch, nil
		}
		err = errors.New("origin HEAD is empty")
	}
	originErr := err
	for _, candidate := range []string{"main", "master"} {
		out, err := c.output(ctx, "branch", "--format=%(refname:short)", "--list", candidate)
		if err == nil && strings.TrimSpace(out) == candidate {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("detect default branch: %w", originErr)
}

// StatusPorcelain returns raw porcelain status output.
func (c Client) StatusPorcelain(ctx context.Context) (string, error) {
	return c.rawOutput(ctx, "status", "--porcelain")
}

// StatusPorcelainAllUntracked returns porcelain status with each untracked file
// listed separately instead of collapsing an untracked directory.
func (c Client) StatusPorcelainAllUntracked(ctx context.Context) (string, error) {
	return c.rawOutput(ctx, "status", "--porcelain", "--untracked-files=all")
}

// ActiveOperation reports an in-progress Git operation in root. Git owns the
// operation-marker layout, so callers do not inspect .git internals themselves.
func ActiveOperation(root string) (string, error) {
	gitDir := filepath.Join(root, ".git")
	info, err := os.Stat(gitDir)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	if !info.IsDir() {
		contents, err := os.ReadFile(gitDir) // #nosec G304 -- root is the caller-selected working tree.
		if err != nil {
			return "", err
		}
		const prefix = "gitdir:"
		line := strings.TrimSpace(string(contents))
		if !strings.HasPrefix(line, prefix) {
			return "", fmt.Errorf("unrecognized .git file")
		}
		gitDir = strings.TrimSpace(strings.TrimPrefix(line, prefix))
		if !filepath.IsAbs(gitDir) {
			gitDir = filepath.Join(root, gitDir)
		}
	}
	operations := []struct {
		name string
		path string
	}{
		{name: "rebase", path: "rebase-merge"},
		{name: "rebase", path: "rebase-apply"},
		{name: "merge", path: "MERGE_HEAD"},
		{name: "cherry-pick", path: "CHERRY_PICK_HEAD"},
		{name: "revert", path: "REVERT_HEAD"},
		{name: "cherry-pick/revert", path: "sequencer"},
		{name: "bisect", path: "BISECT_LOG"},
	}
	for _, operation := range operations {
		if _, err := os.Stat(filepath.Join(gitDir, operation.path)); err == nil { // #nosec G703 -- operation.path comes from the fixed list above.
			return operation.name, nil
		} else if !os.IsNotExist(err) {
			return "", err
		}
	}
	return "", nil
}

// IsLinkedWorktreeDirectory reports whether gitDir is a linked-worktree
// metadata directory beneath commonDir/worktrees.
func IsLinkedWorktreeDirectory(commonDir string, gitDir string) (bool, error) {
	rel, err := filepath.Rel(commonDir, gitDir)
	if err != nil {
		return false, err
	}
	if rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return false, nil
	}
	parts := strings.Split(filepath.Clean(rel), string(filepath.Separator))
	return len(parts) >= 2 && parts[0] == "worktrees", nil
}

// StatusPorcelainV1Z returns unquoted, NUL-delimited status entries and lists
// every untracked file instead of collapsing wholly untracked directories.
func (c Client) StatusPorcelainV1Z(ctx context.Context) (string, error) {
	return c.rawOutput(ctx, "status", "--porcelain=v1", "-z", "--untracked-files=all")
}

// Diff returns raw diff output for revspec.
func (c Client) Diff(ctx context.Context, revspec string) (string, error) {
	return c.rawOutput(ctx, "diff", revspec)
}

// WorkingDiff returns the combined staged and unstaged worktree diff from HEAD
// for only paths. Untracked files are not included by Git and must be supplied
// by callers that intentionally expose their contents.
func (c Client) WorkingDiff(ctx context.Context, paths ...string) (string, error) {
	args := append([]string{"diff", "HEAD", "--"}, paths...)
	return c.rawOutput(ctx, args...)
}

// RecentLog returns at most limit one-line commit summaries.
func (c Client) RecentLog(ctx context.Context, limit int) (string, error) {
	if limit <= 0 {
		return "", errors.New("recent log limit must be positive")
	}
	return c.rawOutput(ctx, "log", "--oneline", fmt.Sprintf("-%d", limit))
}

// ChangedFiles returns changed file paths from diff --name-only for revspec.
func (c Client) ChangedFiles(ctx context.Context, revspec string) ([]string, error) {
	out, err := c.rawOutput(ctx, "diff", "--name-only", revspec)
	if err != nil {
		return nil, err
	}
	files := make([]string, 0)
	for line := range strings.SplitSeq(out, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			files = append(files, line)
		}
	}
	return files, nil
}

// DiffStat returns raw diff --stat output for revspec.
func (c Client) DiffStat(ctx context.Context, revspec string) (string, error) {
	return c.rawOutput(ctx, "diff", "--stat", revspec)
}

// Checkout checks out branch.
func (c Client) Checkout(ctx context.Context, branch string) error {
	return c.run(ctx, "checkout", branch)
}

// MergeFFOnly fast-forwards the current branch to ref.
func (c Client) MergeFFOnly(ctx context.Context, ref string) error {
	if err := c.run(ctx, "merge", "--ff-only", ref); err != nil {
		return fmt.Errorf("fast-forward merge %q: %w", ref, err)
	}
	return nil
}

// MergeSquash stages the combined changes from ref without creating a commit.
func (c Client) MergeSquash(ctx context.Context, ref string) error {
	if err := c.run(ctx, "merge", "--squash", ref); err != nil {
		return fmt.Errorf("squash merge %q: %w", ref, err)
	}
	return nil
}

// HasStagedChanges reports whether the index differs from HEAD.
func (c Client) HasStagedChanges(ctx context.Context) (bool, error) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	args := []string{"diff", "--cached", "--quiet"}
	err := c.git(ctx, args, &stdout, &stderr)
	if err == nil {
		return false, nil
	}
	var exitCoder interface{ ExitCode() int }
	if errors.As(err, &exitCoder) && exitCoder.ExitCode() == 1 {
		return true, nil
	}
	return false, fmt.Errorf("inspect staged changes: %w", commandError(args, err, stderr.String()))
}

// CleanUntracked removes disposable untracked files and directories.
func (c Client) CleanUntracked(ctx context.Context) error {
	if err := c.run(ctx, "clean", "-fdx"); err != nil {
		return fmt.Errorf("clean untracked files: %w", err)
	}
	return nil
}

// Rebase rebases the current branch onto the given revision.
func (c Client) Rebase(ctx context.Context, onto string) error {
	return c.run(ctx, "rebase", onto)
}

// RebaseAbort aborts an in-progress rebase.
func (c Client) RebaseAbort(ctx context.Context) error {
	return c.run(ctx, "rebase", "--abort")
}

// ResetHard resets the current branch and worktree to ref.
func (c Client) ResetHard(ctx context.Context, ref string) error {
	return c.run(ctx, "reset", "--hard", ref)
}

// Add stages paths.
func (c Client) Add(ctx context.Context, paths ...string) error {
	args := append([]string{"add", "--"}, paths...)
	return c.run(ctx, args...)
}

// RestoreStaged unstages paths.
func (c Client) RestoreStaged(ctx context.Context, paths ...string) error {
	args := append([]string{"restore", "--staged", "--"}, paths...)
	return c.run(ctx, args...)
}

// Commit creates a commit with message.
func (c Client) Commit(ctx context.Context, message string) error {
	return c.run(ctx, "commit", "-m", message)
}

// CommitPaths creates a commit with message limited to paths.
func (c Client) CommitPaths(ctx context.Context, message string, paths ...string) error {
	if len(paths) == 0 {
		return errors.New("commit paths: at least one path is required")
	}
	args := append([]string{"commit", "-m", message, "--"}, paths...)
	return c.run(ctx, args...)
}

// LocalBranchExists reports whether branch exists as a local branch.
func (c Client) LocalBranchExists(ctx context.Context, branch string) (bool, error) {
	out, err := c.output(ctx, "branch", "--format=%(refname:short)", "--list", branch)
	if err != nil {
		return false, err
	}
	for line := range strings.SplitSeq(out, "\n") {
		if strings.TrimSuffix(line, "\r") == branch {
			return true, nil
		}
	}
	return false, nil
}

// RemoteTrackingBranchExists reports whether any known remote-tracking ref has
// the exact branch name after its remote component.
func (c Client) RemoteTrackingBranchExists(ctx context.Context, branch string) (bool, error) {
	out, err := c.output(ctx, "for-each-ref", "--format=%(refname)", "refs/remotes")
	if err != nil {
		return false, err
	}
	for line := range strings.SplitSeq(out, "\n") {
		remoteAndBranch := strings.TrimPrefix(strings.TrimSpace(line), "refs/remotes/")
		if _, candidate, ok := strings.Cut(remoteAndBranch, "/"); ok && candidate == branch {
			return true, nil
		}
	}
	return false, nil
}

// RemoteBranchExists queries every configured remote for the exact branch ref.
// Unlike RemoteTrackingBranchExists, it does not depend on the last fetch.
func (c Client) RemoteBranchExists(ctx context.Context, branch string) (bool, error) {
	remotes, err := c.output(ctx, "remote")
	if err != nil {
		return false, err
	}
	ref := "refs/heads/" + branch
	for line := range strings.SplitSeq(remotes, "\n") {
		remote := strings.TrimSpace(line)
		if remote == "" {
			continue
		}
		out, err := c.rawOutput(ctx, "ls-remote", "--heads", remote, ref)
		if err != nil {
			return false, fmt.Errorf("check remote %q for branch %q: %w", remote, branch, err)
		}
		for result := range strings.SplitSeq(out, "\n") {
			fields := strings.Fields(result)
			if len(fields) >= 2 && fields[1] == ref {
				return true, nil
			}
		}
	}
	return false, nil
}

// DeleteBranch deletes a local branch. If force is true, Git's --force flag is added.
func (c Client) DeleteBranch(ctx context.Context, branch string, force bool) error {
	args := []string{"branch", "--delete"}
	if force {
		args = append(args, "--force")
	}
	args = append(args, branch)
	return c.run(ctx, args...)
}

// BranchExists reports whether rev resolves to an existing ref or commit.
func (c Client) BranchExists(ctx context.Context, rev string) (bool, error) {
	if _, err := c.output(ctx, "rev-parse", "--verify", rev); err != nil {
		return false, nil //nolint:nilerr // a failed rev-parse means the ref does not exist, not an error
	}
	return true, nil
}

// IsAncestor reports whether ancestor is an ancestor of descendant.
func (c Client) IsAncestor(ctx context.Context, ancestor string, descendant string) (bool, error) {
	if err := c.run(ctx, "merge-base", "--is-ancestor", ancestor, descendant); err != nil {
		return false, nil //nolint:nilerr // a non-zero merge-base exit means not-an-ancestor, not an error
	}
	return true, nil
}

// BranchMerged reports whether branch is already merged into HEAD.
func (c Client) BranchMerged(ctx context.Context, branch string) (bool, error) {
	return c.IsAncestor(ctx, branch, "HEAD")
}

// TopLevel returns the absolute path of the repository's top-level directory.
func (c Client) TopLevel(ctx context.Context) (string, error) {
	return c.output(ctx, "rev-parse", "--show-toplevel")
}

// ListBranches returns local branch names matching pattern. An empty pattern lists
// every local branch.
func (c Client) ListBranches(ctx context.Context, pattern string) ([]string, error) {
	args := []string{"branch", "--format=%(refname:short)", "--list"}
	if pattern != "" {
		args = append(args, pattern)
	}
	out, err := c.output(ctx, args...)
	if err != nil {
		return nil, err
	}
	var branches []string
	for line := range strings.SplitSeq(out, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			branches = append(branches, line)
		}
	}
	return branches, nil
}

// MergeMechanism identifies how a branch was proven to be merged.
type MergeMechanism string

const (
	MergeMechanismNone     MergeMechanism = ""
	MergeMechanismAncestry MergeMechanism = "ancestry"
	MergeMechanismSquash   MergeMechanism = "squash"
)

// MergedInto reports whether branch's changes are already contained in base. It
// detects fast-forward and merge-commit integration via ancestry, and squash
// merges via a synthetic-commit patch-id comparison.
func (c Client) MergedInto(ctx context.Context, branch string, base string) (bool, error) {
	mechanism, err := c.MergedIntoMechanism(ctx, branch, base)
	return mechanism != MergeMechanismNone, err
}

// MergedIntoMechanism reports how branch's changes were proven to be contained
// in base, or MergeMechanismNone when they could not be proven merged.
func (c Client) MergedIntoMechanism(ctx context.Context, branch string, base string) (MergeMechanism, error) {
	ancestor, err := c.IsAncestor(ctx, branch, base)
	if err != nil {
		return MergeMechanismNone, err
	}
	if ancestor {
		return MergeMechanismAncestry, nil
	}
	merged, err := c.squashMergedInto(ctx, branch, base)
	if err != nil {
		return MergeMechanismNone, err
	}
	if merged {
		return MergeMechanismSquash, nil
	}
	return MergeMechanismNone, nil
}

// squashMergedInto reports whether branch was squash-merged into base. It builds a
// synthetic commit holding branch's tree on top of the merge base, then asks
// git cherry whether base already contains an equivalent patch.
func (c Client) squashMergedInto(ctx context.Context, branch string, base string) (bool, error) {
	mergeBase, err := c.output(ctx, "merge-base", base, branch)
	if err != nil {
		// No common history means nothing we can prove is merged.
		return false, nil
	}
	tree, err := c.output(ctx, "rev-parse", branch+"^{tree}")
	if err != nil {
		return false, err
	}
	synthetic, err := c.output(ctx, "commit-tree", tree, "-p", mergeBase, "-m", "tao-cleanup-merge-probe")
	if err != nil {
		return false, err
	}
	cherry, err := c.output(ctx, "cherry", base, synthetic)
	if err != nil {
		// A cherry failure means we cannot prove the branch is merged.
		return false, nil
	}
	if cherry == "" {
		return true, nil
	}
	for line := range strings.SplitSeq(cherry, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "+") {
			return false, nil
		}
	}
	return true, nil
}

func (c Client) output(ctx context.Context, args ...string) (string, error) {
	out, err := c.rawOutput(ctx, args...)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

func (c Client) rawOutput(ctx context.Context, args ...string) (string, error) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if err := c.git(ctx, args, &stdout, &stderr); err != nil {
		return "", commandError(args, err, stderr.String())
	}
	return stdout.String(), nil
}

func (c Client) run(ctx context.Context, args ...string) error {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if err := c.git(ctx, args, &stdout, &stderr); err != nil {
		return commandError(args, err, stderr.String())
	}
	return nil
}

func (c Client) git(ctx context.Context, args []string, stdout io.Writer, stderr io.Writer) error {
	gitArgs := append([]string{"-C", c.repoRoot}, args...)
	return c.runner(ctx, "", "git", gitArgs, stdout, stderr)
}

func commandError(args []string, err error, stderr string) error {
	if text := strings.TrimSpace(stderr); text != "" {
		return fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, text)
	}
	return fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
}
