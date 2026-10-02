package tui

import (
	"context"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/iamseth/tao/internal/monitor"
	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/internal/term"
	"github.com/iamseth/tao/internal/theme"
)

func TestChangesTickerLoop(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ticker := &fakeTicker{channel: make(chan time.Time, 1)}
	output := &recordingWriter{writes: make(chan string, 64)}
	reader, writer := io.Pipe()
	defer func() { _ = reader.Close() }()
	defer func() { _ = writer.Close() }()
	starts := make(chan struct{}, 10)
	release := make(chan struct{}, 10)
	row := monitor.Row{RepositoryID: "repo", PlanID: "one", PlanDir: "/one", Status: plan.StatusPlanned}
	a := App{Input: reader, Output: output, Terminal: &fakeTerminal{size: term.Size{Width: 120, Height: 30}}, Ticker: ticker, Now: func() time.Time { return time.Unix(200, 0) }, Collector: &fakeCollector{snapshots: []monitor.Snapshot{{Rows: []monitor.Row{row}}}}, Details: &fakeDetailRepository{detail: &plan.PlanDetail{}}, Changes: DetailChangesFuncs{SnapshotFunc: func(ctx context.Context, _ *plan.PlanDetail, _ string) (DetailChangesSnapshot, error) {
		starts <- struct{}{}
		select {
		case <-release:
			return DetailChangesSnapshot{Availability: "ready", Warnings: []string{"snapshot landed"}}, nil
		case <-ctx.Done():
			return DetailChangesSnapshot{}, ctx.Err()
		}
	}}}
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()
	wait := func(text string) {
		t.Helper()
		for {
			if strings.Contains(waitForFrame(t, output.writes), text) {
				return
			}
		}
	}
	waitForFrame(t, output.writes)
	ticker.channel <- time.Unix(100, 0)
	waitForFrame(t, output.writes)
	if len(starts) != 0 {
		t.Fatal("dashboard polled Changes")
	}
	if _, err := fmt.Fprint(writer, "\r\t\t\t"); err != nil {
		t.Fatal(err)
	}
	receiveChanges(t, starts)
	release <- struct{}{}
	wait("snapshot landed")
	for _, sec := range []int64{102, 104} {
		ticker.channel <- time.Unix(sec, 0)
		receiveChanges(t, starts)
		// Snapshot stays visible while collection is held behind this barrier.
		frame := waitForFrame(t, output.writes)
		if !strings.Contains(frame, "snapshot landed") || strings.Contains(strings.ToLower(frame), "updating") {
			t.Fatalf("background refresh should retain content quietly: %q", frame)
		}
		release <- struct{}{}
		wait("snapshot landed")
	}
	if _, err := fmt.Fprint(writer, "q"); err != nil {
		t.Fatal(err)
	}
	if err := receiveChanges(t, done); err != nil {
		t.Fatal(err)
	}
}

func TestChangesVisibleTickFloor(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	starts := make(chan context.Context, 10)
	release := make(chan struct{}, 10)
	a := App{Changes: DetailChangesFuncs{SnapshotFunc: func(ctx context.Context, _ *plan.PlanDetail, _ string) (DetailChangesSnapshot, error) {
		starts <- ctx
		select {
		case <-release:
			return DetailChangesSnapshot{Availability: "ready"}, nil
		case <-ctx.Done():
			return DetailChangesSnapshot{}, ctx.Err()
		}
	}}}
	s := loopState{detail: &detailState{ctx: ctx, plan: &plan.PlanDetail{}, activeTab: detailTabChanges}}
	defer s.detail.cancelChanges()
	tick := time.Unix(100, 0)
	a.refreshDetailChanges(&s, tick)
	receiveChanges(t, starts)
	first := s.changesUpdates()
	a.refreshDetailChanges(&s, tick.Add(1500*time.Millisecond))
	if s.changesUpdates() != first {
		t.Fatal("in-flight request replaced")
	}
	release <- struct{}{}
	a.acceptDetailChanges(&s, receiveChanges(t, first))
	// Delivered tick time, not completion time or the skipped in-flight tick.
	a.refreshDetailChanges(&s, tick.Add(2*time.Second))
	receiveChanges(t, starts)
	release <- struct{}{}
	a.acceptDetailChanges(&s, receiveChanges(t, s.changesUpdates()))
	a.refreshDetailChanges(&s, tick.Add(2500*time.Millisecond))
	if s.changesUpdates() != nil {
		t.Fatal("within-floor tick loaded")
	}
	a.handleChangesKey(&s, term.KeyEvent{Key: term.KeyRune, Rune: 'u'})
	old := receiveChanges(t, starts)
	s.detail.changes.Snapshot.Scope = DetailChangesScopeWorktree
	s.detail.changes.Pinned = true
	a.handleChangesKey(&s, term.KeyEvent{Key: term.KeyRune, Rune: 'w'})
	receiveChanges(t, starts)
	if old.Err() == nil || s.detail.requestedScope != DetailChangesScopeBranch || s.detail.changes.Pinned {
		t.Fatal("scope did not cancel/reset")
	}
	s.detail.activeTab = detailTabOverview
	a.syncDetailChanges(&s)
	a.refreshDetailChanges(&s, tick.Add(4*time.Second))
	if s.changesUpdates() != nil {
		t.Fatal("inactive tab loaded")
	}
	s.detail = nil
	a.refreshDetailChanges(&s, tick.Add(6*time.Second))
}

