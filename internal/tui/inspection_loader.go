package tui

import (
	"context"

	"github.com/iamseth/tao/internal/term/cells"
)

func (a App) startDetailInspection(state *loopState) {
	detail := state.detail
	key := detailInspectionKey(detail.plan)
	if key == detail.inspectionKey {
		return
	}
	if detail.inspectionCancel != nil {
		detail.inspectionCancel()
		detail.inspectionCancel = nil
	}
	detail.inspectionKey = key
	detail.inspectionGeneration++
	if a.Inspector == nil || detail.plan == nil {
		detail.inspection = detailInspectionView{status: detailInspectionUnavailable}
		return
	}
	ctx, cancel := context.WithCancel(detail.ctx)
	detail.inspectionCancel = cancel
	detail.inspection = detailInspectionView{status: detailInspectionLoading}
	updates := state.ensureUpdates()
	generation, loaded := detail.inspectionGeneration, detail.plan
	go func() {
		result, err := a.Inspector.Inspect(ctx, loaded)
		view := detailInspectionView{status: detailInspectionReady, findings: boundedDetailInspection(result).Findings}
		if err != nil {
			view = detailInspectionView{status: detailInspectionFailed, err: cells.Truncate(singleLineDetail(err.Error()), detailInspectionMaxText)}
		}
		publishLoopUpdate(ctx, updates, func(s *loopState) bool {
			if s.detail != detail || ctx.Err() != nil || detail.inspectionGeneration != generation || detail.inspectionKey != key {
				return false
			}
			detail.inspectionCancel = nil
			cancel()
			detail.inspection = view
			detail.clampOffsets(s.size)
			return true
		})
	}()
}
