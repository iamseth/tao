package tui

import (
	"context"
	"strings"

	"github.com/iamseth/tao/internal/term"
)

type trendsResult struct {
	generation uint64
	snapshot   TrendsSnapshot
	err        error
}

// trendsState is owned by the input loop; collectors only send immutable results.
type trendsState struct {
	model      TrendsModel
	updates    chan trendsResult
	generation uint64
	good       bool
}

func (a App) refreshTrends(ctx context.Context, s *loopState) {
	if s.activePage() != PageTrends || s.trends.model.Loading || ctx.Err() != nil {
		return
	}
	if a.Trends == nil {
		s.trends.model.Snapshot.Unavailable = "trends collector unavailable"
		return
	}
	if s.trends.updates == nil {
		s.trends.updates = make(chan trendsResult, 1)
	}
	s.trends.generation++
	generation, updates := s.trends.generation, s.trends.updates
	s.trends.model.Loading = true
	go func() {
		snapshot, err := a.Trends.Collect(ctx)
		select {
		case updates <- trendsResult{generation: generation, snapshot: snapshot, err: err}:
		case <-ctx.Done():
		}
	}()
}

func (s *loopState) acceptTrends(result trendsResult) {
	if result.generation != s.trends.generation {
		return
	}
	m := &s.trends.model
	m.Loading = false
	if result.err != nil {
		m.Failure = result.err.Error()
		m.Stale = s.trends.good
		if !s.trends.good {
			m.Snapshot.Unavailable = "collection failed"
		}
	} else {
		m.Snapshot = result.snapshot
		m.Failure, m.Stale = "", false
		s.trends.good = true
	}
	s.clampTrendsOffset()
}

func (s loopState) trendsModel() *TrendsModel {
	m := s.trends.model
	m.Width, m.Height = max(1, s.size.Width), max(1, s.size.Height-1)
	m.Theme, m.Profile = s.theme, s.profile
	return &m
}

func (s loopState) trendsMaxOffset() int {
	m := s.trendsModel()
	height := m.Height
	m.Height, m.ScrollOffset = int(^uint(0)>>1), 0
	return max(0, len(strings.Split(RenderTrends(*m), "\n"))-height)
}

func (s *loopState) clampTrendsOffset() {
	s.trends.model.ScrollOffset = min(max(0, s.trends.model.ScrollOffset), s.trendsMaxOffset())
}

func (s *loopState) handleTrendsKey(key term.KeyEvent) bool {
	m := &s.trends.model
	switch {
	case key.Key == term.KeyRune && key.Rune == 'c':
		switch m.Cohort {
		case "pi":
			m.Cohort = "claude"
		case "claude":
			m.Cohort = "overall"
		default:
			m.Cohort = "pi"
		}
		m.ScrollOffset = 0
	case key.Key == term.KeyArrowUp || key.Key == term.KeyRune && key.Rune == 'k':
		m.ScrollOffset--
	case key.Key == term.KeyArrowDown || key.Key == term.KeyRune && key.Rune == 'j':
		m.ScrollOffset++
	case key.Key == term.KeyRune && key.Rune == 'g':
		m.ScrollOffset = 0
	case key.Key == term.KeyRune && key.Rune == 'G':
		m.ScrollOffset = s.trendsMaxOffset()
	default:
		return false
	}
	s.clampTrendsOffset()
	return true
}
