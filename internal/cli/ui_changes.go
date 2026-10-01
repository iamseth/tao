package cli

import (
	"context"

	"github.com/iamseth/tao/internal/commandrunner"
	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/internal/plandelta"
	"github.com/iamseth/tao/internal/tui"
)

// The adapter retains no snapshots: raw lookup identity travels with the TUI
// projection, while the TUI's defensive scrub remains the display boundary.
func newUIDetailChangesLoader(runner commandrunner.Runner) tui.DetailChangesLoader {
	collector := plandelta.NewCollector(runner)
	return tui.DetailChangesFuncs{
		SnapshotFunc: func(ctx context.Context, detail *plan.PlanDetail, scope string) (tui.DetailChangesSnapshot, error) {
			s, err := collector.Snapshot(ctx, detail, plandelta.Scope(scope))
			return uiChangesSnapshot(s), err
		},
		FileDiffFunc: func(ctx context.Context, s tui.DetailChangesSnapshot, path string) (tui.DetailFileDiff, error) {
			d, err := collector.FileDiff(ctx, domainChangesSnapshot(s), path)
			return uiChangesFileDiff(d), err
		},
	}
}

func uiChangesSnapshot(s plandelta.Snapshot) tui.DetailChangesSnapshot {
	result := tui.DetailChangesSnapshot{
		PlanID: s.Target.PlanID, RepoRoot: s.Target.RepoRoot, WorktreePath: s.Target.WorktreePath, Branch: s.Target.Branch, Strategy: s.Target.Strategy, Separate: s.Target.Separate, BranchIsNew: s.Target.BranchIsNew,
		Scope: string(s.Scope), Base: tui.DetailChangesBase{SHA: s.Base.SHA, Source: string(s.Base.Source), DefaultBranch: s.Base.DefaultBranch, PlanBranch: s.Base.PlanBranch},
		Head: s.Head, DefaultHead: s.DefaultHead, WorktreeMissing: s.WorktreeMissing, ActiveOperation: s.ActiveOperation, RebaseIntent: s.RebaseIntent, Dirty: s.Dirty, Merged: s.Merged,
		Review: tui.DetailReviewParity{Recorded: s.Review.Recorded, Verdict: s.Review.Verdict, Base: s.Review.Base, Head: s.Review.Head, BaseMatches: s.Review.BaseMatches, HeadMatches: s.Review.HeadMatches, Superseded: s.Review.Superseded},
		Total:  tui.DetailChangesStat{Files: s.Total.Files, Added: s.Total.Added, Deleted: s.Total.Deleted}, Uncommitted: tui.DetailChangesStat{Files: s.Uncommitted.Files, Added: s.Uncommitted.Added, Deleted: s.Uncommitted.Deleted},
		UntrackedCount: s.UntrackedCount, FilesTruncated: s.FilesTruncated, Signature: s.Signature, Warnings: s.Warnings, Availability: string(s.Availability), Reason: s.Reason, CollectedAt: s.CollectedAt,
	}
	for _, f := range s.Files {
		result.Files = append(result.Files, tui.DetailChangedFile{Path: f.Path, OldPath: f.OldPath, Status: f.Status, Added: f.Added, Deleted: f.Deleted, Binary: f.Binary, Untracked: f.Untracked, Uncommitted: f.Uncommitted, RevertsCommitted: f.RevertsCommitted})
	}
	return result
}

func domainChangesSnapshot(s tui.DetailChangesSnapshot) plandelta.Snapshot {
	result := plandelta.Snapshot{
		Target: plandelta.Target{PlanID: s.PlanID, RepoRoot: s.RepoRoot, WorktreePath: s.WorktreePath, Branch: s.Branch, Strategy: s.Strategy, Separate: s.Separate, BranchIsNew: s.BranchIsNew},
		Scope:  plandelta.Scope(s.Scope), Base: plandelta.Base{SHA: s.Base.SHA, Source: plandelta.BaseSource(s.Base.Source), DefaultBranch: s.Base.DefaultBranch, PlanBranch: s.Base.PlanBranch},
		Head: s.Head, DefaultHead: s.DefaultHead, WorktreeMissing: s.WorktreeMissing, ActiveOperation: s.ActiveOperation, RebaseIntent: s.RebaseIntent, Dirty: s.Dirty, Merged: s.Merged,
		Review: plandelta.ReviewParity{Recorded: s.Review.Recorded, Verdict: s.Review.Verdict, Base: s.Review.Base, Head: s.Review.Head, BaseMatches: s.Review.BaseMatches, HeadMatches: s.Review.HeadMatches, Superseded: s.Review.Superseded},
		Total:  plandelta.Stat{Files: s.Total.Files, Added: s.Total.Added, Deleted: s.Total.Deleted}, Uncommitted: plandelta.Stat{Files: s.Uncommitted.Files, Added: s.Uncommitted.Added, Deleted: s.Uncommitted.Deleted},
		UntrackedCount: s.UntrackedCount, FilesTruncated: s.FilesTruncated, Signature: s.Signature, Warnings: s.Warnings, Availability: plandelta.Availability(s.Availability), Reason: s.Reason, CollectedAt: s.CollectedAt,
	}
	for _, f := range s.Files {
		result.Files = append(result.Files, plandelta.FileChange{Path: f.Path, OldPath: f.OldPath, Status: f.Status, Added: f.Added, Deleted: f.Deleted, Binary: f.Binary, Untracked: f.Untracked, Uncommitted: f.Uncommitted, RevertsCommitted: f.RevertsCommitted})
	}
	return result
}

func uiChangesFileDiff(d plandelta.FileDiff) tui.DetailFileDiff {
	result := tui.DetailFileDiff{Path: d.Path, Signature: d.Signature, Binary: d.Binary, Truncated: d.Truncated, Hunks: d.Hunks}
	for _, line := range d.Lines {
		kind := tui.DetailDiffMeta
		switch line.Kind {
		case plandelta.Context:
			kind = tui.DetailDiffContext
		case plandelta.Add:
			kind = tui.DetailDiffAdd
		case plandelta.Del:
			kind = tui.DetailDiffDel
		case plandelta.Hunk:
			kind = tui.DetailDiffHunk
		case plandelta.FileHeader:
			kind = tui.DetailDiffFileHeader
		case plandelta.Meta:
			kind = tui.DetailDiffMeta
		case plandelta.NoNewline:
			kind = tui.DetailDiffNoNewline
		case plandelta.Marker:
			kind = tui.DetailDiffMarker
		}
		result.Lines = append(result.Lines, tui.DetailDiffLine{Kind: kind, Text: line.Text})
	}
	return result
}
