package run

import (
	"fmt"
	"sort"
	"strings"

	"github.com/iamseth/tao/internal/plan"
)

type commitLeftoverAmbiguousStatusError struct {
	Lines []string
}

func (e *commitLeftoverAmbiguousStatusError) Error() string {
	if len(e.Lines) == 1 {
		return fmt.Sprintf("ambiguous git status entry %q", e.Lines[0])
	}
	return fmt.Sprintf("ambiguous git status entries: %s", strings.Join(e.Lines, ", "))
}

// commitLeftovers reports run-produced uncommitted paths for review cleanliness:
// non-.tao git status candidates minus the optional starting-dirty tolerance.
// expected_files is advisory only and does not filter completeness.
func commitLeftovers(detail *plan.PlanDetail, status string, isStartingDirty func(string) bool) ([]string, error) {
	if detail == nil {
		return nil, nil
	}
	cls := classifyGitStatus(status, isStartingDirty)
	if len(cls.AmbiguousLines) > 0 {
		return nil, &commitLeftoverAmbiguousStatusError{Lines: cls.AmbiguousLines}
	}
	seen := map[string]bool{}
	var paths []string
	for _, path := range cls.CommitCandidates {
		if isStartingDirty != nil && isStartingDirty(path) {
			continue
		}
		if !seen[path] {
			seen[path] = true
			paths = append(paths, path)
		}
	}
	if len(paths) == 0 {
		return nil, nil
	}
	sort.Strings(paths)
	return paths, nil
}
