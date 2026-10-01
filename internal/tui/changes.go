package tui

import (
	"context"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/iamseth/tao/internal/plan"
)

// Changes projections are advisory display data, never review or lifecycle authority.
// Raw paths and signatures are lookup identities and must not be painted.
type DetailChangesLoader interface {
	Snapshot(context.Context, *plan.PlanDetail, string) (DetailChangesSnapshot, error)
	FileDiff(context.Context, DetailChangesSnapshot, string) (DetailFileDiff, error)
}

type DetailChangesFuncs struct {
	SnapshotFunc func(context.Context, *plan.PlanDetail, string) (DetailChangesSnapshot, error)
	FileDiffFunc func(context.Context, DetailChangesSnapshot, string) (DetailFileDiff, error)
}

func (f DetailChangesFuncs) Snapshot(ctx context.Context, p *plan.PlanDetail, scope string) (DetailChangesSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return DetailChangesSnapshot{}, err
	}
	if f.SnapshotFunc == nil {
		return DetailChangesSnapshot{Availability: "unavailable", Reason: "Changes loader unavailable"}, nil
	}
	return f.SnapshotFunc(ctx, p, scope)
}
func (f DetailChangesFuncs) FileDiff(ctx context.Context, s DetailChangesSnapshot, path string) (DetailFileDiff, error) {
	if err := ctx.Err(); err != nil {
		return DetailFileDiff{}, err
	}
	if f.FileDiffFunc == nil {
		return DetailFileDiff{Path: path, Lines: []DetailDiffLine{{Kind: DetailDiffMeta, Text: "Diff unavailable"}}}, nil
	}
	return f.FileDiffFunc(ctx, s, path)
}

const (
	DetailChangesScopeBranch   = "branch"
	DetailChangesScopeWorktree = "worktree"
	DetailChangesFocusFiles    = "files"
	DetailChangesFocusDiff     = "diff"
	detailChangesMaxFiles      = 400
	detailChangesMaxLines      = 4000
	detailChangesMaxRunes      = 1024
)

type DetailChangedFile struct {
	Path, OldPath, Display                           string
	Status                                           byte
	Added, Deleted                                   int
	Binary, Untracked, Uncommitted, RevertsCommitted bool
}
type DetailReviewParity struct {
	Recorded                             bool
	Verdict, Base, Head                  string
	BaseMatches, HeadMatches, Superseded bool
}
type DetailChangesStat struct{ Files, Added, Deleted int }
type DetailChangesBase struct{ SHA, Source, DefaultBranch, PlanBranch string }
type DetailChangesSnapshot struct {
	PlanID, RepoRoot, WorktreePath, Branch, Strategy string
	Separate, BranchIsNew                            bool
	Scope                                            string
	Base                                             DetailChangesBase
	Head, DefaultHead                                string
	ActiveOperation                                  string
	RebaseIntent, WorktreeMissing, Dirty             bool
	Merged                                           *bool
	Review                                           DetailReviewParity
	Files                                            []DetailChangedFile
	Total, Uncommitted                               DetailChangesStat
	UntrackedCount                                   int
	FilesTruncated                                   bool
	Signature                                        string
	Warnings                                         []string
	Availability, Reason                             string
	CollectedAt                                      time.Time
}
type DetailDiffLineKind uint8

const (
	DetailDiffContext DetailDiffLineKind = iota
	DetailDiffAdd
	DetailDiffDel
	DetailDiffHunk
	DetailDiffFileHeader
	DetailDiffMeta
	DetailDiffNoNewline
	DetailDiffMarker
)

type DetailDiffLine struct {
	Kind DetailDiffLineKind
	Text string
}
type DetailFileDiff struct {
	Path, Signature   string
	Binary, Truncated bool
	Lines             []DetailDiffLine
	Hunks             []int
}
type DetailChangesModel struct {
	Status                            string
	Snapshot                          DetailChangesSnapshot
	Diff                              DetailFileDiff
	DiffStatus, Focus, Scope          string
	FileIndex, ListOffset, DiffOffset int
	Pinned, Zoom                      bool
	RefreshError                      string
	UpdatedAt                         time.Time
	Updating                          bool
}

func boundedDetailChangesModel(m DetailChangesModel) DetailChangesModel {
	m.Snapshot = boundedDetailChanges(m.Snapshot)
	m.Diff = boundedDetailFileDiff(m.Diff)
	m.RefreshError = changesText(m.RefreshError, 240)
	m.Scope = changesText(m.Scope, 240)
	return m
}

// These projection boundaries run before caching, not in the renderer. They also
// detach loader-owned slices so asynchronous producers cannot mutate a frame.
func boundedDetailChanges(s DetailChangesSnapshot) DetailChangesSnapshot {
	s.FilesTruncated = s.FilesTruncated || len(s.Files) > detailChangesMaxFiles
	s.Files = append([]DetailChangedFile(nil), s.Files[:min(len(s.Files), detailChangesMaxFiles)]...)
	for i := range s.Files {
		f := &s.Files[i]
		if f.Display == "" {
			f.Display = f.Path
		}
		f.Display = changesText(f.Display, detailChangesMaxRunes)
		f.Added = max(f.Added, 0)
		f.Deleted = max(f.Deleted, 0)
		if !strings.ContainsRune("AMDUT?!", rune(f.Status)) {
			f.Status = '?'
		}
	}
	for _, p := range []*string{&s.PlanID, &s.Branch, &s.Strategy, &s.Scope, &s.Base.SHA, &s.Base.Source, &s.Base.DefaultBranch, &s.Base.PlanBranch, &s.Head, &s.DefaultHead, &s.ActiveOperation, &s.Review.Verdict, &s.Review.Base, &s.Review.Head, &s.Availability, &s.Reason} {
		*p = changesText(*p, 240)
	}
	s.Warnings = append([]string(nil), s.Warnings[:min(len(s.Warnings), 16)]...)
	for i := range s.Warnings {
		s.Warnings[i] = changesText(s.Warnings[i], 240)
	}
	if s.Merged != nil {
		v := *s.Merged
		s.Merged = &v
	}
	return s
}
func boundedDetailFileDiff(d DetailFileDiff) DetailFileDiff {
	d.Truncated = d.Truncated || len(d.Lines) > detailChangesMaxLines
	d.Lines = append([]DetailDiffLine(nil), d.Lines[:min(len(d.Lines), detailChangesMaxLines)]...)
	d.Hunks = nil
	for i := range d.Lines {
		if utf8.RuneCountInString(d.Lines[i].Text) > detailChangesMaxRunes {
			d.Truncated = true
		}
		d.Lines[i].Text = changesText(d.Lines[i].Text, detailChangesMaxRunes)
		if d.Lines[i].Kind == DetailDiffHunk {
			d.Hunks = append(d.Hunks, i)
		}
	}
	return d
}
func changesText(s string, limit int) string {
	var b strings.Builder
	n := 0
	for _, r := range s {
		if n >= limit {
			break
		}
		if r == '\t' {
			spaces := min(4, limit-n)
			b.WriteString(strings.Repeat(" ", spaces))
			n += spaces
			continue
		}
		n++
		if r == '\ufffd' || unicode.IsControl(r) || unicode.In(r, unicode.Cf) {
			b.WriteRune(' ')
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}