func TestChangesRefreshGitErrorRetainsReady(t *testing.T) {
	s := loopState{detail: &detailState{changes: DetailChangesModel{Status: "ready", Snapshot: DetailChangesSnapshot{Signature: "good"}, Diff: DetailFileDiff{Path: "a"}}}}
	a := App{}
	a.acceptDetailChanges(&s, changesUpdate{snapshot: DetailChangesSnapshot{Availability: "git_error", Reason: "busy"}})
	if s.detail.changes.Status != "ready" || s.detail.changes.Snapshot.Signature != "good" || s.detail.changes.Diff.Path != "a" || s.detail.changes.RefreshError != "busy" {
		t.Fatalf("lost last good: %+v", s.detail.changes)
	}
	a.acceptDetailChanges(&s, changesUpdate{snapshot: DetailChangesSnapshot{Availability: "ready", Signature: "good"}})
	if s.detail.changes.RefreshError != "" {
		t.Fatal("error not cleared")
	}
}

func TestChangesForceRejectsOldDiffAndReloadsEqualSignature(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	snapshot := DetailChangesSnapshot{Availability: "ready", Signature: "same", Files: []DetailChangedFile{{Path: "a"}}}
	a := App{Changes: DetailChangesFuncs{SnapshotFunc: func(context.Context, *plan.PlanDetail, string) (DetailChangesSnapshot, error) { return snapshot, nil }, FileDiffFunc: func(_ context.Context, s DetailChangesSnapshot, p string) (DetailFileDiff, error) {
		return DetailFileDiff{Path: p, Signature: s.Signature}, nil
	}}}
	s := loopState{detail: &detailState{ctx: ctx, plan: &plan.PlanDetail{}, activeTab: detailTabChanges, changes: DetailChangesModel{Snapshot: snapshot}}}
	defer s.detail.cancelChanges()
	a.startDetailFileDiff(&s, true)
	old := receiveChanges(t, s.fileDiffUpdates())
	a.handleChangesKey(&s, term.KeyEvent{Key: term.KeyRune, Rune: 'u'})
	a.acceptDetailFileDiff(&s, old)
	if s.detail.changes.Diff.Path != "" {
		t.Fatal("force accepted canceled old diff")
	}
	a.acceptDetailChanges(&s, receiveChanges(t, s.changesUpdates()))
	if s.fileDiffUpdates() == nil {
		t.Fatal("force failed to reload equal-signature diff")
	}
	current := receiveChanges(t, s.fileDiffUpdates())
	bad := current
	bad.signature = "old"
	a.acceptDetailFileDiff(&s, bad)
	bad = current
	bad.path = "other"
	a.acceptDetailFileDiff(&s, bad)
	if s.detail.changes.Diff.Path != "" {
		t.Fatal("signature/path guard failed")
	}
	a.acceptDetailFileDiff(&s, current)
	if s.detail.changes.Diff.Path != "a" {
		t.Fatal("current diff rejected")
	}
}

func TestChangesRefreshGrowthPin(t *testing.T) {
	s := loopState{size: term.Size{Width: 120, Height: 40}, detail: &detailState{activeTab: detailTabChanges, changesLoadState: changesLoadState{selectedPath: "a"}, changes: DetailChangesModel{Focus: DetailChangesFocusDiff, Snapshot: DetailChangesSnapshot{Signature: "s"}, Diff: DetailFileDiff{Path: "a", Lines: make([]DetailDiffLine, 10)}}}}
	a := App{}
	land := func(n int) {
		a.acceptDetailFileDiff(&s, fileDiffUpdate{path: "a", signature: "s", diff: DetailFileDiff{Path: "a", Lines: make([]DetailDiffLine, n)}})
	}
	land(200)
	if s.detail.changes.DiffOffset != 0 {
		t.Fatal("unpinned growth moved")
	}
	a.handleChangesKey(&s, term.KeyEvent{Key: term.KeyRune, Rune: 'G'})
	land(300)
	if !s.detail.changes.Pinned || s.detail.changes.DiffOffset != s.detail.maxOffset(detailTabChanges, s.size) {
		t.Fatal("pin did not follow")
	}
	a.handleChangesKey(&s, term.KeyEvent{Key: term.KeyArrowUp})
	offset := s.detail.changes.DiffOffset
	land(400)
	if s.detail.changes.Pinned || s.detail.changes.DiffOffset != offset {
		t.Fatal("upward did not unpin")
	}
	land(10)
	a.handleChangesKey(&s, term.KeyEvent{Key: term.KeyRune, Rune: 'G'})
	land(200)
	if !s.detail.changes.Pinned || s.detail.changes.DiffOffset != s.detail.maxOffset(detailTabChanges, s.size) {
		t.Fatal("explicit G on fitting diff did not pin")
	}
}

