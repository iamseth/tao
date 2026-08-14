package run

import (
	"context"
	"fmt"
	"strings"
)

func (c deterministicPullRequestCreator) pushBranch(ctx context.Context, run PullRequestRun) error {
	git := gitClient(c.execution, run.RepoRoot)
	if run.Detail == nil || run.Detail.State.Plan.ChangeType == "" {
		return git.PushUpstream(ctx, run.Branch)
	}

	remoteHead, found, err := git.OriginRemoteHead(ctx, run.Branch)
	if err != nil {
		return fmt.Errorf("check typed branch remote ownership: %w", err)
	}
	leaseHead, err := pullRequestPushLease(run, remoteHead, found)
	if err != nil {
		return err
	}
	return git.PushUpstreamWithLease(ctx, run.Branch, leaseHead)
}

func pullRequestPushLease(run PullRequestRun, remoteHead string, found bool) (string, error) {
	branch := strings.TrimSpace(run.Branch)
	head := strings.TrimSpace(run.HeadSHA)
	priorHead := recordedPushedHead(run)
	switch {
	case !found && priorHead != "":
		return "", fmt.Errorf("push typed branch %q: remote branch is missing, want recorded Tao head %s", branch, priorHead)
	case found && remoteHead == head:
		// Preserve idempotent publication when a prior push reached the remote
		// before its local state was recorded.
		return head, nil
	case found && priorHead != "" && remoteHead == priorHead:
		// A recorded workspace branch and its last pushed head are durable
		// ownership evidence. Advance it only while the remote still equals the
		// recorded boundary, as happens when a published PR is reworked.
		return priorHead, nil
	case found && priorHead != "":
		return "", fmt.Errorf("push typed branch %q: remote branch is at %s, want recorded Tao head %s or new reviewed head %s", branch, remoteHead, priorHead, head)
	case found:
		return "", fmt.Errorf("push typed branch %q: remote branch already exists at %s, want exact Tao head %s", branch, remoteHead, head)
	default:
		return "", nil
	}
}

func recordedPushedHead(run PullRequestRun) string {
	if run.Detail == nil || run.Detail.State.Workspace == nil {
		return ""
	}
	workspace := run.Detail.State.Workspace
	if strings.TrimSpace(workspace.Branch) != strings.TrimSpace(run.Branch) {
		return ""
	}
	return strings.TrimSpace(workspace.PushedSHA)
}
