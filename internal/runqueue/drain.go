package runqueue

import (
	"context"
	"time"
)

func (m *Manager) WaitForDrain(ctx context.Context) error {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err := m.drainError(); err != nil {
			return err
		}
		pending, active := m.drainCounts()
		if pending == 0 && active == 0 {
			return nil
		}
		if m.StopRequested() && active == 0 {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-m.drainFailed:
		case <-ticker.C:
		}
	}
}

func (m *Manager) drainCounts() (pending int, active int) {
	for _, entry := range m.Queue().Entries {
		if entry.Status == QueueStatusPending {
			pending++
		}
	}
	return pending, len(m.ActiveStatuses())
}
