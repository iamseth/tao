package agentsession

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/iamseth/tao/internal/commandrunner"
	"github.com/iamseth/tao/internal/gitops"
)

// ControlCheckoutLeakError reports that an isolated agent session changed the
// control checkout instead of confining edits to the execution worktree.
type ControlCheckoutLeakError struct {
	ControlRoot string
	Paths       []string
}

func (e ControlCheckoutLeakError) Error() string {
	paths := strings.Join(e.Paths, ", ")
	if paths == "" {
		paths = "(unable to determine paths)"
	}
	return fmt.Sprintf("agent session changed control checkout %s; leaked paths: %s", e.ControlRoot, paths)
}

func guardControlCheckoutLeaks[T any](ctx context.Context, runner commandrunner.Runner, controlRoot, executionRoot string, session func() (T, error)) (T, error) {
	if sameCheckoutRoot(controlRoot, executionRoot) {
		return session()
	}
	if runner == nil {
		runner = commandrunner.DefaultLocal
	}
	git := gitops.NewClient(controlRoot, runner)
	before, err := git.DirtyFingerprint(ctx)
	if err != nil {
		var zero T
		return zero, fmt.Errorf("capture control checkout dirty fingerprint before agent session: %w", err)
	}
	result, runErr := session()
	after, err := git.DirtyFingerprint(ctx)
	if err != nil {
		var zero T
		return zero, fmt.Errorf("capture control checkout dirty fingerprint after agent session: %w", err)
	}
	if before.Hash != after.Hash {
		return result, ControlCheckoutLeakError{ControlRoot: controlRoot, Paths: changedLeakPaths(before, after)}
	}
	return result, runErr
}

func sameCheckoutRoot(controlRoot, executionRoot string) bool {
	if controlRoot == "" || executionRoot == "" {
		return true
	}
	return canonicalRoot(controlRoot) == canonicalRoot(executionRoot)
}

func canonicalRoot(root string) string {
	cleaned := filepath.Clean(root)
	if evaluated, err := filepath.EvalSymlinks(cleaned); err == nil {
		return evaluated
	}
	return cleaned
}

func changedLeakPaths(before, after gitops.DirtyFingerprint) []string {
	beforeSet := pathSet(before.Paths)
	afterSet := pathSet(after.Paths)
	var paths []string
	for path := range afterSet {
		if !beforeSet[path] {
			paths = append(paths, path)
		}
	}
	for path := range beforeSet {
		if !afterSet[path] {
			paths = append(paths, path)
		}
	}
	if len(paths) == 0 {
		paths = append(paths, after.Paths...)
	}
	sort.Strings(paths)
	return paths
}

func pathSet(paths []string) map[string]bool {
	set := make(map[string]bool, len(paths))
	for _, path := range paths {
		if path != "" {
			set[path] = true
		}
	}
	return set
}
