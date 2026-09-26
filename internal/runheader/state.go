package runheader

import (
	"time"

	"github.com/iamseth/tao/internal/runstatus"
)

// Slice is the presentation-safe checklist state for one plan slice.
type Slice struct {
	ID     string
	Title  string
	Status string
}

// State is the in-process state needed to render a live run header. It is
// presentation state only and is never persisted as lifecycle evidence.
type State struct {
	RepoName             string
	PlanID               string
	PlanTitle            string
	Agent                string
	ExecutionMode        string
	Branch               string
	ReviewEnabled        bool
	ReworkRound          int
	MaxReworkAttempts    int
	RecurringFindingFile string
	Slices               []Slice
	CompletedCount       int
	TotalCount           int
	Phase                runstatus.Phase
	CurrentSliceID       string
	CurrentSliceTitle    string
	StartedAt            time.Time
	AgentSessionCount    int
	TotalTokens          int64
	Cost                 float64
	CostReported         bool
	BatchPosition        int
	BatchTotal           int
}

// Clone returns a snapshot with an independent copy of the slice checklist.
func (state State) Clone() State {
	state.Slices = append([]Slice(nil), state.Slices...)
	return state
}
