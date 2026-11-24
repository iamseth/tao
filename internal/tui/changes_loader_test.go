package tui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
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
	case <-time.After(30 * time.Second): // Failure backstop, not a behavioral deadline.
		t.Fatal("loader did not settle")
		var zero T
		return zero
	}
}

func TestChangesQueuedDiffIdentityAndSettlement(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a := App{Changes: DetailChangesFuncs{FileDiffFunc: func(_ context.Context, snapshot DetailChangesSnapshot, path string) (DetailFileDiff, error) {
		return DetailFileDiff{Signature: snapshot.Signature, Path: path}, nil
	}}}
	newDetail := func() *detailState {
		return &detailState{ctx: ctx, plan: &plan.PlanDetail{}, activeTab: detailTabChanges, changes: DetailChangesModel{Snapshot: DetailChangesSnapshot{Signature: "same", Files: []DetailChangedFile{{Path: "a"}}}}}
	}
	s := loopState{detail: newDetail()}
	oldDetail := s.detail
	defer oldDetail.cancelChanges()
	a.startDetailFileDiff(&s, true)
	old := receiveChanges(t, s.updates)
	s.detail = newDetail()
	defer s.detail.cancelChanges()
	a.startDetailFileDiff(&s, true)
	if oldDetail.diffGeneration != s.detail.diffGeneration {
		t.Fatal("test requires matching generations")
	}
	if old(&s) || !s.detail.diffPending {
		t.Fatal("old detail settled replacement")
	}
	queued := receiveChanges(t, s.updates)
	a.startDetailFileDiff(&s, true)
	if queued(&s) || !s.detail.diffPending {
		t.Fatal("old request settled replacement")
	}
	if !receiveLoopUpdate(t, &s) || s.detail.diffPending || s.detail.diffCancel != nil || s.detail.changes.Diff.Path != "a" {
		t.Fatal("current request did not settle")
	}
	a.startDetailFileDiff(&s, true)
	mismatched := receiveChanges(t, s.updates)
	s.detail.changes.Snapshot.Signature = "changed"
	if mismatched(&s) || !s.detail.diffPending {
		t.Fatal("signature mismatch applied or settled request")
	}
	s.detail.changes.Snapshot.Signature = "same"
	s.detail.selectedPath = "other"
	if mismatched(&s) || !s.detail.diffPending {
		t.Fatal("path mismatch applied or settled request")
	}
	a.startDetailFileDiff(&s, true)
	ended := receiveChanges(t, s.updates)
	cancel()
	if ended(&s) {
		t.Fatal("expired lifetime applied")
	}
}

func TestChangesDiffCanceledFullQueue(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started := make(chan context.Context, 1)
	returned := make(chan struct{})
	a := App{Changes: DetailChangesFuncs{FileDiffFunc: func(ctx context.Context, _ DetailChangesSnapshot, _ string) (DetailFileDiff, error) {
		started <- ctx
		<-ctx.Done()
		defer close(returned)
		return DetailFileDiff{Path: "abandoned"}, nil
	}}}
	s := loopState{detail: &detailState{ctx: ctx, plan: &plan.PlanDetail{}, activeTab: detailTabChanges, changes: DetailChangesModel{Snapshot: DetailChangesSnapshot{Files: []DetailChangedFile{{Path: "a"}}}}}}
	queue := s.ensureUpdates()
	for range cap(queue) {
		queue <- func(*loopState) bool { return false }
	}
	a.startDetailFileDiff(&s, true)
	request := receiveChanges(t, started)
	s.detail.cancelChanges()
	receiveChanges(t, returned)
	if request.Err() == nil || s.detail.diffPending {
		t.Fatal("full-queue request not canceled")
	}
	for range cap(queue) {
		if receiveLoopUpdate(t, &s) {
			t.Fatal("filler requested redraw")
		}
	}
	// A canceled publisher can win a newly available queue slot, but its
	// closure must still be inert while a fresh request settles independently.
	a.Changes = DetailChangesFuncs{FileDiffFunc: func(context.Context, DetailChangesSnapshot, string) (DetailFileDiff, error) {
		return DetailFileDiff{Path: "a"}, nil
	}}
	a.startDetailFileDiff(&s, true)
	for s.detail.diffPending {
		receiveLoopUpdate(t, &s)
	}
	if s.detail.changes.Diff.Path != "a" {
		t.Fatal("canceled publication corrupted replacement")
	}
}

