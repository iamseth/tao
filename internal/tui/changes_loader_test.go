package tui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/iamseth/tao/internal/monitor"
	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/internal/term"
)

func receiveChanges[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(3 * time.Second):
		t.Fatal("loader did not settle")
		var zero T
		return zero
	}
}

func TestChangesDebounceCancellationAndStaleResults(t *testing.T) {
	type waiting struct {
		ctx     context.Context
		release chan struct{}
	}
	waits := make(chan waiting, 20)
	calls := make(chan string, 20)
	a := App{ChangesWait: func(ctx context.Context, _ time.Duration) bool {
		w := waiting{ctx, make(chan struct{})}
		waits <- w
		select {
		case <-ctx.Done():
			return false
		case <-w.release:
			return true
		}
	}, Changes: DetailChangesFuncs{FileDiffFunc: func(_ context.Context, s DetailChangesSnapshot, p string) (DetailFileDiff, error) {
		calls <- p
		return DetailFileDiff{Path: p, Signature: s.Signature}, nil
	}}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := loopState{detail: &detailState{ctx: ctx, activeTab: detailTabChanges, plan: &plan.PlanDetail{}}}
	for i := 0; i < 12; i++ {
		s.detail.changes.Snapshot.Files = append(s.detail.changes.Snapshot.Files, DetailChangedFile{Path: fmt.Sprint(i)})
	}
	var previous waiting
	for i := 0; i < 10; i++ {
		a.moveChangesFile(&s, 1)
		w := receiveChanges(t, waits)
		if previous.ctx != nil && previous.ctx.Err() == nil {
			t.Fatal("previous request not canceled")
		}
		previous = w
	}
	close(previous.release)
	u := receiveChanges(t, s.fileDiffUpdates())
	a.acceptDetailFileDiff(&s, u)
	if receiveChanges(t, calls) != "10" || len(calls) != 0 {
		t.Fatal("unsettled cursor invoked loader")
	}
	before := s.detail.changes.Diff
	a.acceptDetailFileDiff(&s, fileDiffUpdate{generation: u.generation - 1, path: "10", diff: DetailFileDiff{Path: "stale"}})
	if s.detail.changes.Diff.Path != before.Path {
		t.Fatal("accepted stale diff")
	}
	a.startDetailFileDiff(&s, false)
	w := receiveChanges(t, waits)
	s.detail.activeTab = detailTabOverview
	a.syncDetailChanges(&s)
	if w.ctx.Err() == nil || s.fileDiffUpdates() != nil {
		t.Fatal("tab departure retained load")
	}
	s.detail.activeTab = detailTabChanges
	a.startDetailFileDiff(&s, false)
	w = receiveChanges(t, waits)
	s.closeDetail()
	if w.ctx.Err() == nil {
		t.Fatal("close retained load")
	}
}

func TestChangesBoundsPanicAndLastGood(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := loopState{detail: &detailState{ctx: ctx, activeTab: detailTabChanges, plan: &plan.PlanDetail{}}}
	a := App{Changes: DetailChangesFuncs{SnapshotFunc: func(context.Context, *plan.PlanDetail, string) (DetailChangesSnapshot, error) { panic("unsafe\\x1b") }}}
	a.syncDetailChanges(&s)
	a.acceptDetailChanges(&s, receiveChanges(t, s.changesUpdates()))
	if s.detail.changes.RefreshError != "changes loader panicked" {
		t.Fatal("panic not contained")
	}
	s.detail.changes.Status = "ready"
	s.detail.changes.Snapshot.Signature = "last-good"
	a.Changes = DetailChangesFuncs{SnapshotFunc: func(context.Context, *plan.PlanDetail, string) (DetailChangesSnapshot, error) {
		return DetailChangesSnapshot{}, errors.New("bad\x1b\n")
	}}
	a.syncDetailChanges(&s)
	a.acceptDetailChanges(&s, receiveChanges(t, s.changesUpdates()))
	if s.detail.changes.Snapshot.Signature != "last-good" || s.detail.changes.RefreshError != "bad  " {
		t.Fatal("lost snapshot or unsafe error")
	}
	raw := DetailChangesSnapshot{Files: make([]DetailChangedFile, 450), Warnings: make([]string, 30)}
	for i := range raw.Files {
		raw.Files[i] = DetailChangedFile{Path: "raw\x1b", Display: strings.Repeat("x", 2000)}
	}
	a.Changes = DetailChangesFuncs{SnapshotFunc: func(context.Context, *plan.PlanDetail, string) (DetailChangesSnapshot, error) { return raw, nil }, FileDiffFunc: func(context.Context, DetailChangesSnapshot, string) (DetailFileDiff, error) {
		return DetailFileDiff{Lines: make([]DetailDiffLine, 5000)}, nil
	}}
	a.syncDetailChanges(&s)
	a.acceptDetailChanges(&s, receiveChanges(t, s.changesUpdates()))
	if len(s.detail.changes.Snapshot.Files) != 400 || len(s.detail.changes.Snapshot.Warnings) != 16 || len(s.detail.changes.Snapshot.Files[0].Display) != 1024 {
		t.Fatal("snapshot bounds")
	}
	a.acceptDetailFileDiff(&s, receiveChanges(t, s.fileDiffUpdates()))
	if len(s.detail.changes.Diff.Lines) != 4000 || !s.detail.changes.Diff.Truncated {
		t.Fatal("diff bounds")
	}
	a.Changes = DetailChangesFuncs{FileDiffFunc: func(context.Context, DetailChangesSnapshot, string) (DetailFileDiff, error) { panic("bad") }}
	a.startDetailFileDiff(&s, true)
	a.acceptDetailFileDiff(&s, receiveChanges(t, s.fileDiffUpdates()))
	if s.detail.changes.DiffStatus != "error" || len(s.detail.changes.Diff.Lines) != 4000 {
		t.Fatal("diff panic lost cached data")
	}
}

func TestChangesEqualSignatureKeepsPendingDiffAndFreshMetadata(t *testing.T) {
	release := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a := App{ChangesWait: func(ctx context.Context, _ time.Duration) bool {
		select {
		case <-release:
			return true
		case <-ctx.Done():
			return false
		}
	}, Changes: DetailChangesFuncs{FileDiffFunc: func(_ context.Context, s DetailChangesSnapshot, p string) (DetailFileDiff, error) {
		return DetailFileDiff{Path: p, Signature: s.Signature}, nil
	}}}
	snapshot := DetailChangesSnapshot{Availability: "ready", Signature: "same", Head: "old", Files: []DetailChangedFile{{Path: "a"}}}
	s := loopState{detail: &detailState{ctx: ctx, activeTab: detailTabChanges, plan: &plan.PlanDetail{}, changes: DetailChangesModel{Status: "ready", Snapshot: snapshot}}}
	a.startDetailFileDiff(&s, false)
	pending := s.fileDiffUpdates()
	snapshot.Head = "new"
	snapshot.CollectedAt = time.Unix(100, 0)
	s.detail.changesGeneration++
	a.acceptDetailChanges(&s, changesUpdate{generation: s.detail.changesGeneration, snapshot: snapshot})
	if s.fileDiffUpdates() != pending || s.detail.changes.Snapshot.Head != "new" || s.detail.changes.UpdatedAt != snapshot.CollectedAt {
		t.Fatal("equal signature orphaned diff or discarded metadata")
	}
	close(release)
	a.acceptDetailFileDiff(&s, receiveChanges(t, pending))
	if s.detail.changes.DiffStatus != "ready" {
		t.Fatal("snapshot generation rejected independent diff")
	}
}

func TestChangesRunExitCancelsLoader(t *testing.T) {
	starts := make(chan context.Context, 1)
	row := monitor.Row{RepositoryID: "repo", PlanID: "one", PlanDir: "/one", Status: plan.StatusPlanned}
	a := App{Input: strings.NewReader("\r\t\t\tq"), Output: io.Discard, Terminal: &fakeTerminal{size: term.Size{Width: 120, Height: 30}}, Ticker: &fakeTicker{}, Collector: &fakeCollector{snapshots: []monitor.Snapshot{{Rows: []monitor.Row{row}}}}, Details: &fakeDetailRepository{detail: &plan.PlanDetail{}}, Changes: DetailChangesFuncs{SnapshotFunc: func(ctx context.Context, _ *plan.PlanDetail, _ string) (DetailChangesSnapshot, error) {
		starts <- ctx
		<-ctx.Done()
		return DetailChangesSnapshot{}, ctx.Err()
	}}}
	if err := a.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx := receiveChanges(t, starts)
	if ctx.Err() == nil {
		t.Fatal("exit left loader running")
	}
}

func TestChangesPlanNavigationCancelsBeforeOpening(t *testing.T) {
	starts := make(chan context.Context, 4)
	a := App{Details: &fakeDetailRepository{detail: &plan.PlanDetail{}}, Changes: DetailChangesFuncs{SnapshotFunc: func(ctx context.Context, _ *plan.PlanDetail, _ string) (DetailChangesSnapshot, error) {
		starts <- ctx
		<-ctx.Done()
		return DetailChangesSnapshot{}, ctx.Err()
	}}}
	rows := []monitor.Row{{RepositoryID: "repo", PlanID: "one", PlanDir: "/one", Status: plan.StatusPlanned}, {RepositoryID: "repo", PlanID: "two", PlanDir: "/two", Status: plan.StatusPlanned}}
	s := loopState{snapshot: monitor.Snapshot{Rows: rows}, size: term.Size{Width: 120, Height: 30}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	defer s.closeDetail()
	a.syncDetailChanges(&s)
	a.openDetailTab(ctx, &s, rows[0], detailTabChanges)
	first := receiveChanges(t, starts)
	s.detail.changes.Focus = DetailChangesFocusDiff
	s.detail.changes.Zoom = true
	s.detail.changes.Pinned = true
	a.movePlanDetail(ctx, &s, 1)
	second := receiveChanges(t, starts)
	if first.Err() == nil || second.Err() != nil || s.detail.activeTab != detailTabChanges || s.detail.changes.Zoom || s.detail.changes.Pinned || s.detail.changes.Focus == DetailChangesFocusDiff {
		t.Fatal("plan navigation did not reset and cancel")
	}
	for range 20 {
		_ = s.detail.maxOffset(detailTabChanges, s.size)
		s.detail.clampOffsets(s.size)
	}
	if len(starts) != 0 {
		t.Fatal("geometry loaded")
	}
	s.closeDetail()
	if second.Err() == nil {
		t.Fatal("close did not cancel snapshot")
	}
}

func TestChangesActivationAndEqualSignature(t *testing.T) {
	calls := make(chan string, 4)
	a := App{Changes: DetailChangesFuncs{
		SnapshotFunc: func(_ context.Context, _ *plan.PlanDetail, scope string) (DetailChangesSnapshot, error) {
			calls <- scope
			return DetailChangesSnapshot{Availability: "ready", Signature: "same", Files: []DetailChangedFile{{Path: "raw\x1b", Display: "unsafe\x1b"}}}, nil
		},
		FileDiffFunc: func(_ context.Context, s DetailChangesSnapshot, path string) (DetailFileDiff, error) {
			return DetailFileDiff{Path: path, Signature: s.Signature, Lines: []DetailDiffLine{{Text: "ok\x1b"}}}, nil
		},
	}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := loopState{detail: &detailState{ctx: ctx, plan: &plan.PlanDetail{}}}
	a.syncDetailChanges(&s)
	if s.changesUpdates() != nil {
		t.Fatal("loaded inactive tab")
	}
	s.detail.activeTab = detailTabChanges
	a.syncDetailChanges(&s)
	a.syncDetailChanges(&s)
	u := <-s.changesUpdates()
	a.acceptDetailChanges(&s, u)
	if len(calls) != 1 {
		t.Fatal("not single flight")
	}
	if s.detail.changes.Snapshot.Files[0].Path != "raw\x1b" || s.detail.changes.Snapshot.Files[0].Display != "unsafe " {
		t.Fatal("projection changed identity or retained escape")
	}
	a.startDetailFileDiff(&s, true)
	d := <-s.fileDiffUpdates()
	a.acceptDetailFileDiff(&s, d)
	if s.detail.changes.Diff.Lines[0].Text != "ok " {
		t.Fatal("unsanitized diff")
	}
	s.detail.changesForce = true
	a.syncDetailChanges(&s)
	a.acceptDetailChanges(&s, <-s.changesUpdates())
	a.acceptDetailFileDiff(&s, <-s.fileDiffUpdates())
	if s.detail.changes.DiffStatus != "ready" {
		t.Fatal("equal signature stranded diff")
	}
}
