package plan

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestAutomaticReworkRoundModel(t *testing.T) {
	for _, model := range []string{"", "provider/strong"} {
		t.Run(model, func(t *testing.T) {
			event := automaticReworkRoundEvent("plan", AutomaticReworkRound{
				Round: 4, Attempts: 4, MaxAttempts: 5, Model: model,
				Fingerprint: "findings", ReopenedAt: editTime(),
			})
			wantMessage := "Automatic rework round 4 (attempt 4 of 5)"
			if model != "" {
				wantMessage += " on model " + model
			}
			if event.Type != EventTypeReworkRound || event.Model != model || event.Message != wantMessage {
				t.Fatalf("rework round event = %+v", event)
			}
			data, err := json.Marshal(event)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(data), `"model":`) != (model != "") {
				t.Fatalf("unexpected model serialization: %s", data)
			}
			var decoded Event
			if err := json.Unmarshal(data, &decoded); err != nil {
				t.Fatal(err)
			}
			if got := ReworkRoundModel([]Event{decoded}, 4); got != model {
				t.Fatalf("round-trip model = %q, want %q", got, model)
			}
		})
	}
}

func TestReworkRoundModel(t *testing.T) {
	for _, tt := range []struct {
		name   string
		events []Event
		round  int
		want   string
	}{
		{name: "absent", round: 4},
		{name: "legacy", events: []Event{{Type: EventTypeReworkRound, Round: 4}}, round: 4},
		{name: "latest matching round", events: []Event{
			{Type: EventTypeReworkRound, Round: 4, Model: "old"},
			{Type: EventTypeReworkRound, Round: 4, Model: "new"},
			{Type: EventTypeReworkRound, Round: 5, Model: "other-round"},
			{Type: EventTypeReworkStopped, Round: 4, Model: "not-a-round"},
		}, round: 4, want: "new"},
		{name: "latest empty wins", events: []Event{
			{Type: EventTypeReworkRound, Round: 4, Model: "old"},
			{Type: EventTypeReworkRound, Round: 4},
		}, round: 4},
		{name: "missing round", events: []Event{{Type: EventTypeReworkRound, Round: 4, Model: "m"}}, round: 3},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := ReworkRoundModel(tt.events, tt.round); got != tt.want {
				t.Fatalf("ReworkRoundModel = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestMarkSliceBlockedEvidence(t *testing.T) {
	for _, tt := range []struct {
		name      string
		evidence  *SliceBlockedEvidence
		wantOwned bool
	}{
		{name: "legacy"},
		{name: "owned", evidence: &SliceBlockedEvidence{PlanOwned: true, HeadSHA: "head", WorktreeFingerprint: "fingerprint"}, wantOwned: true},
		{name: "not owned", evidence: &SliceBlockedEvidence{HeadSHA: "head", WorktreeFingerprint: "fingerprint"}},
		{name: "no head", evidence: &SliceBlockedEvidence{PlanOwned: true, WorktreeFingerprint: "fingerprint"}},
		{name: "no fingerprint", evidence: &SliceBlockedEvidence{PlanOwned: true, HeadSHA: "head"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			detail := startSliceDetail("")
			if tt.evidence != nil {
				tt.evidence.GateCommand = "go test ./internal/plan"
				tt.evidence.FailingPaths = []string{` ./internal\plan/b.go `, "internal/plan/a.go", "internal/plan/./a.go"}
			}
			event, appendEvent, err := markSliceBlocked(detail, "001-a", "  lint failed  ", tt.evidence, editTime())
			if err != nil || !appendEvent {
				t.Fatalf("block: append=%v, err=%v", appendEvent, err)
			}
			if detail.Slices.Slices[0].BlockerNote != "lint failed" || event.Reason != "lint failed" {
				t.Fatalf("blocker note changed: %+v", event)
			}
			wantClassification := ""
			if tt.wantOwned {
				wantClassification = BlockerClassificationPlanOwned
			}
			if event.BlockerClassification != wantClassification {
				t.Fatalf("classification = %q, want %q", event.BlockerClassification, wantClassification)
			}
			if tt.evidence == nil {
				if event.Paths != nil || event.Command != "" || event.HeadSHA != "" || event.Fingerprint != "" {
					t.Fatalf("legacy block gained evidence: %+v", event)
				}
				return
			}
			if event.Command != tt.evidence.GateCommand || event.HeadSHA != tt.evidence.HeadSHA || event.Fingerprint != tt.evidence.WorktreeFingerprint || !reflect.DeepEqual(event.Paths, []string{"internal/plan/a.go", "internal/plan/b.go"}) {
				t.Fatalf("incorrect evidence: %+v", event)
			}
			if tt.evidence.FailingPaths[0] != ` ./internal\plan/b.go ` {
				t.Fatal("caller paths mutated")
			}
			tt.evidence.FailingPaths[0] = "changed.go"
			if event.Paths[0] != "internal/plan/a.go" {
				t.Fatal("event aliases caller paths")
			}
		})
	}
}

func TestMarkSliceBlockedEvidencePathBound(t *testing.T) {
	paths := []string{"", " ", "../outside.go", "/absolute.go", `C:\absolute.go`, "."}
	for i := 69; i >= 0; i-- {
		paths = append(paths, fmt.Sprintf(" ./dir\\%02d.go ", i), fmt.Sprintf("dir/%02d.go", i))
	}
	event, _, err := markSliceBlocked(startSliceDetail(""), "001-a", "lint", &SliceBlockedEvidence{FailingPaths: paths}, editTime())
	if err != nil {
		t.Fatal(err)
	}
	want := make([]string, 64)
	for i := range want {
		want[i] = fmt.Sprintf("dir/%02d.go", i)
	}
	if !reflect.DeepEqual(event.Paths, want) {
		t.Fatalf("bounded paths = %q, want %q", event.Paths, want)
	}
}

func TestLifecycleSelectedSliceEdges(t *testing.T) {
	tests := []struct {
		name             string
		detail           *PlanDetail
		wantNext         string
		wantRunnable     bool
		wantComplete     bool
		wantRunnableText string
	}{
		{
			name: "pending",
			detail: &PlanDetail{
				State:  State{Status: StatusPlanned, Plan: PlanState{ID: "plan", PendingSlices: []string{"001-a"}}},
				Slices: SlicesFile{Slices: []Slice{{ID: "001-a", Status: StatusPending}}},
			},
			wantNext:     "001-a",
			wantRunnable: true,
		},
		{
			name: "blocked plan",
			detail: &PlanDetail{
				State:  State{Status: StatusBlocked, Plan: PlanState{ID: "plan", PendingSlices: []string{"001-a"}}},
				Slices: SlicesFile{Slices: []Slice{{ID: "001-a", Status: StatusPending}}},
			},
			wantNext:         "001-a",
			wantRunnableText: "plan plan is blocked",
		},
		{
			name: "completed plan",
			detail: &PlanDetail{
				State:  State{Status: StatusCompleted, Plan: PlanState{ID: "plan", CompletedSlices: []string{"001-a"}}},
				Slices: SlicesFile{Slices: []Slice{{ID: "001-a", Status: StatusCompleted}}},
			},
			wantComplete:     true,
			wantRunnableText: "plan plan is complete",
		},
		{
			name: "missing slice",
			detail: &PlanDetail{
				State: State{Status: StatusPlanned, Plan: PlanState{ID: "plan", PendingSlices: []string{"001-a"}}},
			},
			wantNext:         "001-a",
			wantRunnableText: "slice 001-a not found",
		},
		{
			name: "approval gated",
			detail: &PlanDetail{
				State:  State{Status: StatusPlanned, Plan: PlanState{ID: "plan", PendingSlices: []string{"001-a"}}},
				Slices: SlicesFile{Slices: []Slice{{ID: "001-a", Status: StatusPending, Approval: &Approval{Required: true, Reason: "approval"}}}},
			},
			wantNext:         "001-a",
			wantRunnableText: "requires approval",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lifecycle := AnalyzeLifecycle(tt.detail)
			if lifecycle.NextSliceID != tt.wantNext {
				t.Fatalf("expected next slice %q, got %+v", tt.wantNext, lifecycle)
			}
			if lifecycle.Runnable != tt.wantRunnable {
				t.Fatalf("expected runnable=%v, got %+v", tt.wantRunnable, lifecycle)
			}
			if lifecycle.Complete != tt.wantComplete {
				t.Fatalf("expected complete=%v, got %+v", tt.wantComplete, lifecycle)
			}
			if tt.wantRunnableText == "" && lifecycle.RunnableError != nil {
				t.Fatalf("expected no runnable error, got %v", lifecycle.RunnableError)
			}
			if tt.wantRunnableText != "" && (lifecycle.RunnableError == nil || !strings.Contains(lifecycle.RunnableError.Error(), tt.wantRunnableText)) {
				t.Fatalf("expected runnable error containing %q, got %v", tt.wantRunnableText, lifecycle.RunnableError)
			}
		})
	}
}

func TestMarkSliceExecutionStartIsIdempotentAndImmutable(t *testing.T) {
	detail := startSliceDetail("")
	boundary := SliceExecutionStart{Branch: "tao/plan", Head: "abc123", CommitPolicy: "slice", WorkspaceStrategy: WorkspaceStrategyWorktree}
	if err := markSliceExecutionStart(detail, "001-a", boundary); err != nil {
		t.Fatalf("record boundary: %v", err)
	}
	if err := markSliceExecutionStart(detail, "001-a", boundary); err != nil {
		t.Fatalf("repeat identical boundary: %v", err)
	}
	for _, changed := range []SliceExecutionStart{
		{Branch: "other", Head: "abc123", CommitPolicy: "slice", WorkspaceStrategy: WorkspaceStrategyWorktree},
		{Branch: "tao/plan", Head: "def456", CommitPolicy: "slice", WorkspaceStrategy: WorkspaceStrategyWorktree},
	} {
		if err := markSliceExecutionStart(detail, "001-a", changed); err == nil || !strings.Contains(err.Error(), "refusing to overwrite branch or head") {
			t.Fatalf("boundary overwrite error = %v", err)
		}
	}
	if got := detail.Slices.Slices[0].ExecutionStart; got == nil || *got != boundary {
		t.Fatalf("boundary changed: %#v", got)
	}
	if workspace := detail.State.Workspace; workspace == nil || workspace.Strategy != WorkspaceStrategyWorktree || workspace.Branch != boundary.Branch || workspace.HeadSHA != boundary.Head {
		t.Fatalf("workspace boundary mirror = %#v, want %#v", workspace, boundary)
	}
}

func TestMarkSliceExecutionStartRefreshesWorkspaceBoundaryForLaterSlice(t *testing.T) {
	detail := startSliceDetail("")
	detail.State.Workspace = &Workspace{Strategy: WorkspaceStrategyWorktree, Branch: "tao/plan", HeadSHA: "base"}
	detail.Slices.Slices = append(detail.Slices.Slices, Slice{ID: "002-b", Status: StatusPending})
	boundary := SliceExecutionStart{Branch: "tao/plan", Head: "first-commit", CommitPolicy: "slice", WorkspaceStrategy: WorkspaceStrategyWorktree}

	if err := markSliceExecutionStart(detail, "002-b", boundary); err != nil {
		t.Fatalf("record later boundary: %v", err)
	}
	if detail.State.Workspace.Branch != boundary.Branch || detail.State.Workspace.HeadSHA != boundary.Head {
		t.Fatalf("workspace boundary = %#v, want later slice %#v", detail.State.Workspace, boundary)
	}
}

func TestLifecycleBlocksUnsettledAutomaticSliceCompletion(t *testing.T) {
	detail := &PlanDetail{
		State: State{Status: StatusInProgress, Plan: PlanState{
			ID: "plan", CompletedSlices: []string{"001-a"}, PendingSlices: []string{"002-b"},
		}},
		Slices: SlicesFile{Slices: []Slice{
			{ID: "001-a", Status: StatusInProgress, CommitIntent: &SliceCommitIntent{Policy: "slice"}},
			{ID: "002-b", Status: StatusPending},
		}},
	}

	lifecycle := AnalyzeLifecycle(detail)
	if lifecycle.Runnable || lifecycle.Complete {
		t.Fatalf("unsettled automatic completion advanced lifecycle: %+v", lifecycle)
	}
	if lifecycle.RunnableError == nil || !strings.Contains(lifecycle.RunnableError.Error(), "completion outcome is missing") {
		t.Fatalf("runnable error = %v, want recovery guidance", lifecycle.RunnableError)
	}

	detail.Slices.Slices[0].Status = StatusCompleted
	detail.Slices.Slices[0].Completion = &SliceCompletionOutcome{Outcome: SliceCompletionCommitted, CommitSHA: "commit-sha"}
	lifecycle = AnalyzeLifecycle(detail)
	if !lifecycle.Runnable || lifecycle.NextSliceID != "002-b" {
		t.Fatalf("persisted automatic outcome did not unblock next slice: %+v", lifecycle)
	}
}

func TestLifecycleMutationHelpersRejectInvalidMutations(t *testing.T) {
	tests := []struct {
		name   string
		mutate func() error
		want   string
	}{
		{
			name: "start nil detail",
			mutate: func() error {
				_, _, err := markSliceStarted(nil, "001-a", editTime())
				return err
			},
			want: "plan detail is nil",
		},
		{
			name: "start missing slice",
			mutate: func() error {
				_, _, err := markSliceStarted(startSliceDetail(""), "missing", editTime())
				return err
			},
			want: "slice missing not found",
		},
		{
			name: "complete without start",
			mutate: func() error {
				_, _, err := markSliceCompleted(startSliceDetail(""), "001-a", "done", nil, editTime())
				return err
			},
			want: "has no started_at",
		},
		{
			name: "approve not required",
			mutate: func() error {
				_, _, err := markSliceApproved(startSliceDetail(""), "001-a", "Seth", editTime())
				return err
			},
			want: "does not require approval",
		},
		{
			name: "approve blank actor",
			mutate: func() error {
				detail := startSliceDetail("")
				detail.Slices.Slices[0].Approval = &Approval{Required: true, Reason: "approval"}
				_, _, err := markSliceApproved(detail, "001-a", "  ", editTime())
				return err
			},
			want: "approved_by is required",
		},
		{
			name: "continue not blocked",
			mutate: func() error {
				return markBlockedContinued(startSliceDetail(""), editTime())
			},
			want: "continue is not meaningful",
		},
		{
			name: "remove dependent slice",
			mutate: func() error {
				_, err := markSliceRemoved(editPlanDetail(), "001-a", editTime())
				return err
			},
			want: "pending slices depend on it",
		},
		{
			name: "skip not pending",
			mutate: func() error {
				detail := editPlanDetail()
				detail.State.Plan.PendingSlices = []string{"002-b", "003-c"}
				_, err := markSliceSkipped(detail, "001-a", editTime())
				return err
			},
			want: "not in pending_slices",
		},
		{
			name: "reorder omits pending slice",
			mutate: func() error {
				_, err := markPendingSlicesReordered(editPlanDetail(), []string{"001-a", "002-b"}, editTime())
				return err
			},
			want: "must include every pending slice",
		},
		{
			name: "reorder duplicate pending slice",
			mutate: func() error {
				_, err := markPendingSlicesReordered(editPlanDetail(), []string{"001-a", "001-a", "003-c"}, editTime())
				return err
			},
			want: "duplicate slice 001-a",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.mutate()
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("expected error containing %q, got %v", tt.want, err)
			}
		})
	}
}