func TestChangesQueuedSnapshotIdentityAndSettlement(t *testing.T) {
	for _, outcome := range []string{"success", "error", "cancel"} {
		t.Run(outcome, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			a := App{Changes: DetailChangesFuncs{SnapshotFunc: func(context.Context, *plan.PlanDetail, string) (DetailChangesSnapshot, error) {
				switch outcome {
				case "error":
					return DetailChangesSnapshot{}, errors.New("failed")
				case "cancel":
					return DetailChangesSnapshot{}, context.Canceled
				default:
					return DetailChangesSnapshot{Availability: "ready", Signature: "current"}, nil
				}
			}}}
			newDetail := func() *detailState {
				return &detailState{ctx: ctx, plan: &plan.PlanDetail{}, activeTab: detailTabChanges}
			}
			s := loopState{detail: newDetail()}
			oldDetail := s.detail
			a.startDetailChanges(&s)
			old := receiveChanges(t, s.updates)
			// Even an uncanceled old detail cannot apply to a reopened detail
			// whose request happens to have the same generation.
			s.detail = newDetail()
			defer oldDetail.cancelChanges()
			defer s.detail.cancelChanges()
			a.startDetailChanges(&s)
			if oldDetail.changesGeneration != s.detail.changesGeneration {
				t.Fatal("test requires matching generations")
			}
			if old(&s) || !s.detail.changesPending || !s.detail.changes.Updating {
				t.Fatal("old detail settled replacement or requested redraw")
			}
			if !receiveLoopUpdate(t, &s) || s.detail.changesPending || s.detail.changes.Updating || s.detail.changesCancel != nil {
				t.Fatal("current result did not settle and redraw")
			}
			a.startDetailChanges(&s)
			queued := receiveChanges(t, s.updates)
			s.detail.changesForce = true
			a.startDetailChanges(&s)
			if queued(&s) || !s.detail.changesPending {
				t.Fatal("forced replacement settled by stale closure")
			}
			replacement := receiveChanges(t, s.updates)
			s.detail.cancelChanges()
			if replacement(&s) || s.detail.changesPending || s.detail.changes.Updating {
				t.Fatal("canceled closure applied or cancellation retained pending state")
			}
			a.startDetailChanges(&s)
			ended := receiveChanges(t, s.updates)
			cancel() // Lifetime guard must work without a generation change.
			if ended(&s) {
				t.Fatal("expired lifetime requested redraw")
			}
		})
	}
}

func TestChangesSnapshotToDiffHandoffWithFullQueue(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a := App{ChangesWait: func(context.Context, time.Duration) bool { return true }, Changes: DetailChangesFuncs{
		SnapshotFunc: func(context.Context, *plan.PlanDetail, string) (DetailChangesSnapshot, error) {
			return DetailChangesSnapshot{Signature: "snapshot", Files: []DetailChangedFile{{Path: "a"}}}, nil
		},
		FileDiffFunc: func(_ context.Context, snapshot DetailChangesSnapshot, path string) (DetailFileDiff, error) {
			return DetailFileDiff{Signature: snapshot.Signature, Path: path}, nil
		},
	}}
	s := loopState{detail: &detailState{ctx: ctx, plan: &plan.PlanDetail{}, activeTab: detailTabChanges}}
	defer s.detail.cancelChanges()
	a.startDetailChanges(&s)
	update := receiveChanges(t, s.updates)
	for range cap(s.updates) {
		s.updates <- func(*loopState) bool { return false }
	}
	if !update(&s) || s.detail.changesPending || s.detail.changes.Updating || !s.detail.diffPending {
		t.Fatal("snapshot did not settle and hand off to diff")
	}
	for range cap(s.updates) {
		if receiveLoopUpdate(t, &s) {
			t.Fatal("filler requested redraw")
		}
	}
	receiveLoopUpdate(t, &s)
	if s.detail.changes.DiffStatus != "ready" || s.detail.changes.Diff.Path != "a" || s.detail.diffPending {
		t.Fatal("diff handoff changed transport or lost result")
	}
}

