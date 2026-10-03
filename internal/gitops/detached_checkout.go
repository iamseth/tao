package gitops

import (
	"context"
	"errors"
	"fmt"
	"os"
)

// WithDetachedCheckout runs fn in a disposable shared clone at sha, without
// changing the source repository's checkout or registering a worktree there.
func (c Client) WithDetachedCheckout(ctx context.Context, sha string, fn func(dir string) error) (err error) {
	resolved, err := c.output(ctx, "rev-parse", "--verify", "--end-of-options", sha+"^{commit}")
	if err != nil {
		return fmt.Errorf("resolve detached checkout %q: %w", sha, err)
	}
	dir, err := os.MkdirTemp("", "tao-baseline-probe-")
	if err != nil {
		return fmt.Errorf("create detached checkout directory: %w", err)
	}
	defer func() {
		if cleanupErr := os.RemoveAll(dir); cleanupErr != nil {
			err = errors.Join(err, fmt.Errorf("remove detached checkout: %w", cleanupErr))
		}
	}()
	if err := c.runAt(ctx, c.repoRoot, "clone", "--shared", "--no-checkout", "--", c.repoRoot, dir); err != nil {
		return fmt.Errorf("clone detached checkout: %w", err)
	}
	if err := NewClient(dir, c.runner).run(ctx, "checkout", "--detach", resolved); err != nil {
		return fmt.Errorf("check out detached revision: %w", err)
	}
	if err := fn(dir); err != nil {
		return fmt.Errorf("run in detached checkout: %w", err)
	}
	return nil
}