func TestAbandonedLifecycleMutatorsPreserveStatusSlicesQueueAndEvents(t *testing.T) {
	tests := []struct {
		name   string
		setup  func(*PlanDetail)
		mutate func(*PlanDetail) error
	}{
		{
			name: "block slice",
			mutate: func(detail *PlanDetail) error {
				_, _, err := markSliceBlocked(detail, "001-a", "cannot continue", nil, editTime())
				return err
			},
		},
		{
			name: "budget block completed slice",
			setup: func(detail *PlanDetail) {
				detail.State.Plan.CompletedSlices = []string{"001-a"}
				detail.State.Plan.PendingSlices = []string{"002-b", "003-c"}
				detail.Slices.Slices[0].Status = StatusCompleted
			},
			mutate: func(detail *PlanDetail) error {
				_, _, err := markSliceBudgetBlocked(detail, "001-a", "budget exceeded", editTime())
				return err
			},
		},
		{
			name: "remove pending slice",
			setup: func(detail *PlanDetail) {
				detail.Slices.Slices[1].DependsOn = nil
			},
			mutate: func(detail *PlanDetail) error {
				_, err := markSliceRemoved(detail, "001-a", editTime())
				return err
			},
		},
		{
			name: "skip pending slice",
			setup: func(detail *PlanDetail) {
				detail.Slices.Slices[1].DependsOn = nil
			},
			mutate: func(detail *PlanDetail) error {
				_, err := markSliceSkipped(detail, "001-a", editTime())
				return err
			},
		},
		{
			name: "reorder pending slices",
			mutate: func(detail *PlanDetail) error {
				_, err := markPendingSlicesReordered(detail, []string{"001-a", "003-c", "002-b"}, editTime())
				return err
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			detail := editPlanDetail()
			detail.State.Status = StatusAbandoned
			detail.Events = []Event{{Type: EventTypePlanAbandoned, Timestamp: editTime().Add(-time.Hour), PlanID: "edit", Reason: "superseded"}}
			if test.setup != nil {
				test.setup(detail)
			}
			before := clonePlanDetail(detail)

			err := test.mutate(detail)
			if err == nil || !strings.Contains(err.Error(), "plan edit is abandoned: superseded") {
				t.Fatalf("mutation error = %v", err)
			}
			if !reflect.DeepEqual(detail.State, before.State) || !reflect.DeepEqual(detail.Slices, before.Slices) || !reflect.DeepEqual(detail.Events, before.Events) {
				t.Fatalf("abandoned mutation changed preserved artifacts:\n got: %#v\nwant: %#v", detail, before)
			}
		})
	}
}

