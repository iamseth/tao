package cli

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/internal/rework"
)

type recordingAutoReworkRepository struct {
	queueRepository
	events    []plan.Event
	dirs      []string
	appendErr error
}

func (r *recordingAutoReworkRepository) AppendEvent(dir string, event plan.Event) error {
	r.dirs = append(r.dirs, dir)
	r.events = append(r.events, event)
	return r.appendErr
}

func (r *recordingAutoReworkRepository) PlanRecord(detail *plan.PlanDetail) (*plan.PlanRecord, error) {
	dir := detail.Dir
	if dir == "" {
		dir = "/plantest/" + detail.State.Plan.ID
		detail.Dir = dir
	}
	store, ok := r.queueRepository.(plan.ArtifactStore)
	if !ok {
		return nil, errors.New("recording repository has no artifact store")
	}
	return plan.NewPlanRecordWithStore(recordingAutoReworkStore{ArtifactStore: store, repo: r}, dir, detail)
}

type recordingAutoReworkStore struct {
	plan.ArtifactStore
	repo *recordingAutoReworkRepository
}

func (s recordingAutoReworkStore) AppendEvent(dir string, event plan.Event) error {
	return s.repo.AppendEvent(dir, event)
}

func TestPlanAutoReworkerAtomicallyRecordsReworkRound(t *testing.T) {
	now := time.Date(2026, 7, 14, 2, 30, 0, 0, time.UTC)
	planID := "20260714-0230-rework-round"
	detail, fingerprint := autoReworkTestDetail(planID, now)
	repo := newRecordingAutoReworkRepository(planID, detail)

	result, err := planAutoReworker(repo, func() time.Time { return now })(context.Background(), planID, 0, 0, "", 3)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Reworked || result.Round != 1 || result.Fingerprint != fingerprint {
		t.Fatalf("automatic rework result = %+v", result)
	}
	var event plan.Event
	eventDir := ""
	reopenedIndex, roundIndex := -1, -1
	var reopened plan.Event
	for i, candidate := range repo.events {
		switch candidate.Type {
		case plan.EventTypePlanReopened:
			reopened, reopenedIndex = candidate, i
		case plan.EventTypeReworkRound:
			event, eventDir, roundIndex = candidate, repo.dirs[i], i
		}
	}
	if event.Type != plan.EventTypeReworkRound || event.PlanID != planID || event.Round != 1 || event.Attempts != 1 || event.Fingerprint != fingerprint {
		t.Fatalf("rework round event = %+v", event)
	}
	if event.Timestamp != now || event.Message != "Automatic rework round 1 (attempt 1 of 3)" {
		t.Fatalf("rework round event metadata = %+v", event)
	}
	if eventDir != detail.Dir {
		t.Fatalf("rework_round dir = %q, want %q", eventDir, detail.Dir)
	}
	if reopenedIndex < 0 || roundIndex != reopenedIndex+1 || reopened.MutationID == "" || reopened.MutationID != event.MutationID {
		t.Fatalf("automatic reopen events are not one ordered mutation: reopened=%+v round=%+v", reopened, event)
	}
}

func TestPlanAutoReworkerRecordsReworkStopped(t *testing.T) {
	now := time.Date(2026, 7, 14, 2, 35, 0, 0, time.UTC)
	const stalledReason = "automatic rework stalled on equivalent consecutive findings"

	for _, test := range []struct {
		name        string
		attempts    int
		previous    func(string) string
		maxAttempts int
		wantKind    rework.StopKind
		wantReason  string
		wantOutput  []string
		doNotWant   []string
	}{
		{
			name: "cap exhausted", attempts: 2, previous: func(string) string { return "" }, maxAttempts: 2,
			wantKind:   rework.StopKindCapExhausted,
			wantReason: "automatic rework cap exhausted after 2 cycles",
			wantOutput: []string{"Automatic rework stopped: attempt cap reached", "automatic rework cap exhausted after 2 cycles"},
			doNotWant:  []string{"!!!!!!!!!!!!!!!!", "GOING IN CIRCLES", "preserve automatic rework history"},
		},
		{
			name: "fingerprint stall", attempts: 1, previous: func(fingerprint string) string { return fingerprint }, maxAttempts: 3,
			wantKind:   rework.StopKindFindingsStalled,
			wantReason: stalledReason,
			wantOutput: []string{"!!!!!!!!!!!!!!!!", "THE LOOP IS GOING IN CIRCLES", "internal/cli/autorework.go:45", "preserve automatic rework history", "append a typed plan event", "before re-running"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			planID := "20260714-0235-" + test.name
			detail, fingerprint := autoReworkTestDetail(planID, now)
			repo := newRecordingAutoReworkRepository(planID, detail)

			result, err := planAutoReworker(repo, func() time.Time { return now })(context.Background(), planID, 0, test.attempts, test.previous(fingerprint), test.maxAttempts)
			if err != nil {
				t.Fatal(err)
			}
			if result.StopKind != test.wantKind || result.StopReason != test.wantReason || result.Round != 0 || result.Fingerprint != fingerprint {
				t.Fatalf("automatic rework result = %+v", result)
			}
			stopOutput := rework.FormatStopMessage(result)
			for _, want := range test.wantOutput {
				if !strings.Contains(stopOutput, want) {
					t.Errorf("stop output %q does not contain %q", stopOutput, want)
				}
			}
			for _, unwanted := range test.doNotWant {
				if strings.Contains(stopOutput, unwanted) {
					t.Errorf("stop output %q unexpectedly contains %q", stopOutput, unwanted)
				}
			}
			var event plan.Event
			for _, candidate := range repo.events {
				if candidate.Type == plan.EventTypeReworkStopped {
					event = candidate
				}
			}
			if event.Type != plan.EventTypeReworkStopped || event.PlanID != planID || event.Round != 0 || event.Attempts != test.attempts || event.Fingerprint != fingerprint || event.Reason != test.wantReason || event.Message != test.wantReason {
				t.Fatalf("rework stopped event = %+v", event)
			}
		})
	}
}