func TestChangesDebounceCancellationAndStaleResults(t *testing.T) {
	type waiting struct {
		ctx     context.Context
		release chan struct{}
	}
	waits := make(chan waiting, 20)
	calls := make(chan string, 20)
	a := App{ChangesWait: func(ctx context.Context, delay time.Duration) bool {
		if delay != 75*time.Millisecond {
			t.Errorf("debounce = %v", delay)
		}
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
	generation := s.detail.diffGeneration
	receiveLoopUpdate(t, &s)
	if receiveChanges(t, calls) != "10" || len(calls) != 0 {
		t.Fatal("unsettled cursor invoked loader")
	}
	before := s.detail.changes.Diff
	a.acceptDetailFileDiff(&s, fileDiffUpdate{generation: generation - 1, path: "10", diff: DetailFileDiff{Path: "stale"}})
	if s.detail.changes.Diff.Path != before.Path {
		t.Fatal("accepted stale diff")
	}
	a.startDetailFileDiff(&s, false)
	w := receiveChanges(t, waits)
	s.detail.activeTab = detailTabOverview
	a.syncDetailChanges(&s)
	if w.ctx.Err() == nil || s.detail.diffPending {
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
	receiveLoopUpdate(t, &s)
	if s.detail.changes.RefreshError != "changes loader panicked" {
		t.Fatal("panic not contained")
	}
	s.detail.changes.Status = "ready"
	s.detail.changes.Snapshot.Signature = "last-good"
	a.Changes = DetailChangesFuncs{SnapshotFunc: func(context.Context, *plan.PlanDetail, string) (DetailChangesSnapshot, error) {
		return DetailChangesSnapshot{}, errors.New("bad\x1b\n")
	}}
	a.syncDetailChanges(&s)
	receiveLoopUpdate(t, &s)
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
	receiveLoopUpdate(t, &s)
	if len(s.detail.changes.Snapshot.Files) != 400 || len(s.detail.changes.Snapshot.Warnings) != 16 || len(s.detail.changes.Snapshot.Files[0].Display) != 1024 {
		t.Fatal("snapshot bounds")
	}
	receiveLoopUpdate(t, &s)
	if len(s.detail.changes.Diff.Lines) != 4000 || !s.detail.changes.Diff.Truncated {
		t.Fatal("diff bounds")
	}
	a.Changes = DetailChangesFuncs{FileDiffFunc: func(context.Context, DetailChangesSnapshot, string) (DetailFileDiff, error) { panic("bad") }}
	a.startDetailFileDiff(&s, true)
	receiveLoopUpdate(t, &s)
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
	pending := s.detail.diffGeneration
	snapshot.Head = "new"
	snapshot.CollectedAt = time.Unix(100, 0)
	s.detail.changesGeneration++
	a.acceptDetailChanges(&s, changesUpdate{generation: s.detail.changesGeneration, snapshot: snapshot})
	if !s.detail.diffPending || s.detail.diffGeneration != pending || s.detail.changes.Snapshot.Head != "new" || s.detail.changes.UpdatedAt != snapshot.CollectedAt {
		t.Fatal("equal signature orphaned diff or discarded metadata")
	}
	close(release)
	receiveLoopUpdate(t, &s)
	if s.detail.changes.DiffStatus != "ready" {
		t.Fatal("snapshot generation rejected independent diff")
	}
}

// gatedReader serves its first reader to EOF, then blocks until gate closes
// before serving the rest. It lets a scripted input stream wait for a loader to
// start before it delivers the keys that exit the loop. Optional stop releases a
// blocked read during cleanup even if startup never happens.
type gatedReader struct {
	first io.Reader
	gate  <-chan struct{}
	rest  io.Reader
	stop  <-chan struct{}
}

func (r *gatedReader) Read(p []byte) (int, error) {
	if r.first != nil {
		n, err := r.first.Read(p)
		if err == io.EOF {
			r.first = nil
			if n == 0 {
				return r.Read(p)
			}
			return n, nil
		}
		return n, err
	}
	select {
	case <-r.stop:
		return 0, io.EOF
	case <-r.gate:
		return r.rest.Read(p)
	}
}

func TestGatedReaderReleaseAndCancellation(t *testing.T) {
	for _, release := range []bool{false, true} {
		t.Run(fmt.Sprint("release=", release), func(t *testing.T) {
			gate := make(chan struct{})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			r := &gatedReader{first: strings.NewReader("a"), gate: gate, rest: strings.NewReader("q"), stop: ctx.Done()}
			buf := make([]byte, 1)
			if n, err := r.Read(buf); n != 1 || err != nil || string(buf) != "a" {
				t.Fatalf("first read = %d, %v, %q", n, err, buf)
			}
			done := make(chan error, 1)
			go func() {
				_, err := r.Read(buf)
				done <- err
			}()
			select {
			case err := <-done:
				t.Fatalf("held gate returned: %v", err)
			default:
			}
			if release {
				close(gate)
				if err := receiveChanges(t, done); err != nil || string(buf) != "q" {
					t.Fatalf("released read = %v, %q", err, buf)
				}
			} else {
				cancel() // Startup never occurs: cleanup must still unblock Read.
				if err := receiveChanges(t, done); !errors.Is(err, io.EOF) {
					t.Fatalf("canceled read = %v, want EOF", err)
				}
			}
		})
	}
}

func TestChangesRunExitCancelsLoader(t *testing.T) {
	runCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	starts := make(chan context.Context, 1)
	started := make(chan struct{})
	var once sync.Once
	row := monitor.Row{RepositoryID: "repo", PlanID: "one", PlanDir: "/one", Status: plan.StatusPlanned}
	// Quit only after the loader has observably started: exiting earlier cancels the
	// loader context before its goroutine runs, and Snapshot returns on ctx.Err()
	// without ever invoking SnapshotFunc.
	input := &gatedReader{first: strings.NewReader("\r\t\t\t"), gate: started, rest: strings.NewReader("q"), stop: runCtx.Done()}
	a := App{Input: input, Output: io.Discard, Terminal: &fakeTerminal{size: term.Size{Width: 120, Height: 30}}, Ticker: &fakeTicker{}, Collector: &fakeCollector{snapshots: []monitor.Snapshot{{Rows: []monitor.Row{row}}}}, Details: &fakeDetailRepository{detail: &plan.PlanDetail{}}, Changes: DetailChangesFuncs{SnapshotFunc: func(ctx context.Context, _ *plan.PlanDetail, _ string) (DetailChangesSnapshot, error) {
		starts <- ctx
		once.Do(func() { close(started) })
		<-ctx.Done()
		return DetailChangesSnapshot{}, ctx.Err()
	}}}
	done := make(chan error, 1)
	settled := make(chan struct{})
	go func() {
		defer close(settled)
		done <- a.Run(runCtx)
	}()
	t.Cleanup(func() {
		cancel() // Also releases the input gate if the loader never started.
		receiveChanges(t, settled)
	})
	ctx := receiveChanges(t, starts)
	if err := receiveChanges(t, done); err != nil {
		t.Fatal(err)
	}
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
	if s.detail.changesPending {
		t.Fatal("loaded inactive tab")
	}
	s.detail.activeTab = detailTabChanges
	a.syncDetailChanges(&s)
	a.syncDetailChanges(&s)
	if !receiveLoopUpdate(t, &s) {
		t.Fatal("snapshot did not request redraw")
	}
	if len(calls) != 1 {
		t.Fatal("not single flight")
	}
	if s.detail.changes.Snapshot.Files[0].Path != "raw\x1b" || s.detail.changes.Snapshot.Files[0].Display != "unsafe " {
		t.Fatal("projection changed identity or retained escape")
	}
	receiveLoopUpdate(t, &s)
	if s.detail.changes.Diff.Lines[0].Text != "ok " {
		t.Fatal("unsanitized diff")
	}
	s.detail.changesForce = true
	a.syncDetailChanges(&s)
	receiveLoopUpdate(t, &s)
	receiveLoopUpdate(t, &s)
	if s.detail.changes.DiffStatus != "ready" {
		t.Fatal("equal signature stranded diff")
	}
}
