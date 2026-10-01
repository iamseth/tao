package plandelta

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/iamseth/tao/internal/gitops"
	"github.com/iamseth/tao/internal/plan"
)

func collectWorktree(ctx context.Context, git gitops.Client, s Snapshot, detail *plan.PlanDetail, tip string) (Snapshot, error) {
	head, exists, err := git.VerifyCommit(ctx, "HEAD")
	if err != nil {
		return unavailable(s, AvailabilityGitError, err.Error()), nil
	}
	if !exists {
		return unavailable(s, AvailabilityGitError, "worktree has no HEAD"), nil
	}
	s.Head = head
	if s.ActiveOperation != "" || head != tip {
		base := ""
		if s.DefaultHead != "" {
			base, err = git.MergeBase(ctx, s.DefaultHead, head)
		}
		if err == nil && strings.TrimSpace(base) != "" {
			s.Base.SHA, s.Base.Source = strings.TrimSpace(base), BaseSource("merge-base (worktree HEAD)")
		} else {
			s.warn("worktree HEAD merge-base unavailable; using recorded fallback")
			s.Base.SHA, s.Base.Source = WorkspaceBase(detail.State), BaseSourceWorkspace
			if s.Base.SHA == "" {
				s.Base.SHA, s.Base.Source = detail.State.Repo.BaseCommit, BaseSourceRepo
			}
		}
	}
	if s.Base.SHA == "" {
		return unavailable(s, AvailabilityNoBase, "no worktree comparison base"), nil
	}
	base, exists, err := git.VerifyCommit(ctx, s.Base.SHA)
	if err != nil {
		return unavailable(s, AvailabilityGitError, err.Error()), nil
	}
	if !exists {
		return unavailable(s, AvailabilityNoBase, "worktree comparison base unavailable"), nil
	}
	s.Base.SHA = base
	if review := plan.PersistedReview(detail); review != nil {
		// Review parity describes the committed branch, not the uncommitted overlay.
		s.Review = ReviewParity{Recorded: true, Verdict: sanitizeReason(review.Verdict), Base: sanitizeReason(review.Base), Head: sanitizeReason(review.Head), BaseMatches: review.Base != "" && review.Base == s.Base.SHA, HeadMatches: review.Head != "" && review.Head == tip, Superseded: plan.CurrentReview(detail) == nil}
	}
	names, nt, err := git.DiffNameStatusZ(ctx, MaxListBytes, s.Base.SHA, "--")
	if err != nil {
		return unavailable(s, AvailabilityGitError, err.Error()), nil
	}
	stats, st, err := git.DiffNumstatZ(ctx, MaxListBytes, s.Base.SHA, "--")
	if err != nil {
		return unavailable(s, AvailabilityGitError, err.Error()), nil
	}
	files, err := joinBranchLists(names, stats, st)
	if err != nil {
		return unavailable(s, AvailabilityGitError, err.Error()), nil
	}
	headStats, ht, err := git.DiffNumstatZ(ctx, MaxListBytes, head, "--")
	if err != nil {
		return unavailable(s, AvailabilityGitError, err.Error()), nil
	}
	// Reuse the strict count/path validation used for branch records.
	headNames := make([]gitops.NameStatusEntry, 0, len(headStats))
	for _, row := range headStats {
		headNames = append(headNames, gitops.NameStatusEntry{Path: row.Path, Status: "M"})
	}
	headFiles, err := joinBranchLists(headNames, headStats, ht)
	if err != nil {
		return unavailable(s, AvailabilityGitError, err.Error()), nil
	}
	raw, statusCap, statusErr := git.StatusPorcelainV2Z(ctx, MaxListBytes)
	var records []statusRecord
	if statusErr != nil {
		s.warn("worktree status unavailable: " + statusErr.Error())
	} else {
		records, err = parseStatus(raw)
		if err != nil {
			return unavailable(s, AvailabilityGitError, err.Error()), nil
		}
	}
	byPath := map[string]FileChange{}
	for _, f := range files {
		byPath[f.Path] = f
	}
	changed := map[string]bool{}
	var extra []string
	for _, h := range headFiles {
		changed[h.Path] = true
		f, ok := byPath[h.Path]
		if !ok {
			f = h
			f.RevertsCommitted = !nt && !st
		}
		f.Uncommitted = true
		byPath[h.Path] = f
		extra = append(extra, h.Path, strconv.Itoa(h.Added), strconv.Itoa(h.Deleted), strconv.FormatBool(h.Binary))
	}
	for _, r := range records {
		if r.Kind == '!' {
			continue
		}
		if r.Kind == '?' {
			byPath[r.Path] = FileChange{Path: r.Path, Status: '?', Untracked: true, Uncommitted: true}
		} else if changed[r.Path] || ht || r.Kind == 'u' || r.XY[0] != '.' {
			s.Dirty = true
			extra = append(extra, r.Path, r.OldPath, r.Fields)
		}
	}
	if len(headFiles) > 0 {
		s.Dirty = true
	}
	files = files[:0]
	for _, f := range byPath {
		files = append(files, f)
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	s.FilesTruncated = nt || st || ht || statusCap || len(files) > MaxFiles
	if nt || st || ht {
		s.warn("tracked change lists truncated")
	}
	if statusCap {
		s.warn("worktree status truncated")
	}
	digests, err := worktreeDigests(ctx, git, head, headFiles)
	if err != nil {
		return unavailable(s, AvailabilityGitError, err.Error()), nil
	}
	extra = append(extra, digests...)
	allFiles := files
	if len(files) > MaxFiles {
		files = files[:MaxFiles]
	}
	for _, f := range files {
		s.Total.Files++
		s.Total.Added += f.Added
		s.Total.Deleted += f.Deleted
		if f.Uncommitted {
			s.Uncommitted.Files++
			s.Uncommitted.Added += f.Added
			s.Uncommitted.Deleted += f.Deleted
		}
		if f.Untracked {
			s.UntrackedCount++
			// Do not read untracked bodies on refresh. FileDiff confines selected reads.
			info, err := os.Lstat(filepath.Join(s.Target.WorktreePath, f.Path))
			if err != nil {
				s.warn(fmt.Sprintf("untracked stat: %s", err))
			} else {
				extra = append(extra, f.Path, strconv.FormatInt(info.Size(), 10), strconv.FormatInt(info.ModTime().UnixNano(), 10))
			}
		}
	}
	s.Files = files
	s.Signature = snapshotSignature(s, allFiles, extra...)
	s.Availability = AvailabilityReady
	return s, ctx.Err()
}
