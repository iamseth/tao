package merge

import (
	"context"
	"slices"
)

// preparedMergeEdits retains one porcelain snapshot for comparison and deferred
// bucket staging. It grants no commit or rollback authority: callers must run
// their own marker checks before stage and retain their settlement policy.
// Legacy batch intents remain caller-owned: candidate uses its historical raw
// status path, aggregate still checks paths, and both stage plain Add(".").
// Single settlement always compares a fingerprint, including an absent one.
type preparedMergeEdits struct {
	porcelainChanges
}

func prepareMergeEdits(ctx context.Context, git GitClient) (preparedMergeEdits, error) {
	changes, err := concretePorcelainChanges(ctx, git)
	return preparedMergeEdits{changes}, err
}

func (edits preparedMergeEdits) matchesPaths(paths []string) bool {
	return slices.Equal(edits.changedPaths, paths)
}

func (edits preparedMergeEdits) matchesContent(root, expected string) (bool, error) {
	fingerprint, err := aggregateReworkContentFingerprint(root, edits.changedPaths)
	return fingerprint == expected, err
}

func (edits preparedMergeEdits) stage(ctx context.Context, git GitClient) error {
	return stageResolutionChanges(ctx, git, edits.porcelainChanges)
}