func TestLifecycleMutationEventIdempotencyDecisions(t *testing.T) {
	first := editTime()
	second := first.Add(2 * time.Minute)

	t.Run("start event already present", func(t *testing.T) {
		detail := startSliceDetail("")
		detail.Events = []Event{{Type: EventTypeSliceStarted, Timestamp: first, PlanID: "plan-a", SliceID: "001-a"}}

		_, appendEvent, err := markSliceStarted(detail, "001-a", second)
		if err != nil {
			t.Fatal(err)
		}
		if appendEvent {
			t.Fatalf("expected existing start event to suppress append")
		}
		if detail.Slices.Slices[0].Timing.StartedAt == nil || !detail.Slices.Slices[0].Timing.StartedAt.Equal(second) {
			t.Fatalf("expected start metadata to still be applied, got %#v", detail.Slices.Slices[0].Timing)
		}
	})

	t.Run("complete event already present", func(t *testing.T) {
		detail := startSliceDetail("")
		detail.Slices.Slices[0].Timing.StartedAt = &first
		detail.Events = []Event{{Type: EventTypeSliceCompleted, Timestamp: first, PlanID: "plan-a", SliceID: "001-a"}}

		_, appendEvent, err := markSliceCompleted(detail, "001-a", "done", nil, second)
		if err != nil {
			t.Fatal(err)
		}
		if appendEvent {
			t.Fatalf("expected existing completion event to suppress append")
		}
		if len(detail.State.Plan.CompletedSlices) != 1 || detail.Slices.Slices[0].Notes != "done" {
			t.Fatalf("expected completion metadata to still be applied: state=%#v slice=%#v", detail.State.Plan, detail.Slices.Slices[0])
		}
	})

	t.Run("approval event already present", func(t *testing.T) {
		detail := startSliceDetail("")
		detail.Slices.Slices[0].Approval = &Approval{Required: true, Reason: "approval"}
		detail.Events = []Event{{Type: EventTypeSliceApproved, Timestamp: first, PlanID: "plan-a", SliceID: "001-a"}}

		_, appendEvent, err := markSliceApproved(detail, "001-a", "Seth", second)
		if err != nil {
			t.Fatal(err)
		}
		if appendEvent {
			t.Fatalf("expected existing approval event to suppress append")
		}
		if detail.Slices.Slices[0].Approval == nil || !detail.Slices.Slices[0].Approval.Approved {
			t.Fatalf("expected approval metadata to still be applied: %#v", detail.Slices.Slices[0].Approval)
		}
	})
}
