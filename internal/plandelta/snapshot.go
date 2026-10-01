package plandelta

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/iamseth/tao/internal/commandrunner"
	"github.com/iamseth/tao/internal/gitops"
	"github.com/iamseth/tao/internal/plan"
)

// Snapshot collects advisory committed or live worktree changes.
func (c Collector) Snapshot(ctx context.Context, detail *plan.PlanDetail, scope Scope) (s Snapshot, err error) {
	s.Scope = scope
	if c.Now != nil {
		s.CollectedAt = c.Now()
	}
	// Some legacy Git probes swallow errors (notably default/ancestor probes).
	// Cancellation must still escape every availability and fallback path.
	var probeCancellation error
	defer func() {
		s.Base.DefaultBranch = sanitizeReason(s.Base.DefaultBranch)
		s.Base.PlanBranch = sanitizeReason(s.Base.PlanBranch)
		if probeCancellation != nil {
			err = probeCancellation
		}
		if ctx.Err() != nil {
			err = ctx.Err()
		}
	}()
	if ctx.Err() != nil {
		return s, ctx.Err()
	}
	if scope != "" && scope != ScopeBranch && scope != ScopeWorktree {
		return unavailable(s, AvailabilityGitError, "unsupported changes scope"), nil
	}
	if detail == nil || !filepath.IsAbs(strings.TrimSpace(detail.State.Repo.Root)) {
		return unavailable(s, AvailabilityNoRepoRoot, "no absolute repository root recorded"), nil
	}
	target, e := ResolveTarget(detail, c.Config)
	if e != nil {
		return unavailable(s, AvailabilityNoBranch, e.Error()), nil
	}
	s.Target = target
	if e = directory(target.RepoRoot); e != nil {
		return unavailable(s, AvailabilityRepoInaccessible, e.Error()), nil
	}
	if target.Separate {
		if e = directory(target.WorktreePath); e != nil {
			s.WorktreeMissing = true
			s.warn("worktree missing or inaccessible: " + e.Error())
		}
	}
	if scope == "" || scope == ScopeWorktree {
		s.Scope = ScopeWorktree
		if s.WorktreeMissing {
			s.Scope = ScopeBranch
		}
	}
	// Observe failures hidden by legacy helpers without changing their semantics.
	// Use the same local runner as Git clients when none was injected.
	runner := c.Runner
	if runner == nil {
		runner = commandrunner.DefaultLocal
	}
	var probeFailures []error
	observedRunner := func(ctx context.Context, cwd, name string, args []string, out, errout io.Writer) error {
		e := runner(ctx, cwd, name, args, out, errout)
		if e != nil {
			probeFailures = append(probeFailures, e)
			if errors.Is(e, context.Canceled) || errors.Is(e, context.DeadlineExceeded) {
				probeCancellation = e
			}
		}
		return e
	}
	git := gitops.NewReadOnlyClient(target.RepoRoot, observedRunner)
	tip, exists, e := git.VerifyCommit(ctx, "refs/heads/"+target.Branch)
	if e != nil {
		return unavailable(s, AvailabilityGitError, e.Error()), nil
	}
	if !exists {
		reason := "branch not found (cleaned up?)"
		if target.BranchIsNew {
			reason = "branch has not been created yet"
		}
		return unavailable(s, AvailabilityNoBranch, reason), nil
	}
	probeFailures = nil
	s.Base = ResolveBase(ctx, git, detail.State)
	if len(probeFailures) > 0 {
		s.warn("live comparison base lookup failed; using recorded fallback when available")
	}
	// Worktree scope resolves detached HEAD before validating its own base.
	if s.Scope == ScopeBranch {
		if s.Base.SHA == "" {
			return unavailable(s, AvailabilityNoBase, "no comparison base available"), nil
		}
		base, exists, e := git.VerifyCommit(ctx, s.Base.SHA)
		if e != nil {
			return unavailable(s, AvailabilityGitError, e.Error()), nil
		}
		if !exists {
			return unavailable(s, AvailabilityNoBase, "comparison base is not available locally"), nil
		}
		s.Base.SHA = base
	}
	s.Head = tip
	if target.Strategy == plan.WorkspaceStrategyCurrent {
		s.Head, exists, e = git.VerifyCommit(ctx, "HEAD")
		if e != nil {
			return unavailable(s, AvailabilityGitError, e.Error()), nil
		}
		if !exists {
			return unavailable(s, AvailabilityGitError, "current checkout has no commit"), nil
		}
	}
	defaultBranch := s.Base.DefaultBranch
	if defaultBranch == "" {
		probeFailures = nil
		defaultBranch, e = git.DefaultBranch(ctx)
		if e != nil {
			s.warn("default branch: " + e.Error())
		} else if len(probeFailures) > 0 {
			s.warn("default branch lookup used a local fallback")
		}
	}
	if defaultBranch != "" {
		s.DefaultHead, exists, e = git.VerifyCommit(ctx, "refs/heads/"+defaultBranch)
		if e != nil {
			s.warn("default head: " + e.Error())
		} else if !exists {
			s.warn("default branch is not available locally")
		}
	}
	if target.Separate && target.Branch != defaultBranch && s.DefaultHead != "" {
		probeFailures = nil
		merged, _ := git.IsAncestor(ctx, tip, s.DefaultHead)
		// Exit 1 means not an ancestor; all other failures leave the badge unknown.
		if len(probeFailures) == 0 || len(probeFailures) == 1 && exitCodeOne(probeFailures[0]) {
			s.Merged = &merged
		} else {
			s.warn("ancestor lookup failed")
		}
	}
	operationRoot := target.WorktreePath
	if !s.WorktreeMissing {
		s.ActiveOperation, e = gitops.ActiveOperation(operationRoot)
		if e != nil {
			s.warn("active operation: " + e.Error())
		}
	}
	s.RebaseIntent = detail.State.Workspace != nil && detail.State.Workspace.RebaseIntent != nil
	if s.Scope == ScopeWorktree {
		s.Base.DefaultBranch = defaultBranch
		return collectWorktree(ctx, gitops.NewReadOnlyClient(target.WorktreePath, observedRunner), s, detail, tip)
	}
	if review := plan.PersistedReview(detail); review != nil {
		s.Review = ReviewParity{Recorded: true, Verdict: sanitizeReason(review.Verdict), Base: sanitizeReason(review.Base), Head: sanitizeReason(review.Head), BaseMatches: review.Base != "" && review.Base == s.Base.SHA, HeadMatches: review.Head != "" && review.Head == s.Head, Superseded: plan.CurrentReview(detail) == nil}
	}
	names, nt, e := git.DiffNameStatusZ(ctx, MaxListBytes, s.Base.SHA, s.Head, "--")
	if e != nil {
		return unavailable(s, AvailabilityGitError, e.Error()), nil
	}
	stats, st, e := git.DiffNumstatZ(ctx, MaxListBytes, s.Base.SHA, s.Head, "--")
	if e != nil {
		return unavailable(s, AvailabilityGitError, e.Error()), nil
	}
	files, e := joinBranchLists(names, stats, st)
	if e != nil {
		return unavailable(s, AvailabilityGitError, e.Error()), nil
	}
	s.FilesTruncated = nt || st || len(files) > MaxFiles
	// Hash all retained logical records, including those beyond the display cap.
	s.Signature = snapshotSignature(s, files)
	for _, file := range files {
		s.Total.Files++
		s.Total.Added += file.Added
		s.Total.Deleted += file.Deleted
	}
	if len(files) > MaxFiles {
		files = files[:MaxFiles]
	}
	s.Files = files
	s.Base.DefaultBranch = sanitizeReason(defaultBranch)
	s.Base.PlanBranch = sanitizeReason(s.Base.PlanBranch)
	// Target remains raw identity, like FileChange.Path; labels must be sanitized
	// by consumers rather than corrupting subsequent Git/path lookup keys.
	s.Availability = AvailabilityReady
	return s, nil
}

