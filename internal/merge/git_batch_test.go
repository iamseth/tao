package merge

import (
	"context"
	"errors"
	"slices"
	"testing"
)

type batchingFakeGit struct {
	*fakeGitClient
	batch    []string
	batchErr error
	batches  int
}

func (g *batchingFakeGit) RevParseAll(context.Context, ...string) ([]string, error) {
	g.batches++
	return g.batch, g.batchErr
}

func TestRevParseAllBatchesAndFailsClosed(t *testing.T) {
	missing := errors.New("missing ref")
	revs := []string{"refs/heads/plan", "refs/heads/main", "HEAD"}
	perRef := func() *fakeGitClient {
		return &fakeGitClient{
			revParse:       map[string]string{"refs/heads/main": "main-sha", "HEAD": "head-sha"},
			revParseErrors: map[string][]error{"refs/heads/plan": {missing}},
		}
	}

	batched := &batchingFakeGit{fakeGitClient: perRef(), batch: []string{"plan-sha", "main-sha", "head-sha"}}
	values, errs := revParseAll(context.Background(), batched, revs...)
	if !slices.Equal(values, batched.batch) || errors.Join(errs...) != nil || batched.batches != 1 || len(batched.calls) != 0 {
		t.Fatalf("batched = %q, %v, batches %d, calls %q", values, errs, batched.batches, batched.calls)
	}

	// A failed batch fails every revision and is never retried per revision,
	// so a transient fault cannot be masked by a later successful query.
	batchErr := errors.New("one ref is missing")
	failed := &batchingFakeGit{fakeGitClient: perRef(), batchErr: batchErr}
	values, errs = revParseAll(context.Background(), failed, revs...)
	for i, err := range errs {
		if !errors.Is(err, batchErr) || values[i] != "" {
			t.Fatalf("failed batch: values %q, errors %v", values, errs)
		}
	}
	if len(failed.calls) != 0 {
		t.Fatalf("failed batch retried per revision: %q", failed.calls)
	}

	values, errs = revParseAll(context.Background(), perRef(), revs...)
	if !errors.Is(errs[0], missing) || errs[1] != nil || errs[2] != nil || values[1] != "main-sha" || values[2] != "head-sha" {
		t.Fatalf("per-revision fallback: values %q, errors %v", values, errs)
	}
}

func TestGitBoundaryDirsMatchesSeparateResolution(t *testing.T) {
	fixture := newRealGitWorktree(t)
	ctx := context.Background()
	for _, root := range []string{fixture.repoRoot, fixture.worktreePath} {
		gitDir, commonDir, err := gitBoundaryDirs(ctx, root)
		if err != nil {
			t.Fatal(err)
		}
		wantGitDir, err := gitBoundaryPath(ctx, root, "--absolute-git-dir")
		if err != nil {
			t.Fatal(err)
		}
		wantCommonDir, err := gitBoundaryPath(ctx, root, "--path-format=absolute", "--git-common-dir")
		if err != nil {
			t.Fatal(err)
		}
		if gitDir != wantGitDir || commonDir != wantCommonDir {
			t.Fatalf("%s: dirs = %s, %s; want %s, %s", root, gitDir, commonDir, wantGitDir, wantCommonDir)
		}
	}
	if fixture.repoRoot == fixture.worktreePath {
		t.Fatal("fixture must cover a linked worktree")
	}
}
