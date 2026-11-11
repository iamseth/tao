package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iamseth/tao/internal/note"
	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/internal/planning"
	"github.com/iamseth/tao/internal/run"
	"github.com/iamseth/tao/internal/runtimeconfig"
	"github.com/iamseth/tao/internal/taodata"
	"github.com/iamseth/tao/internal/testenv"
)

func TestNoteRunExitCodes(t *testing.T) {
	cause := errors.New("cause")
	for code := 0; code <= 6; code++ {
		err := &NoteRunOutcome{Code: code, Cause: cause}
		wrapped := fmt.Errorf("wrapped: %w", err)
		var outcome *NoteRunOutcome
		if ExitCode(wrapped) != code || !errors.Is(wrapped, cause) || !errors.As(wrapped, &outcome) {
			t.Fatalf("code %d: %v", code, wrapped)
		}
	}
	if ExitCode(&run.SliceVerificationError{}) != NoteRunInfrastructure {
		t.Fatal("untyped error with nil cause guessed success")
	}
	if ExitCode(nil) != 0 || ExitCode(cause) != 1 {
		t.Fatal("unrelated exit behavior changed")
	}
}

func TestNoteRunDurableOutcomes(t *testing.T) {
	for _, tc := range []struct {
		name, status, sliceStatus string
		approval, before          bool
		code                      int
	}{
		{name: "new completion", status: plan.StatusCompleted, sliceStatus: plan.StatusCompleted, code: 0},
		{name: "pre-existing completion", status: plan.StatusCompleted, sliceStatus: plan.StatusCompleted, before: true, code: 6},
		{name: "partial progress", status: plan.StatusInProgress, sliceStatus: plan.StatusPending, code: 6},
		{name: "blocked", status: plan.StatusBlocked, sliceStatus: plan.StatusBlocked, code: 3},
		{name: "pending approval", status: plan.StatusPlanned, sliceStatus: plan.StatusPending, approval: true, code: 4},
		{name: "blocked approval", status: plan.StatusBlocked, sliceStatus: plan.StatusBlocked, approval: true, code: 4},
		{name: "no review", status: plan.StatusInReview, sliceStatus: plan.StatusCompleted, code: 4},
		{name: "merge decision", status: plan.StatusReviewed, sliceStatus: plan.StatusCompleted, code: 4},
	} {
		t.Run(tc.name, func(t *testing.T) {
			detail := &plan.PlanDetail{State: plan.State{Status: tc.status, Plan: plan.PlanState{ID: "plan", PendingSlices: []string{"001-a"}}}, Slices: plan.SlicesFile{Slices: []plan.Slice{{ID: "001-a", Status: tc.sliceStatus}}}}
			if tc.approval {
				detail.Slices.Slices[0].Approval = &plan.Approval{Required: true}
			}
			if tc.sliceStatus == plan.StatusCompleted {
				detail.State.Plan.PendingSlices = nil
				detail.State.Plan.CompletedSlices = []string{"001-a"}
			}
			if err := projectNoteRunOutcome(detail, tc.before); ExitCode(err) != tc.code {
				t.Fatalf("code=%d want=%d: %v", ExitCode(err), tc.code, err)
			}
		})
	}
	if ExitCode(projectNoteRunOutcome(nil, false)) != 1 {
		t.Fatal("nil load guessed success")
	}
}