func unavailable(s Snapshot, a Availability, reason string) Snapshot {
	s.Availability = a
	s.Reason = sanitizeReason(reason)
	return s
}
func (s *Snapshot) warn(reason string) { s.Warnings = append(s.Warnings, sanitizeReason(reason)) }
func directory(path string) error {
	info, e := os.Stat(path)
	if e != nil {
		return e
	}
	if !info.IsDir() {
		return fmt.Errorf("not a directory: %s", path)
	}
	return nil
}
func exitCodeOne(err error) bool {
	var exit interface{ ExitCode() int }
	return errors.As(err, &exit) && exit.ExitCode() == 1
}

func excludedDeltaPath(path string) bool {
	return path == ".git" || strings.HasPrefix(path, ".git/") || gitops.IsTaoMetadataPath(path)
}

func joinBranchLists(names []gitops.NameStatusEntry, stats []gitops.NumstatEntry, statsTruncated bool) ([]FileChange, error) {
	counts := make(map[string]gitops.NumstatEntry, len(stats))
	for _, st := range stats {
		if excludedDeltaPath(st.Path) {
			continue
		}
		if e := validatePath(st.Path); e != nil {
			return nil, e
		}
		if st.OldPath != "" {
			return nil, errors.New("unexpected numstat rename")
		}
		if _, ok := counts[st.Path]; ok {
			return nil, errors.New("duplicate numstat path")
		}
		counts[st.Path] = st
	}
	seen := make(map[string]bool, len(names))
	var files []FileChange
	for _, name := range names {
		if excludedDeltaPath(name.Path) {
			continue
		}
		if e := validatePath(name.Path); e != nil {
			return nil, e
		}
		if len(name.Status) != 1 || !strings.ContainsAny(name.Status, "AMDTUXB") || name.OldPath != "" {
			return nil, errors.New("unexpected name-status record")
		}
		if seen[name.Path] {
			return nil, errors.New("duplicate name-status path")
		}
		seen[name.Path] = true
		st, ok := counts[name.Path]
		if !ok && !statsTruncated {
			continue
		}
		files = append(files, FileChange{Path: name.Path, Status: name.Status[0], Added: st.Added, Deleted: st.Deleted, Binary: st.Binary})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return files, nil
}
