// Package plantest provides test-support helpers for the plan package:
// an in-memory Repository fake and a fluent PlanDetail builder.
// No production package may import internal/plantest.
package plantest

import (
	"time"

	"github.com/iamseth/tao/internal/plan"
)

// defaultTimestamp is the stable fixture timestamp used by builders.
var defaultTimestamp = time.Date(2026, 4, 30, 12, 0, 0, 0, time.UTC)

// PlanDetailBuilder builds plan.PlanDetail values for tests without JSON fixtures.
type PlanDetailBuilder struct {
	state  plan.State
	slices []plan.Slice
}

// NewPlanDetail returns a builder for a plan with the given ID.
// Default status is "planned"; use WithStatus to change it.
func NewPlanDetail(id string) *PlanDetailBuilder {
	return &PlanDetailBuilder{
		state: plan.State{
			Schema:           "tao.plan.state.v1",
			Status:           plan.StatusPlanned,
			CreatedAt:        defaultTimestamp,
			UpdatedAt:        defaultTimestamp,
			GlobalInvariants: []string{},
			OpenQuestions:    []string{},
			Plan: plan.PlanState{
				ID:              id,
				CompletedSlices: []string{},
				PendingSlices:   []string{},
				Timing:          plan.PlanTiming{},
			},
		},
	}
}

// WithTitle sets the plan title.
func (b *PlanDetailBuilder) WithTitle(title string) *PlanDetailBuilder {
	b.state.Plan.Title = title
	return b
}

// WithStatus sets the plan-level status.
func (b *PlanDetailBuilder) WithStatus(status string) *PlanDetailBuilder {
	b.state.Status = status
	return b
}

// WithRepoRoot sets the repository root in the plan state.
func (b *PlanDetailBuilder) WithRepoRoot(root string) *PlanDetailBuilder {
	b.state.Repo.Root = root
	return b
}

// WithCurrentSlice sets the current in-progress slice ID.
// Pass an empty string to clear it.
func (b *PlanDetailBuilder) WithCurrentSlice(id string) *PlanDetailBuilder {
	if id == "" {
		b.state.Plan.CurrentSlice = nil
	} else {
		s := id
		b.state.Plan.CurrentSlice = &s
	}
	return b
}

// WithPendingSlices sets the ordered pending slice queue.
func (b *PlanDetailBuilder) WithPendingSlices(ids ...string) *PlanDetailBuilder {
	b.state.Plan.PendingSlices = ids
	return b
}

// WithCompletedSlices sets the completed slice list.
func (b *PlanDetailBuilder) WithCompletedSlices(ids ...string) *PlanDetailBuilder {
	b.state.Plan.CompletedSlices = ids
	return b
}

// AddSlice appends a slice to the plan's slice list.
func (b *PlanDetailBuilder) AddSlice(s plan.Slice) *PlanDetailBuilder {
	b.slices = append(b.slices, s)
	return b
}

// Build returns the constructed PlanDetail.
func (b *PlanDetailBuilder) Build() *plan.PlanDetail {
	slices := make([]plan.Slice, len(b.slices))
	copy(slices, b.slices)
	return &plan.PlanDetail{
		State: b.state,
		Slices: plan.SlicesFile{
			Schema:    "tao.plan.slices.v1",
			PlanID:    b.state.Plan.ID,
			Execution: plan.Execution{Mode: "serial"},
			Slices:    slices,
		},
	}
}

// SliceBuilder builds plan.Slice values for tests.
type SliceBuilder struct {
	s plan.Slice
}

// NewSlice returns a builder for a slice with the given ID.
// Default status is "pending".
func NewSlice(id string) *SliceBuilder {
	return &SliceBuilder{s: plan.Slice{
		ID:            id,
		Status:        plan.StatusPending,
		DependsOn:     []string{},
		Tasks:         []string{},
		ExpectedFiles: []string{},
		Verification: plan.Verification{
			Commands:     []string{},
			ManualChecks: []string{},
		},
		Timing: plan.SliceTiming{
			CreatedAt: defaultTimestamp,
			UpdatedAt: defaultTimestamp,
		},
	}}
}

// WithTitle sets the slice title.
func (b *SliceBuilder) WithTitle(title string) *SliceBuilder {
	b.s.Title = title
	return b
}

// WithStatus sets the slice status.
func (b *SliceBuilder) WithStatus(status string) *SliceBuilder {
	b.s.Status = status
	return b
}

// WithDependsOn sets the slice dependency list.
func (b *SliceBuilder) WithDependsOn(ids ...string) *SliceBuilder {
	b.s.DependsOn = ids
	return b
}

// WithApproval attaches approval metadata to the slice.
func (b *SliceBuilder) WithApproval(a *plan.Approval) *SliceBuilder {
	b.s.Approval = a
	return b
}

// WithCompletedAt records a completion timestamp on the slice timing.
func (b *SliceBuilder) WithCompletedAt(t time.Time) *SliceBuilder {
	b.s.Timing.CompletedAt = &t
	return b
}

// WithVerificationCommands sets the slice verification command list.
func (b *SliceBuilder) WithVerificationCommands(cmds ...string) *SliceBuilder {
	b.s.Verification.Commands = cmds
	return b
}

// Build returns the constructed Slice.
func (b *SliceBuilder) Build() plan.Slice {
	return b.s
}

// Approval is a convenience constructor for plan.Approval to reduce boilerplate.
func Approval(required bool, reason string, approved bool) *plan.Approval {
	return &plan.Approval{Required: required, Reason: reason, Approved: approved}
}
