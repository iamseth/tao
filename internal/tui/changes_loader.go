package tui

import (
	"context"
	"errors"
	"time"

	"github.com/iamseth/tao/internal/monitor"
)

const detailChangesMinRefresh = time.Second

// Throttle by delivered ticks, never by collection start or completion latency.
func (a App) refreshDetailChanges(s *loopState, tick time.Time) {
	d := s.detail
	if d == nil || d.activeTab != detailTabChanges || d.sliceOpen || d.snapshotUpdates != nil {
		return
	}
	if !d.changesTick.IsZero() && tick.Sub(d.changesTick) < detailChangesMinRefresh {
		return
	}
	d.changesTick = tick
	a.syncDetailChanges(s)
}

type changesUpdate struct {
	force      bool
	generation uint64
	snapshot   DetailChangesSnapshot
	err        error
}
type fileDiffUpdate struct {
	generation      uint64
	signature, path string
	diff            DetailFileDiff
	err             error
}
type changesLoadState struct {
	changesGeneration, diffGeneration uint64
	changesCancel, diffCancel         context.CancelFunc
	snapshotUpdates                   <-chan changesUpdate
	diffUpdates                       <-chan fileDiffUpdate
	selectedPath, requestedScope      string
	diffError                         string
	changesForce                      bool
	changesTick                       time.Time
}

func (d *detailState) cancelChanges() {
	if d.changesCancel != nil {
		d.changesCancel()
		d.changesCancel = nil
	}
	if d.diffCancel != nil {
		d.diffCancel()
		d.diffCancel = nil
	}
	d.changesGeneration++
	d.diffGeneration++
	d.snapshotUpdates = nil
	d.diffUpdates = nil
	d.changes.Updating = false
}
func (s *loopState) changesUpdates() <-chan changesUpdate {
	if s.detail == nil {
		return nil
	}
	return s.detail.snapshotUpdates
}
func (s *loopState) fileDiffUpdates() <-chan fileDiffUpdate {
	if s.detail == nil {
		return nil
	}
	return s.detail.diffUpdates
}
func (a App) openDetailTab(ctx context.Context, s *loopState, row monitor.Row, tab detailTab) {
	s.closeDetail()
	a.openDetail(ctx, s, row)
	s.detail.activeTab = tab
	a.syncDetailChanges(s)
}
func (a App) syncDetailChanges(s *loopState) {
	d := s.detail
	if d == nil {
		return
	}
	if d.activeTab != detailTabChanges || d.sliceOpen {
		d.cancelChanges()
		return
	}
	a.startDetailChanges(s)
}
func (a App) startDetailChanges(s *loopState) {
	d := s.detail
	if d == nil || d.activeTab != detailTabChanges || d.sliceOpen || d.plan == nil {
		return
	}
	if a.Changes == nil {
		d.changes.Status = "unavailable"
		return
	}
	if d.snapshotUpdates != nil && !d.changesForce {
		return
	}
	force := d.changesForce
	d.changesForce = false
	if force {
		d.cancelChanges()
	}
	d.changesGeneration++
	generation := d.changesGeneration
	ctx, cancel := context.WithCancel(d.ctx)
	d.changesCancel = cancel
	updates := make(chan changesUpdate, 1)
	d.snapshotUpdates = updates
	d.changes.Updating = true
	if d.changes.Status != "ready" {
		d.changes.Status = "loading"
	}
	loaded, scope := d.plan, d.requestedScope
	go func() {
		u := changesUpdate{generation: generation, force: force}
		defer func() {
			if recover() != nil {
				u.err = errors.New("changes loader panicked")
			}
			select {
			case updates <- u:
			case <-ctx.Done():
			}
			close(updates)
		}()
		result, err := a.Changes.Snapshot(ctx, loaded, scope)
		u.err = changesLoadError(err)
		u.snapshot = boundedDetailChanges(result)
	}()
}

