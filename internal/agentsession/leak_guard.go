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

// ControlCheckoutPathChange describes a change to the checkout's dirty path set.
// Kind is appeared, disappeared, or modified; it does not imply authorship.
type ControlCheckoutPathChange struct {
	Path    string
	Tracked bool
	Kind    string
}

// ControlCheckoutChange is diagnostic evidence, not attribution or authority.
type ControlCheckoutChange struct {
	ControlRoot string
	Paths       []string
	Changes     []ControlCheckoutPathChange
	HeadBefore  string
	HeadAfter   string
}

// ControlCheckoutLeakError reports a control checkout change during an isolated
// session, which may have been made by another process.
type ControlCheckoutLeakError struct {
	ControlRoot string
	Paths       []string
	Change      ControlCheckoutChange
}

func (e ControlCheckoutLeakError) Error() string {
	change := e.Change
	if change.ControlRoot == "" {
		change.ControlRoot = e.ControlRoot
	}
	if len(change.Changes) == 0 {
		paths := change.Paths
		if len(paths) == 0 {
			paths = e.Paths
		}
		for _, path := range paths {
			change.Changes = append(change.Changes, ControlCheckoutPathChange{Path: path, Tracked: true, Kind: "modified"})
		}
	}
	message := fmt.Sprintf("control checkout %s changed during the agent session (possibly by another process)", change.ControlRoot)
	if len(change.Changes) > 0 {
		var paths []string
		for _, path := range change.Changes[:min(20, len(change.Changes))] {
			state := "untracked"
			if path.Tracked {
				state = "tracked"
			}
			paths = append(paths, fmt.Sprintf("%s (%s, %s)", path.Path, state, path.Kind))
		}
		if len(change.Changes) > 20 {
			paths = append(paths, fmt.Sprintf("and %d more", len(change.Changes)-20))
		}
		message += "; changed paths: " + strings.Join(paths, ", ")
	}
	headMoved := change.HeadBefore != "" && change.HeadAfter != "" && change.HeadBefore != change.HeadAfter
	if headMoved {
		message += fmt.Sprintf("; HEAD moved %s -> %s", change.HeadBefore, change.HeadAfter)
	}
	if len(change.Changes) == 0 && !headMoved {
		message += "; index or staged content changed without working-tree path differences"
	}
	// Preserve concrete evidence on continuation lines rather than truncating paths.
	runes := []rune(message)
	if len(runes) > 511 {
		message = string(runes[:511]) + "\n" + string(runes[511:])
	}
	return message
}

func guardControlCheckoutLeaks[T any](ctx context.Context, runner commandrunner.Runner, controlRoot, executionRoot string, session func() (T, error), attribute func(context.Context, ControlCheckoutChange) bool) (T, error) {
	if sameCheckoutRoot(controlRoot, executionRoot) {
		return session()
	}
	if runner == nil {
		runner = commandrunner.DefaultLocal
	}
	git := gitops.NewClient(controlRoot, runner)
	before, err := controlFingerprint(ctx, git)
	if err != nil {
		var zero T
		return zero, fmt.Errorf("capture control checkout dirty fingerprint before agent session: %w", err)
	}
	headBefore, headErr := git.RevParse(ctx, "HEAD")
	if headErr != nil {
		headBefore = ""
	}
	result, runErr := session()
	after, err := controlFingerprint(ctx, git)
	if err != nil {
		var zero T
		return zero, fmt.Errorf("capture control checkout dirty fingerprint after agent session: %w", err)
	}
	headAfter, headErr := git.RevParse(ctx, "HEAD")
	if headErr != nil {
		headAfter = ""
	}
	if before.Hash != after.Hash || (headBefore != "" && headAfter != "" && headBefore != headAfter) {
		change := ControlCheckoutChange{ControlRoot: controlRoot, HeadBefore: headBefore, HeadAfter: headAfter}
		if before.Hash != after.Hash {
			change.Changes = changedLeakPaths(before, after)
			for _, path := range change.Changes {
				change.Paths = append(change.Paths, path.Path)
			}
		}
		if attribute != nil && !attribute(ctx, change) {
			return result, runErr
		}
		return result, ControlCheckoutLeakError{ControlRoot: controlRoot, Paths: change.Paths, Change: change}
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

// Keep the shared fingerprint hash and path computation unchanged, but obtain
// exact dirty paths here so rename evidence includes both endpoints.
func controlFingerprint(ctx context.Context, git gitops.Client) (gitops.DirtyFingerprint, error) {
	fingerprint, err := git.DirtyFingerprint(ctx)
	if err != nil || len(fingerprint.Paths) == 0 {
		return fingerprint, err
	}
	paths, err := git.ChangedFilesExact(ctx, "HEAD")
	if err != nil {
		return fingerprint, err
	}
	fingerprint.Paths = append(fingerprint.Paths, paths...)
	return fingerprint, nil
}

func changedLeakPaths(before, after gitops.DirtyFingerprint) []ControlCheckoutPathChange {
	beforeSet := pathSet(before.Paths)
	afterSet := pathSet(after.Paths)
	beforeUntracked := pathSet(before.Untracked)
	afterUntracked := pathSet(after.Untracked)
	var paths []ControlCheckoutPathChange
	for path := range afterSet {
		kind := "appeared"
		if beforeSet[path] {
			// The aggregate fingerprint cannot exclude changes to persistent
			// dirty paths, even when other paths appear or disappear.
			kind = "modified"
		}
		paths = append(paths, ControlCheckoutPathChange{path, !afterUntracked[path], kind})
	}
	for path := range beforeSet {
		if !afterSet[path] {
			paths = append(paths, ControlCheckoutPathChange{path, !beforeUntracked[path], "disappeared"})
		}
	}
	sort.Slice(paths, func(i, j int) bool { return paths[i].Path < paths[j].Path })
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