func TestPlanAutoReworkerRoundSettlementFailureFailsClosed(t *testing.T) {
	now := time.Date(2026, 7, 14, 2, 33, 0, 0, time.UTC)
	planID := "20260714-0233-rework-round-failure"
	detail, _ := autoReworkTestDetail(planID, now)
	beforeStatus := detail.State.Status
	beforeSlices := len(detail.Slices.Slices)
	repo := newRecordingAutoReworkRepository(planID, detail)
	repo.appendErr = errors.New("event journal unavailable")

	result, err := planAutoReworker(repo, func() time.Time { return now })(context.Background(), planID, 0, 0, "", 3)
	if err == nil {
		t.Fatal("automatic rework unexpectedly published an unsettled round")
	}
	for _, want := range []string{"automatic rework mutation failed", "record automatic rework round", "event journal unavailable"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("automatic rework error %q does not contain %q", err, want)
		}
	}
	if !reflect.DeepEqual(result, rework.Decision{}) {
		t.Fatalf("automatic rework result = %+v, want zero decision", result)
	}
	if detail.State.Status != beforeStatus || len(detail.Slices.Slices) != beforeSlices {
		t.Fatalf("failed round published mutation: status=%q slices=%+v", detail.State.Status, detail.Slices.Slices)
	}
}

func TestPlanAutoReworkerStopsOnThirdRecurringFileReview(t *testing.T) {
	now := time.Date(2026, 7, 14, 2, 36, 0, 0, time.UTC)
	planID := "20260714-0236-recurring-file"
	detail, fingerprint := autoReworkTestDetail(planID, now)
	detail.Slices.Slices = append(detail.Slices.Slices,
		plan.Slice{ID: "r101-internal-cli-autorework-go", Status: plan.StatusCompleted, ExpectedFiles: []string{"internal/cli/autorework.go"}},
		plan.Slice{ID: "r201-internal-cli-autorework-go", Status: plan.StatusCompleted, ExpectedFiles: []string{"./internal/cli/autorework.go"}},
	)
	detail.State.Plan.CompletedSlices = append(detail.State.Plan.CompletedSlices,
		"r101-internal-cli-autorework-go",
		"r201-internal-cli-autorework-go",
	)
	repo := newRecordingAutoReworkRepository(planID, detail)

	result, err := planAutoReworker(repo, func() time.Time { return now })(context.Background(), planID, 0, 1, "different-fingerprint", 5)
	if err != nil {
		t.Fatal(err)
	}
	if result.Reworked || result.Round != 2 || result.Fingerprint != fingerprint || result.StopKind != rework.StopKindRecurringFiles {
		t.Fatalf("automatic rework result = %+v", result)
	}
	if want := []string{"internal/cli/autorework.go"}; !reflect.DeepEqual(result.RecurringFiles, want) {
		t.Fatalf("recurring files = %#v, want %#v", result.RecurringFiles, want)
	}
	for _, want := range []string{"THE SAME FILES KEEP RECURRING", "- internal/cli/autorework.go", "internal/cli/autorework.go:45", "preserve automatic rework history", "append a typed plan event"} {
		if output := rework.FormatStopMessage(result); !strings.Contains(output, want) {
			t.Errorf("stop output %q does not contain %q", output, want)
		}
	}
	if len(detail.Slices.Slices) != 3 || detail.State.Status != plan.StatusChangesRequested {
		t.Fatalf("recurring-file stop crossed the reopen boundary: status=%q slices=%+v", detail.State.Status, detail.Slices.Slices)
	}
	var stopped *plan.Event
	for i := range repo.events {
		if repo.events[i].Type == plan.EventTypeReworkStopped {
			stopped = &repo.events[i]
		}
	}
	if stopped == nil || stopped.PlanID != planID || stopped.Round != 2 || stopped.Attempts != 2 || stopped.Fingerprint != fingerprint || rework.StopKindForPersistedReason(stopped.Reason) != rework.StopKindRecurringFiles {
		t.Fatalf("rework_stopped event = %+v", stopped)
	}
}

