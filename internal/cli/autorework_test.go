package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/internal/rework"
	"github.com/iamseth/tao/internal/run"
)

type recordingAutoReworkRepository struct {
	planRunRepository
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
	store, ok := r.planRunRepository.(plan.ArtifactStore)
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

func TestReworkDriverAtomicallyRecordsReworkRound(t *testing.T) {
	now := time.Date(2026, 7, 14, 2, 30, 0, 0, time.UTC)
	planID := "20260714-0230-rework-round"
	detail, fingerprint := autoReworkTestDetail(planID, now)
	repo := newRecordingAutoReworkRepository(planID, detail)

	result, err := newReworkDriver(repo, func() time.Time { return now }).Decide(context.Background(), planID, 0, 0, "", 3, plan.AgentBudgetThresholds{})
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

func TestReworkDriverRecordsReworkStopped(t *testing.T) {
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

			result, err := newReworkDriver(repo, func() time.Time { return now }).Decide(context.Background(), planID, 0, test.attempts, test.previous(fingerprint), test.maxAttempts, plan.AgentBudgetThresholds{})
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

func TestReworkDriverRoundSettlementFailureFailsClosed(t *testing.T) {
	now := time.Date(2026, 7, 14, 2, 33, 0, 0, time.UTC)
	planID := "20260714-0233-rework-round-failure"
	detail, _ := autoReworkTestDetail(planID, now)
	beforeStatus := detail.State.Status
	beforeSlices := len(detail.Slices.Slices)
	repo := newRecordingAutoReworkRepository(planID, detail)
	repo.appendErr = errors.New("event journal unavailable")

	result, err := newReworkDriver(repo, func() time.Time { return now }).Decide(context.Background(), planID, 0, 0, "", 3, plan.AgentBudgetThresholds{})
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

func TestReworkDriverReopensOnThirdRecurringFileReworkReview(t *testing.T) {
	now := time.Date(2026, 7, 14, 2, 36, 0, 0, time.UTC)
	planID := "20260714-0236-recurring-file"
	detail, fingerprint := autoReworkTestDetail(planID, now)
	reworkSlices := []plan.Slice{
		{ID: "r101-internal-cli-autorework-go", Status: plan.StatusCompleted},
		{ID: "r201-internal-cli-autorework-go", Status: plan.StatusCompleted},
		{ID: "r301-internal-cli-autorework-go", Status: plan.StatusCompleted},
	}
	detail.Slices.Slices = append(detail.Slices.Slices, reworkSlices...)
	for i, slice := range reworkSlices {
		detail.State.Plan.CompletedSlices = append(detail.State.Plan.CompletedSlices, slice.ID)
		finding := detail.State.Plan.Review.Findings[0]
		finding.Line = 40 + i
		finding.Message = "round-specific finding"
		detail.Events = append(detail.Events, plan.Event{
			Type: plan.EventTypePlanReviewed, PlanID: planID, SliceID: slice.ID,
			Review: reworkReview(plan.ReviewVerdictChangesRequested, []plan.ReviewFinding{finding}),
		})
	}
	repo := newRecordingAutoReworkRepository(planID, detail)

	result, err := newReworkDriver(repo, func() time.Time { return now }).Decide(context.Background(), planID, 0, 1, "different-fingerprint", 5, plan.AgentBudgetThresholds{})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Reworked || result.Round != 4 || result.Fingerprint != fingerprint || result.StopKind != rework.StopKindNone || result.StopReason != "" {
		t.Fatalf("automatic rework result = %+v", result)
	}
	want := []rework.Advisory{{Kind: rework.AdvisoryKindFileRecurrence, Location: "internal/cli/autorework.go", Rounds: []int{1, 2, 3}}}
	if !reflect.DeepEqual(result.Advisories, want) {
		t.Fatalf("advisories = %#v, want %#v", result.Advisories, want)
	}
	if len(detail.Slices.Slices) != 5 || detail.State.Status != plan.StatusInProgress {
		t.Fatalf("recurring-file advisory prevented reopen: status=%q slices=%+v", detail.State.Status, detail.Slices.Slices)
	}
	var reopened, round plan.Event
	for _, event := range repo.events {
		switch event.Type {
		case plan.EventTypeReworkStopped:
			t.Fatalf("location advisory wrote stop evidence: %+v", event)
		case plan.EventTypePlanReopened:
			reopened = event
		case plan.EventTypeReworkRound:
			round = event
		}
	}
	if round.Type != plan.EventTypeReworkRound || round.PlanID != planID || round.Round != 4 || round.Attempts != 4 || round.Fingerprint != fingerprint {
		t.Fatalf("rework_round event = %+v", round)
	}
	if reopened.Type != plan.EventTypePlanReopened || reopened.MutationID == "" || reopened.MutationID != round.MutationID {
		t.Fatalf("reopen events not settled together: reopened=%+v round=%+v", reopened, round)
	}
}

func TestReworkDriverStopSettlementFailureFailsClosed(t *testing.T) {
	now := time.Date(2026, 7, 14, 2, 40, 0, 0, time.UTC)
	planID := "20260714-0240-append-failure"
	detail, fingerprint := autoReworkTestDetail(planID, now)
	repo := newRecordingAutoReworkRepository(planID, detail)
	repo.appendErr = errors.New("event journal unavailable")

	result, err := newReworkDriver(repo, func() time.Time { return now }).Decide(context.Background(), planID, 0, 3, "", 3, plan.AgentBudgetThresholds{})
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

func TestReworkDriverSilentDeclinesAppendNothing(t *testing.T) {
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

			result, err := newReworkDriver(repo, func() time.Time { return now }).Decide(context.Background(), planID, 0, 0, "", 3, plan.AgentBudgetThresholds{})
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

// Fail only the selected output path, so advisory best-effort handling cannot
// accidentally mask mandatory progress errors.
type reworkSelectiveWriter struct {
	bytes.Buffer
	failOn   string
	failure  error
	rejected int
}

func (w *reworkSelectiveWriter) Write(p []byte) (int, error) {
	if w.failOn != "" && strings.Contains(string(p), w.failOn) {
		w.rejected++
		return 0, w.failure
	}
	return w.Buffer.Write(p)
}

func TestRunLocationAdvisoriesContinueThroughDirectExecution(t *testing.T) {
	for _, test := range []struct {
		name      string
		failOn    string
		execFail  bool
		cap       bool
		stalled   bool
		budget    bool
		wantCalls int
		wantError string
		wantStop  rework.StopKind
	}{
		{name: "sorted warnings and completion", wantCalls: 5},
		{name: "advisory write failure", failOn: "location advisory only", wantCalls: 5},
		{name: "mandatory progress failure", failOn: "Plan reopened for rework round 3", wantCalls: 3, wantError: "output unavailable"},
		{name: "execution failure after advisory", execFail: true, wantCalls: 4, wantError: "execution failed"},
		{name: "cap precedes equality budget and locations", cap: true, stalled: true, budget: true, wantCalls: 3, wantError: "attempt cap reached", wantStop: rework.StopKindCapExhausted},
		{name: "equality precedes budget and locations", stalled: true, budget: true, wantCalls: 3, wantError: "GOING IN CIRCLES", wantStop: rework.StopKindFindingsStalled},
		{name: "budget precedes locations", budget: true, wantCalls: 3, wantError: "plan agent budget warning", wantStop: rework.StopKindPlanBudget},
	} {
		t.Run(test.name, func(t *testing.T) {
			clearTaoEnv(t)
			t.Setenv("TAO_DATA_HOME", t.TempDir())
			now := time.Date(2026, 9, 24, 23, 0, 0, 0, time.UTC)
			const planID = "20260924-2300-advisory"
			detail, _ := autoReworkTestDetail(planID, now)
			detail.Dir = t.TempDir()
			repo := newRecordingAutoReworkRepository(planID, detail)
			outputErr := errors.New("output unavailable")
			out := &reworkSelectiveWriter{failOn: test.failOn, failure: outputErr}
			calls := 0
			oldExecutor := executeSinglePlan
			t.Cleanup(func() { executeSinglePlan = oldExecutor })
			executeSinglePlan = func(run.Service, context.Context, run.Request) error {
				calls++
				if test.execFail && calls == 4 {
					return errors.New("execution failed")
				}
				if calls > 1 && (detail.State.Status != plan.StatusInProgress || len(detail.State.Plan.PendingSlices) == 0) {
					t.Fatalf("execution without successful reopening: %+v", detail.State.Plan)
				}
				if calls == 4 && test.failOn == "" && !strings.Contains(out.String(), "rework continues with round 3") {
					t.Fatal("execution resumed before advisory delivery")
				}
				sliceID := "001-work"
				if len(detail.State.Plan.PendingSlices) > 0 {
					sliceID = detail.State.Plan.PendingSlices[0]
				}
				for i := range detail.Slices.Slices {
					detail.Slices.Slices[i].Status = plan.StatusCompleted
				}
				detail.State.Plan.CompletedSlices = append(detail.State.Plan.CompletedSlices, detail.State.Plan.PendingSlices...)
				detail.State.Plan.PendingSlices = nil
				detail.State.Plan.CurrentSlice = nil
				if calls == 5 {
					detail.State.Status = plan.StatusReviewed
					detail.State.Plan.Review = reworkReview(plan.ReviewVerdictApprove, nil)
					return nil
				}
				messageRound := calls
				if test.stalled && calls == 3 {
					messageRound = 2
				}
				findings := []plan.ReviewFinding{
					{Severity: "major", File: "internal/z/z.go", Line: 47, Message: fmt.Sprintf("z finding %d", messageRound)},
					{Severity: "major", File: "internal/a/a.go", Line: 12, Message: fmt.Sprintf("a finding %d", messageRound)},
				}
				detail.State.Status = plan.StatusChangesRequested
				detail.State.Plan.Review = reworkReview(plan.ReviewVerdictChangesRequested, findings)
				detail.Events = append(detail.Events, plan.Event{Type: plan.EventTypePlanReviewed, PlanID: planID, SliceID: sliceID, Review: detail.State.Plan.Review})
				if test.budget && calls == 3 {
					detail.Events = append(detail.Events, plan.Event{Type: plan.EventTypeAgentMetrics, PlanID: planID, Metrics: &plan.AgentMetrics{SessionID: "budget", ToolCalls: 1000}})
				}
				return nil
			}
			args := []string{"--no-run-header"}
			if test.cap {
				args = append(args, "--max-rework-attempts", "2")
			}
			args = append(args, planID)
			err := (App{Out: out, Now: func() time.Time { return now }}).run(context.Background(), repo, args)
			if test.wantError == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("error = %v, want %q", err, test.wantError)
			}
			if calls != test.wantCalls {
				t.Fatalf("executions = %d, want %d", calls, test.wantCalls)
			}
			if test.failOn != "" && out.rejected == 0 {
				t.Fatal("output failure was not exercised")
			}
			if test.wantStop != rework.StopKindNone || test.failOn != "" {
				if strings.Contains(out.String(), "location advisory") {
					t.Fatalf("unexpected advisory output: %s", out.String())
				}
			} else {
				want := "Automatic rework location advisory only: rework continues with round 3.\n- anchor internal/a/a.go:12 (rounds [1 2])\n- anchor internal/z/z.go:47 (rounds [1 2])\n"
				if !strings.Contains(out.String(), "Plan reopened for rework round 3\n"+want) {
					t.Fatalf("missing ordered anchor advisory after progress: %s", out.String())
				}
				if !test.execFail {
					want = "Automatic rework location advisory only: rework continues with round 4.\n- anchor internal/a/a.go:12 (rounds [1 2 3])\n- anchor internal/z/z.go:47 (rounds [1 2 3])\n- file internal/a/a.go (rounds [1 2 3])\n- file internal/z/z.go (rounds [1 2 3])\n"
					if !strings.Contains(out.String(), want) {
						t.Fatalf("missing sorted anchor/file advisory: %s", out.String())
					}
				}
			}
			for _, unwanted := range []string{"STOPPED", "reversal", "stalled", "GOING IN CIRCLES"} {
				if strings.Contains(out.String(), unwanted) {
					t.Errorf("live output claims %q: %s", unwanted, out.String())
				}
			}
			var stop plan.Event
			for _, event := range repo.events {
				if event.Type == plan.EventTypeReworkStopped {
					if test.wantStop == rework.StopKindNone {
						t.Fatalf("advisory wrote stop event: %+v", event)
					}
					stop = event
				}
			}
			if test.wantStop != rework.StopKindNone {
				if stop.Type != plan.EventTypeReworkStopped || rework.StopKindForPersistedReason(stop.Reason) != test.wantStop || stop.Round != 2 || stop.Attempts != 2 {
					t.Fatalf("stop evidence = %+v, want %s at round 2", stop, test.wantStop)
				}
				if detail.State.Status != plan.StatusChangesRequested || len(detail.State.Plan.Review.Findings) != 2 {
					t.Fatal("hard stop changed latest review")
				}
			}
		})
	}
}

func TestRunHistoricalLocationStopsStillRequireExplicitRestart(t *testing.T) {
	for _, reason := range []string{
		`automatic rework stopped on repeated finding anchors across review rounds: [{"anchor":"internal/cli/autorework.go:45","rounds":[1,2]}]`,
		`automatic rework stalled on files recurring in three review rounds: [{"file":"internal/cli/autorework.go","rounds":[1,2,3]}]`,
		`automatic rework stalled on files recurring across three consecutive reviews: ["internal/cli/autorework.go"]`,
	} {
		for _, restart := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/restart=%t", rework.StopKindForPersistedReason(reason), restart), func(t *testing.T) {
				clearTaoEnv(t)
				t.Setenv("TAO_DATA_HOME", t.TempDir())
				now := time.Date(2026, 9, 24, 23, 0, 0, 0, time.UTC)
				const planID = "20260924-2300-historical-stop"
				detail, _ := autoReworkTestDetail(planID, now)
				detail.Dir = t.TempDir()
				for round := 1; round <= 3; round++ {
					id := fmt.Sprintf("r%d01-fix", round)
					detail.Slices.Slices = append(detail.Slices.Slices, plan.Slice{ID: id, Status: plan.StatusCompleted})
					detail.State.Plan.CompletedSlices = append(detail.State.Plan.CompletedSlices, id)
					detail.Events = append(detail.Events, plan.Event{Type: plan.EventTypePlanReviewed, PlanID: planID, SliceID: id, Review: detail.State.Plan.Review})
				}
				stop := plan.Event{Type: plan.EventTypeReworkStopped, PlanID: planID, Round: 3, Attempts: 3, Reason: reason}
				detail.Events = append(detail.Events, stop)
				repo := newRecordingAutoReworkRepository(planID, detail)
				var out bytes.Buffer
				calls := 0
				oldExecutor := executeSinglePlan
				t.Cleanup(func() { executeSinglePlan = oldExecutor })
				executeSinglePlan = func(run.Service, context.Context, run.Request) error {
					calls++
					if detail.State.Status != plan.StatusInProgress || rework.RoundCount(detail) != 4 {
						t.Fatal("restart did not reopen before execution")
					}
					// A fresh distinct review must still stop at the new one-round cap.
					for i := range detail.Slices.Slices {
						detail.Slices.Slices[i].Status = plan.StatusCompleted
					}
					detail.State.Plan.CompletedSlices = append(detail.State.Plan.CompletedSlices, detail.State.Plan.PendingSlices...)
					detail.State.Plan.PendingSlices = nil
					detail.State.Status = plan.StatusChangesRequested
					detail.State.Plan.Review = reworkReview(plan.ReviewVerdictChangesRequested, []plan.ReviewFinding{{File: "internal/cli/autorework.go", Line: 45, Message: "a new finding"}})
					return nil
				}
				args := []string{"--no-run-header", "--max-rework-attempts", "1"}
				if restart {
					args = append(args, "--rework-restart")
				}
				args = append(args, planID)
				err := (App{Out: &out, Now: func() time.Time { return now }}).run(context.Background(), repo, args)
				if restart {
					if calls != 1 || err == nil || !strings.Contains(err.Error(), "cap exhausted after 1 cycles") {
						t.Fatalf("bounded restart: calls=%d error=%v", calls, err)
					}
					var reopened plan.Event
					for _, event := range repo.events {
						if event.Type == plan.EventTypeReworkRound {
							reopened = event
						}
					}
					if reopened.Round != 4 || reopened.Attempts != 1 {
						t.Fatalf("fresh baseline evidence = %+v", reopened)
					}
				} else {
					if calls != 0 || err == nil || !strings.Contains(err.Error(), "--rework-restart") || !strings.Contains(err.Error(), reason) {
						t.Fatalf("historical stop refusal: calls=%d error=%v", calls, err)
					}
					if rework.RoundCount(detail) != 3 || detail.State.Status != plan.StatusChangesRequested {
						t.Fatal("refusal changed historical plan")
					}
					for _, event := range repo.events {
						if event.Type == plan.EventTypeReworkRound || event.Type == plan.EventTypeReworkStopped || event.Type == plan.EventTypePlanReopened {
							t.Fatalf("refusal appended lifecycle evidence: %+v", event)
						}
					}
				}
				if strings.Contains(out.String(), "location advisory") {
					t.Fatalf("historical location window leaked into live warnings: %s", out.String())
				}
				var historical plan.Event
				for _, event := range detail.Events {
					if event.Type == plan.EventTypeReworkStopped && event.Reason == reason {
						historical = event
					}
				}
				if !reflect.DeepEqual(historical, stop) {
					t.Fatal("historical stop evidence changed")
				}
			})
		}
	}
}

func newRecordingAutoReworkRepository(planID string, detail *plan.PlanDetail) *recordingAutoReworkRepository {
	return &recordingAutoReworkRepository{planRunRepository: fakeRepository{details: map[string]*plan.PlanDetail{planID: detail}}}
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