// Error formatting is deliberately outside the event loop. Panic values are not
// formatted: arbitrary String methods may themselves panic or allocate unboundedly.
func changesLoadError(err error) error {
	if err == nil || errors.Is(err, context.Canceled) {
		return err
	}
	return errors.New(changesText(err.Error(), 240))
}
func waitChanges(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	}
}
func (a App) startDetailFileDiff(s *loopState, immediate bool) {
	d := s.detail
	if d == nil || d.activeTab != detailTabChanges || d.sliceOpen || d.plan == nil || a.Changes == nil {
		return
	}
	if d.diffCancel != nil {
		d.diffCancel()
	}
	d.diffGeneration++
	d.diffUpdates = nil
	files := d.changes.Snapshot.Files
	if len(files) == 0 {
		d.selectedPath = ""
		d.changes.Pinned = false
		d.changes.DiffOffset = 0
		d.changes.Diff = DetailFileDiff{}
		d.changes.DiffStatus = ""
		return
	}
	path := files[d.changes.FileIndex].Path
	if d.selectedPath != path {
		d.changes.Diff = DetailFileDiff{}
		d.changes.DiffOffset = 0
		d.changes.Pinned = false
	}
	d.selectedPath = path
	d.changes.DiffStatus = "loading"
	snapshot := d.changes.Snapshot
	generation := d.diffGeneration
	ctx, cancel := context.WithCancel(d.ctx)
	d.diffCancel = cancel
	updates := make(chan fileDiffUpdate, 1)
	d.diffUpdates = updates
	wait := a.ChangesWait
	if wait == nil {
		wait = waitChanges
	}
	go func() {
		u := fileDiffUpdate{generation: generation, signature: snapshot.Signature, path: path}
		defer func() {
			if recover() != nil {
				u.err = errors.New("diff loader panicked")
			}
			select {
			case updates <- u:
			case <-ctx.Done():
			}
			close(updates)
		}()
		if !immediate && !wait(ctx, 75*time.Millisecond) {
			u.err = context.Canceled
			return
		}
		if ctx.Err() != nil {
			u.err = ctx.Err()
			return
		}
		result, err := a.Changes.FileDiff(ctx, snapshot, path)
		u.err = changesLoadError(err)
		u.diff = boundedDetailFileDiff(result)
	}()
}
func (a App) acceptDetailChanges(s *loopState, u changesUpdate) {
	d := s.detail
	if d == nil || u.generation != d.changesGeneration {
		return
	}
	d.snapshotUpdates = nil
	if d.changesCancel != nil {
		d.changesCancel()
		d.changesCancel = nil
	}
	d.changes.Updating = false
	if u.err == nil && u.snapshot.Availability == "git_error" {
		u.err = errors.New(u.snapshot.Reason)
	}
	if u.err != nil {
		if errors.Is(u.err, context.Canceled) {
			return
		}
		d.changes.RefreshError = u.err.Error()
		if d.changes.Status != "ready" {
			d.changes.Status = "error"
		}
		if u.force {
			a.startDetailFileDiff(s, true)
		}
		return
	}
	oldSignature := d.changes.Snapshot.Signature
	d.changes.Snapshot = u.snapshot
	d.changes.Status = u.snapshot.Availability
	if d.changes.Status == "" {
		d.changes.Status = "ready"
	}
	d.changes.RefreshError = ""
	d.changes.UpdatedAt = u.snapshot.CollectedAt
	d.changes.Scope = u.snapshot.Scope
	for i, f := range u.snapshot.Files {
		if f.Path == d.selectedPath {
			d.changes.FileIndex = i
			break
		}
	}
	d.clampOffsets(s.size)
	path := ""
	if len(u.snapshot.Files) > 0 {
		path = u.snapshot.Files[d.changes.FileIndex].Path
	}
	if u.force || oldSignature != u.snapshot.Signature || path != d.selectedPath || ((d.changes.DiffStatus != "ready" || d.changes.Diff.Path != path) && d.diffUpdates == nil) {
		a.startDetailFileDiff(s, u.force)
	}
}
func (a App) acceptDetailFileDiff(s *loopState, u fileDiffUpdate) {
	d := s.detail
	if d == nil || u.generation != d.diffGeneration || u.signature != d.changes.Snapshot.Signature || u.path != d.selectedPath {
		return
	}
	d.diffUpdates = nil
	if d.diffCancel != nil {
		d.diffCancel()
		d.diffCancel = nil
	}
	if u.err != nil {
		if !errors.Is(u.err, context.Canceled) {
			d.changes.DiffStatus = "error"
			d.diffError = u.err.Error()
			d.changes.RefreshError = d.diffError
		}
		return
	}
	if d.changes.RefreshError == d.diffError {
		d.changes.RefreshError = ""
	}
	d.diffError = ""
	d.changes.Diff = u.diff
	d.changes.DiffStatus = "ready"
	if d.changes.Pinned {
		d.changes.DiffOffset = d.maxOffset(detailTabChanges, s.size)
	}
	d.clampOffsets(s.size)
}