func TestPlanAutoReworkerRefusesFreshBudgetAfterPersistedStop(t *testing.T) {
	now := time.Date(2026, 7, 14, 2, 38, 0, 0, time.UTC)
	planID := "20260714-0238-stopped"
	detail, _ := autoReworkTestDetail(planID, now)
	detail.Events = []plan.Event{{Type: plan.EventTypeReworkStopped, Reason: "automatic rework stalled on equivalent consecutive findings"}}
	repo := newRecordingAutoReworkRepository(planID, detail)

	result, err := planAutoReworker(repo, func() time.Time { return now })(context.Background(), planID, 0, 0, "", 5)
	if err == nil {
		t.Fatal("automatic rework unexpectedly granted a fresh budget")
	}
	for _, want := range []string{"THE LOOP IS GOING IN CIRCLES", "preserve automatic rework history", "--rework-restart"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("restart refusal %q does not contain %q", err, want)
		}
	}
	if !reflect.DeepEqual(result, rework.Decision{}) {
		t.Fatalf("automatic rework result = %+v, want zero result", result)
	}
	if len(repo.events) != 0 {
		t.Fatalf("restart refusal appended events: %+v", repo.events)
	}
}

func TestPlanAutoReworkerStopSettlementFailureFailsClosed(t *testing.T) {
	now := time.Date(2026, 7, 14, 2, 40, 0, 0, time.UTC)
	planID := "20260714-0240-append-failure"
	detail, fingerprint := autoReworkTestDetail(planID, now)
	repo := newRecordingAutoReworkRepository(planID, detail)
	repo.appendErr = errors.New("event journal unavailable")

	result, err := planAutoReworker(repo, func() time.Time { return now })(context.Background(), planID, 0, 3, "", 3)
	if err == nil {
		t.Fatal("automatic rework unexpectedly published an unsettled stop")
	}
	for _, want := range []string{"automatic rework mutation failed", "event journal unavailable", "Automatic rework stopped: attempt cap reached"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("automatic rework error %q does not contain %q", err, want)
		}
	}
	if !reflect.DeepEqual(result, rework.Decision{}) {
		t.Fatalf("automatic rework result = %+v, want zero decision", result)
	}
	_ = fingerprint
}

func TestPlanAutoReworkerSilentDeclinesAppendNothing(t *testing.T) {
	now := time.Date(2026, 7, 14, 2, 45, 0, 0, time.UTC)

	for _, test := range []struct {
		name   string
		mutate func(*plan.PlanDetail)
	}{
		{name: "ineligible plan", mutate: func(detail *plan.PlanDetail) { detail.State.Status = plan.StatusReviewed }},
		{name: "no generated slices", mutate: func(detail *plan.PlanDetail) { detail.State.Plan.Review.Findings[0].File = "../outside.go" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			planID := "20260714-0245-" + test.name
			detail, _ := autoReworkTestDetail(planID, now)
			test.mutate(detail)
			repo := newRecordingAutoReworkRepository(planID, detail)

			result, err := planAutoReworker(repo, func() time.Time { return now })(context.Background(), planID, 0, 0, "", 3)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(result, rework.Decision{}) {
				t.Fatalf("automatic rework result = %+v, want silent decline", result)
			}
			if len(repo.events) != 0 {
				t.Fatalf("silent decline appended events: %+v", repo.events)
			}
		})
	}
}

func newRecordingAutoReworkRepository(planID string, detail *plan.PlanDetail) *recordingAutoReworkRepository {
	return &recordingAutoReworkRepository{queueRepository: fakeRepository{details: map[string]*plan.PlanDetail{planID: detail}}}
}

func autoReworkTestDetail(planID string, now time.Time) (*plan.PlanDetail, string) {
	finding := plan.ReviewFinding{
		Severity:   "major",
		File:       "internal/cli/autorework.go",
		Line:       45,
		Message:    "preserve automatic rework history",
		Suggestion: "append a typed plan event",
	}
	detail := &plan.PlanDetail{
		Dir: "/plans/" + planID,
		State: plan.State{
			Status:    plan.StatusChangesRequested,
			CreatedAt: now.Add(-time.Hour),
			UpdatedAt: now,
			Plan: plan.PlanState{
				ID:              planID,
				CompletedSlices: []string{"001-work"},
				Review:          reworkReview(plan.ReviewVerdictChangesRequested, []plan.ReviewFinding{finding}),
				Timing:          plan.PlanTiming{StartedAt: new(now), CompletedAt: new(now), LastActivityAt: new(now)},
			},
		},
		Slices: plan.SlicesFile{PlanID: planID, Slices: []plan.Slice{{ID: "001-work", Status: plan.StatusCompleted}}},
	}
	return detail, rework.ReworkFindingsFingerprint([]plan.ReviewFinding{finding})
}
