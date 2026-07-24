package run

import (
	"github.com/iamseth/tao/internal/commit"
	"github.com/iamseth/tao/internal/plan"
)

// Run keeps plan-specific expected-files assembly here while delegating the
// reusable status, path, and safety contract to internal/commit.
type gitStatusClassification = commit.StatusClassification

func classifyGitStatus(status string, isStartingDirty func(string) bool) gitStatusClassification {
	return commit.ClassifyStatus(status, isStartingDirty)
}

func startingDirtyPredicate(paths []string) func(string) bool {
	return commit.StartingDirtyPredicate(paths)
}

type expectedPlanCommitPathSet struct {
	exact map[string]bool
	globs []string
}

func (s expectedPlanCommitPathSet) shared() commit.ExpectedPaths {
	patterns := make([]string, 0, len(s.exact)+len(s.globs))
	for path := range s.exact {
		patterns = append(patterns, path)
	}
	patterns = append(patterns, s.globs...)
	return commit.NewExpectedPaths(patterns...)
}

func (s expectedPlanCommitPathSet) Allows(path string) bool {
	return s.shared().Allows(path)
}

func unexpectedPlanCommitPaths(paths []string, allowed expectedPlanCommitPathSet) []string {
	return commit.UnexpectedPaths(paths, allowed.shared())
}

func expectedPlanCommitPaths(detail *plan.PlanDetail, additionallyCompleted ...string) expectedPlanCommitPathSet {
	allowed := expectedPlanCommitPathSet{exact: map[string]bool{}}
	completed := map[string]bool{}
	for _, id := range detail.State.Plan.CompletedSlices {
		completed[id] = true
	}
	for _, id := range additionallyCompleted {
		completed[id] = true
	}
	for _, slice := range detail.Slices.Slices {
		if !completed[slice.ID] {
			continue
		}
		for _, path := range slice.ExpectedFiles {
			path = normalizePlanCommitPath(path)
			if hasPlanCommitGlobMeta(path) {
				allowed.globs = append(allowed.globs, path)
				continue
			}
			allowed.exact[path] = true
		}
	}
	return allowed
}

func normalizePlanCommitPath(path string) string {
	return commit.NormalizePath(path)
}

func hasPlanCommitGlobMeta(pattern string) bool {
	return commit.HasPathGlobMeta(pattern)
}

func planCommitGlobMatch(pattern, path string) bool {
	return commit.PathPatternMatch(pattern, path)
}

func planCommitGlobRegexp(pattern string) string {
	return commit.PathPatternRegexp(pattern)
}

// commitSafetyPolicy remains as a compatibility adapter for run package tests
// and callers while internal/commit owns the policy itself.
type commitSafetyPolicy struct{}

var defaultCommitSafetyPolicy commitSafetyPolicy

func (commitSafetyPolicy) suspectedSecret(path string) bool {
	return commit.SuspectedSecretPath(path)
}

func (commitSafetyPolicy) generated(path string) bool {
	return commit.GeneratedPath(path)
}

func suspectedSecretPath(path string) bool {
	return commit.SuspectedSecretPath(path)
}

func generatedPath(path string) bool {
	return commit.GeneratedPath(path)
}