func TestNoteRunHandoffOutcomePrecedence(t *testing.T) {
	testenv.SkipIfShort(t)
	for _, tc := range []struct {
		name, status, sliceStatus string
		executionErr              error
		remove                    bool
		code                      int
	}{
		{name: "completed", status: plan.StatusCompleted, sliceStatus: plan.StatusCompleted, code: 0},
		{name: "partial", status: plan.StatusInProgress, sliceStatus: plan.StatusPending, code: 6},
		{name: "nil blocked", status: plan.StatusBlocked, sliceStatus: plan.StatusBlocked, code: 3},
		{name: "no review is a decision", status: plan.StatusInReview, sliceStatus: plan.StatusCompleted, code: 4},
		{name: "cancel beats completion", status: plan.StatusCompleted, sliceStatus: plan.StatusCompleted, executionErr: context.Canceled, code: 1},
		{name: "transport beats blocker", status: plan.StatusBlocked, sliceStatus: plan.StatusBlocked, executionErr: errors.New("transport"), code: 1},
		{name: "typed gate beats completion", status: plan.StatusCompleted, sliceStatus: plan.StatusCompleted, executionErr: &plan.ApprovalRequiredError{}, code: 4},
		{name: "post-run load failed", remove: true, code: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clearTaoEnv(t)
			meta := taodata.Repo{ID: "outcome-repo", Name: "outcome", Root: "/repo"}
			app, _, _ := noteTestApp(t, strings.NewReader(""), meta)
			fixture := newRunPlanFixture(t, plan.StatusPlanned, []string{"001-a"}, nil, "001-a", plan.StatusPending)
			alignNoteRunFixture(t, app, fixture.dir)
			repo := app.noteRepository(meta)
			item, err := repo.Create(context.Background(), "run", nil)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := repo.PromoteToPlan(context.Background(), item.ID, note.PlanLink{ID: fixture.id, Dir: fixture.dir, Mode: "run"}); err != nil {
				t.Fatal(err)
			}
			old := executeSinglePlan
			calls := 0
			executeSinglePlan = func(run.Service, context.Context, run.Request) error {
				calls++
				switch {
				case tc.remove:
					if err := os.Remove(filepath.Join(fixture.dir, "state.json")); err != nil {
						t.Fatal(err)
					}
				case tc.sliceStatus == plan.StatusCompleted:
					fixture.write(tc.status, nil, []string{"001-a"}, "001-a", tc.sliceStatus)
				default:
					fixture.write(tc.status, []string{"001-a"}, nil, "001-a", tc.sliceStatus)
				}
				return tc.executionErr
			}
			t.Cleanup(func() { executeSinglePlan = old })
			err = app.Run(context.Background(), []string{"note", "run", item.ID, "--no-review", "--max-slices=1"})
			if ExitCode(err) != tc.code || calls != 1 {
				t.Fatalf("exit=%d want=%d calls=%d error=%v", ExitCode(err), tc.code, calls, err)
			}
			if tc.executionErr != nil && !errors.Is(err, tc.executionErr) {
				t.Fatalf("lost cause: %v", err)
			}
		})
	}
}

func TestNoteRunTypedExecutionFailures(t *testing.T) {
	for _, tc := range []struct {
		err  error
		code int
	}{
		{&plan.ApprovalRequiredError{SliceID: "001"}, 4},
		{&plan.ReworkScopeDecisionError{SliceID: "001"}, 4},
		{&plan.ApprovalEvidenceError{}, 4},
		{context.Canceled, 1}, {context.DeadlineExceeded, 1}, {errors.New("transport failed"), 1},
	} {
		wrapped := fmt.Errorf("handoff: %w", tc.err)
		err := classifyNoteRunError(wrapped)
		if ExitCode(err) != tc.code || !errors.Is(err, tc.err) {
			t.Fatalf("%v => %d", err, ExitCode(err))
		}
	}
}

func TestNoteRunTerminalNotesWithoutPlans(t *testing.T) {
	for _, tc := range []struct {
		item note.Note
		code int
	}{
		{note.Note{ID: "archived", Status: note.StatusArchived}, 6},
		{note.Note{ID: "missing", Status: note.StatusPromoted}, 1},
		{note.Note{ID: "planning", Status: note.StatusPromoted, Promotion: &note.PromotionLinks{PlanningSession: &note.PlanningSessionLink{ID: "session"}}}, 6},
	} {
		err := (App{Out: io.Discard}).runLinkedNote(context.Background(), taodata.Repo{}, tc.item, runtimeconfig.CommandOptions{})
		if ExitCode(err) != tc.code {
			t.Fatalf("%s: %v", tc.item.ID, err)
		}
	}
}

func TestNoteRunPlanningRefusal(t *testing.T) {
	for _, stage := range []planning.GenerationStage{planning.GenerationStageAllocation, planning.GenerationStagePrompt, planning.GenerationStageRuntime, "unknown"} {
		cause := errors.New("failed")
		err := classifyNoteRunError(&planning.GenerationError{Stage: stage, Err: cause})
		if ExitCode(err) != 1 || !errors.Is(err, cause) {
			t.Fatalf("stage %s: %v", stage, err)
		}
	}
	for _, failure := range []*planning.GenerationError{
		{Stage: planning.GenerationStageValidation, Err: context.Canceled},
		{Stage: planning.GenerationStageValidation, Err: errors.New("rejected"), CleanupErr: errors.New("disk error")},
	} {
		if ExitCode(classifyNoteRunError(failure)) != 1 {
			t.Fatalf("infrastructure hidden: %v", failure)
		}
	}
	for _, stage := range []planning.GenerationStage{planning.GenerationStageRequiredArtifact, planning.GenerationStagePlanID, planning.GenerationStageVerification, planning.GenerationStageOpenQuestions, planning.GenerationStageValidation} {
		refusal := &planning.GenerationError{Stage: stage, Err: errors.New("rejected")}
		for _, err := range []error{refusal, fmt.Errorf("wrapped: %w", refusal), errors.Join(refusal, refusal)} {
			if got := ExitCode(classifyNoteRunError(err)); got != 5 {
				t.Fatalf("%s: code %d", stage, got)
			}
		}
		if got := ExitCode(classifyNoteRunError(errors.Join(refusal, context.Canceled))); got != 1 {
			t.Fatalf("infrastructure hidden: %d", got)
		}
	}
}
