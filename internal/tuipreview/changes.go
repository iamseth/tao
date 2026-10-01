package tuipreview

import (
	"context"
	"time"

	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/internal/tui"
)

// NewChangesLoader captures isolated, deterministic fixtures; it never probes Git.
func (s Scenario) NewChangesLoader() tui.DetailChangesLoader {
	fixtures := make(map[string]PlanFixture, len(s.Plans))
	for _, f := range s.Plans {
		copyFixture := f
		if f.Changes != nil {
			snapshot := cloneChanges(*f.Changes)
			snapshot.PlanID = f.Detail.State.Plan.ID
			copyFixture.Changes = &snapshot
		}
		copyFixture.Diffs = make(map[string]tui.DetailFileDiff, len(f.Diffs))
		for path, d := range f.Diffs {
			copyFixture.Diffs[path] = cloneDiff(d)
		}
		fixtures[f.Detail.Dir] = copyFixture
	}
	return tui.DetailChangesFuncs{
		SnapshotFunc: func(ctx context.Context, p *plan.PlanDetail, scope string) (tui.DetailChangesSnapshot, error) {
			if err := ctx.Err(); err != nil {
				return tui.DetailChangesSnapshot{}, err
			}
			if p != nil {
				if f, ok := fixtures[p.Dir]; ok && f.Changes != nil {
					result := cloneChanges(*f.Changes)
					if scope != "" {
						result.Scope = scope
					}
					return result, nil
				}
			}
			return tui.DetailChangesSnapshot{Availability: "unavailable", Reason: "Changes fixture unavailable"}, nil
		},
		FileDiffFunc: func(ctx context.Context, snapshot tui.DetailChangesSnapshot, path string) (tui.DetailFileDiff, error) {
			if err := ctx.Err(); err != nil {
				return tui.DetailFileDiff{}, err
			}
			for _, f := range fixtures {
				if f.Changes != nil && f.Changes.PlanID == snapshot.PlanID {
					if d, ok := f.Diffs[path]; ok {
						return cloneDiff(d), nil
					}
				}
			}
			return tui.DetailFileDiff{Path: path, Lines: []tui.DetailDiffLine{{Kind: tui.DetailDiffMeta, Text: "Diff fixture unavailable"}}}, nil
		},
	}
}
func cloneChanges(s tui.DetailChangesSnapshot) tui.DetailChangesSnapshot {
	s.Files = append([]tui.DetailChangedFile(nil), s.Files...)
	s.Warnings = append([]string(nil), s.Warnings...)
	if s.Merged != nil {
		v := *s.Merged
		s.Merged = &v
	}
	return s
}
func cloneDiff(d tui.DetailFileDiff) tui.DetailFileDiff {
	d.Lines = append([]tui.DetailDiffLine(nil), d.Lines...)
	d.Hunks = append([]int(nil), d.Hunks...)
	return d
}
func mixedChangesFixture(now time.Time) (tui.DetailChangesSnapshot, map[string]tui.DetailFileDiff) {
	files := []tui.DetailChangedFile{
		{Path: "internal/example.go", Display: "internal/example.go", Status: 'M', Added: 2, Deleted: 1, Uncommitted: true},
		{Path: "new.go", Display: "new.go", Status: 'A', Added: 3},
		{Path: "old.go", Display: "old.go", Status: 'D', Deleted: 4},
		{Path: "scratch.txt", Display: "scratch.txt", Status: '?', Untracked: true, Uncommitted: true, Added: 1},
		{Path: "image.png", Display: "image.png", Status: 'M', Binary: true},
	}
	s := tui.DetailChangesSnapshot{Scope: tui.DetailChangesScopeWorktree, Separate: true, Branch: "feature/example", Strategy: "isolated", Availability: "ready", Signature: "fixture-v1", CollectedAt: now,
		Base: tui.DetailChangesBase{SHA: "0123456789", Source: "live_merge_base", DefaultBranch: "main"}, Head: "abcdef0123", DefaultHead: "0123456789", Files: files, Total: tui.DetailChangesStat{Files: 5, Added: 6, Deleted: 5}, Uncommitted: tui.DetailChangesStat{Files: 2, Added: 3, Deleted: 1}, Dirty: true, UntrackedCount: 1,
		Review: tui.DetailReviewParity{Recorded: true, Verdict: "approve", BaseMatches: true, HeadMatches: true}, Warnings: []string{"read-only preview"}}
	diffs := make(map[string]tui.DetailFileDiff)
	for _, f := range files {
		d := tui.DetailFileDiff{Path: f.Path, Signature: s.Signature, Binary: f.Binary}
		if !f.Binary {
			d.Lines = []tui.DetailDiffLine{{Kind: tui.DetailDiffHunk, Text: "@@ -1 +1,2 @@"}, {Kind: tui.DetailDiffDel, Text: "-old content"}, {Kind: tui.DetailDiffAdd, Text: "+ content"}, {Kind: tui.DetailDiffAdd, Text: "+ updated content"}}
			d.Hunks = []int{0}
		}
		diffs[f.Path] = d
	}
	return s, diffs
}