func TestChangesRefreshMetadataAndSelection(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a := App{ChangesWait: func(context.Context, time.Duration) bool { return true }, Changes: DetailChangesFuncs{FileDiffFunc: func(_ context.Context, s DetailChangesSnapshot, p string) (DetailFileDiff, error) {
		return DetailFileDiff{Path: p, Signature: s.Signature, Lines: make([]DetailDiffLine, 100)}, nil
	}}}
	snapshot := DetailChangesSnapshot{Availability: "ready", Signature: "same", Files: []DetailChangedFile{{Path: "a"}, {Path: "b"}}}
	s := loopState{size: term.Size{Width: 120, Height: 30}, detail: &detailState{ctx: ctx, plan: &plan.PlanDetail{}, activeTab: detailTabChanges, changesLoadState: changesLoadState{selectedPath: "b"}, changes: DetailChangesModel{Status: "ready", Snapshot: snapshot, FileIndex: 1, DiffStatus: "ready", Diff: DetailFileDiff{Path: "b", Signature: "same", Lines: make([]DetailDiffLine, 100)}, DiffOffset: 20}}}
	defer s.detail.cancelChanges()
	snapshot.Review = DetailReviewParity{Recorded: true, BaseMatches: true, HeadMatches: true}
	snapshot.CollectedAt = time.Unix(200, 0)
	snapshot.ActiveOperation = "reopen"
	snapshot.RebaseIntent = true
	snapshot.Warnings = []string{"new warning"}
	a.acceptDetailChanges(&s, changesUpdate{snapshot: snapshot})
	m := s.detail.changes
	if m.FileIndex != 1 || m.DiffOffset != 20 || s.fileDiffUpdates() != nil || m.UpdatedAt != snapshot.CollectedAt {
		t.Fatal("equal signature moved reader")
	}
	badges := changesBadges(m.Snapshot)
	for _, want := range []string{"review matches committed head", "reopen", "rebase", "new warning"} {
		if !strings.Contains(badges, want) {
			t.Fatalf("missing %q: %s", want, badges)
		}
	}
	s.detail.changes.Pinned = true
	s.detail.changes.DiffOffset = s.detail.maxOffset(detailTabChanges, s.size)
	snapshot.Review.Superseded = true
	a.acceptDetailChanges(&s, changesUpdate{snapshot: snapshot})
	if !s.detail.changes.Pinned || !strings.Contains(changesBadges(s.detail.changes.Snapshot), "review superseded") || s.fileDiffUpdates() != nil {
		t.Fatal("equal signature lost pin or reopen metadata")
	}
	s.detail.changes.Pinned = false
	s.detail.changes.DiffOffset = 20
	snapshot.Signature = "changed"
	snapshot.Files = []DetailChangedFile{{Path: "b"}, {Path: "a"}}
	a.acceptDetailChanges(&s, changesUpdate{snapshot: snapshot})
	if s.detail.changes.FileIndex != 0 || s.detail.changes.Diff.Path != "b" || s.detail.changes.DiffOffset != 20 {
		t.Fatal("changed signature lost selected stale diff")
	}
	if got := changesDiffRow(s.detail.changes, 0, 80, theme.Palette{}); strings.Contains(got, "Loading") {
		t.Fatal("stale diff replaced by placeholder")
	}
	old := receiveChanges(t, s.fileDiffUpdates())
	snapshot.Signature = "removed"
	snapshot.Files = []DetailChangedFile{{Path: "a"}}
	a.acceptDetailChanges(&s, changesUpdate{snapshot: snapshot})
	a.acceptDetailFileDiff(&s, old)
	if s.detail.selectedPath != "a" || s.detail.changes.Diff.Path != "" || s.detail.changes.DiffOffset != 0 {
		t.Fatal("removed file retained stale content")
	}
	a.acceptDetailFileDiff(&s, receiveChanges(t, s.fileDiffUpdates()))
	// A present status without a present diff must not suppress a missing load.
	s.detail.changes.Diff = DetailFileDiff{}
	a.acceptDetailChanges(&s, changesUpdate{snapshot: snapshot})
	if s.fileDiffUpdates() == nil {
		t.Fatal("absent diff was not requested")
	}
	a.acceptDetailFileDiff(&s, receiveChanges(t, s.fileDiffUpdates()))
	s.detail.changes.Pinned = true
	snapshot.Signature = "empty"
	snapshot.Files = nil
	a.acceptDetailChanges(&s, changesUpdate{snapshot: snapshot})
	if s.detail.changes.Pinned || s.detail.changes.Diff.Path != "" {
		t.Fatal("empty selection retained pin/diff")
	}
}
