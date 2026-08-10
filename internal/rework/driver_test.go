package rework

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/iamseth/tao/internal/plan"
)

type driverRecord struct {
	detail *plan.PlanDetail
}

func (r *driverRecord) Detail() *plan.PlanDetail { return r.detail }

func (r *driverRecord) Reopen(slices []plan.Slice, now time.Time) error {
	_, err := plan.Reopen(r.detail, slices, now)
	return err
}

func TestDriverDecideReturnsZeroForNonActionablePlan(t *testing.T) {
	driver := Driver{Resolve: func(context.Context, string) (*plan.PlanDetail, error) {
		return &plan.PlanDetail{State: plan.State{Status: plan.StatusReviewed}}, nil
	}}

	got, err := driver.Decide(context.Background(), "plan", 0, 0, "", 5)
	if err != nil {
		t.Fatalf("Decide returned error: %v", err)
	}
	if !reflect.DeepEqual(got, Decision{}) {
		t.Fatalf("Decide = %+v, want zero decision", got)
	}
}

func TestDriverDecideStopsAtCapUsingRoundBaseline(t *testing.T) {
	detail := actionableDriverDetail(5)
	driver := Driver{Resolve: fixedDriverResolver(detail)}

	got, err := driver.Decide(context.Background(), "plan", 2, 1, "", 3)
	if err != nil {
		t.Fatalf("Decide returned error: %v", err)
	}
	if got.StopKind != StopKindCapExhausted || got.StopReason != "automatic rework cap exhausted after 3 cycles" {
		t.Fatalf("stop = (%q, %q)", got.StopKind, got.StopReason)
	}
	if got.Round != 5 {
		t.Fatalf("round = %d, want 5", got.Round)
	}
	if !reflect.DeepEqual(got.Findings, ReviewFindings(detail)) {
		t.Fatalf("blocking findings = %+v, want %+v", got.Findings, ReviewFindings(detail))
	}
}

func TestDriverDecideStopsOnEquivalentConsecutiveFindings(t *testing.T) {
	detail := actionableDriverDetail(1)
	finding := ReviewFindings(detail)[0]
	fingerprint := ReworkFindingsFingerprint([]plan.ReviewFinding{{
		Severity:   " MAJOR ",
		File:       "./" + finding.File,
		Line:       finding.Line,
		Message:    strings.ToUpper(finding.Message),
		Suggestion: "  ",
	}})
	driver := Driver{Resolve: fixedDriverResolver(detail)}

	got, err := driver.Decide(context.Background(), "plan", 1, 0, fingerprint, 5)
	if err != nil {
		t.Fatalf("Decide returned error: %v", err)
	}
	if got.StopKind != StopKindFindingsStalled || got.StopReason != "automatic rework stalled on equivalent consecutive findings" {
		t.Fatalf("stop = (%q, %q)", got.StopKind, got.StopReason)
	}
	if got.Fingerprint != fingerprint {
		t.Fatalf("fingerprint = %q, want %q", got.Fingerprint, fingerprint)
	}
	if !reflect.DeepEqual(got.Findings, ReviewFindings(detail)) {
		t.Fatalf("blocking findings = %+v, want %+v", got.Findings, ReviewFindings(detail))
	}
}

func TestDriverDecideContinuesForDistinctSameFileFindings(t *testing.T) {
	detail := actionableDriverDetail(1)
	detail.State.Plan.Review.Findings[0] = plan.ReviewFinding{
		Severity: "major", File: "store/file.go", Line: 42,
		Message: "Warp leaves the write transaction open", Suggestion: "close the transaction after writing",
	}
	previous := ReworkFindingsFingerprint([]plan.ReviewFinding{{
		Severity: "major", File: "store/file.go", Line: 42,
		Message: "Warp drops the recovered record", Suggestion: "retain the recovered record",
	}})
	driver := Driver{
		Resolve: fixedDriverResolver(detail),
		Record:  func(detail *plan.PlanDetail) (Record, error) { return &driverRecord{detail: detail}, nil },
	}

	got, err := driver.Decide(context.Background(), "plan", 1, 0, previous, 5)
	if err != nil {
		t.Fatalf("Decide returned error: %v", err)
	}
	if !got.Reworked || got.StopReason != "" || got.Fingerprint == previous {
		t.Fatalf("distinct same-file decision = %+v, want another rework round", got)
	}
}

func TestDriverDecideStopsOnRecurringFilesWithoutMutation(t *testing.T) {
	detail, previous := recurringDriverDetail()
	beforeStatus := detail.State.Status
	beforeSlices := slices.Clone(detail.Slices.Slices)
	recordCalled := false
	var stoppedEvent *plan.Event
	driver := Driver{
		Resolve: fixedDriverResolver(detail),
		Record: func(detail *plan.PlanDetail) (Record, error) {
			recordCalled = true
			return &driverRecord{detail: detail}, nil
		},
		Now: func() time.Time { return time.Date(2026, 7, 29, 22, 0, 0, 0, time.UTC) },
		AppendEvent: func(_ string, event plan.Event) error {
			if event.Type == plan.EventTypeReworkStopped {
				stopped := event
				stoppedEvent = &stopped
			}
			return nil
		},
	}

	got, err := driver.Decide(context.Background(), "plan", 0, 0, previous, 5)
	if err != nil {
		t.Fatalf("Decide returned error: %v", err)
	}
	wantFiles := []string{"store/file.go"}
	if got.Reworked || got.StopKind != StopKindRecurringFiles || !slices.Equal(got.RecurringFiles, wantFiles) {
		t.Fatalf("recurring-file decision = %+v", got)
	}
	if got.StopReason != recurringFilesStopReason(wantFiles) {
		t.Fatalf("stop reason = %q", got.StopReason)
	}
	if got.Fingerprint == previous || !reflect.DeepEqual(got.Findings, ReviewFindings(detail)) {
		t.Fatalf("recurring-file evidence = %+v, previous fingerprint %q", got, previous)
	}
	if recordCalled {
		t.Fatal("recurring-file stop crossed the plan mutation boundary")
	}
	if detail.State.Status != beforeStatus || !reflect.DeepEqual(detail.Slices.Slices, beforeSlices) {
		t.Fatalf("recurring-file stop mutated plan detail: status=%q slices=%+v", detail.State.Status, detail.Slices.Slices)
	}
	if stoppedEvent == nil {
		t.Fatal("recurring-file stop did not append rework_stopped")
	}
	if stoppedEvent.PlanID != "plan" || stoppedEvent.Round != 2 || stoppedEvent.Attempts != 2 || stoppedEvent.Fingerprint != got.Fingerprint || stoppedEvent.Reason != got.StopReason || stoppedEvent.Message != got.StopReason {
		t.Fatalf("rework_stopped event = %+v", *stoppedEvent)
	}
}

func TestDriverDecideStopPrecedenceOverRecurringFiles(t *testing.T) {
	tests := []struct {
		name        string
		maxAttempts int
		previous    func(*plan.PlanDetail, string) string
		wantKind    StopKind
	}{
		{
			name:        "custom cap",
			maxAttempts: 2,
			previous:    func(_ *plan.PlanDetail, previous string) string { return previous },
			wantKind:    StopKindCapExhausted,
		},
		{
			name:        "exact fingerprint",
			maxAttempts: 5,
			previous: func(detail *plan.PlanDetail, _ string) string {
				return ReworkFindingsFingerprint(ReviewFindings(detail))
			},
			wantKind: StopKindFindingsStalled,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			detail, previous := recurringDriverDetail()
			driver := Driver{
				Resolve: fixedDriverResolver(detail),
				Record: func(*plan.PlanDetail) (Record, error) {
					t.Fatal("stop precedence crossed the plan mutation boundary")
					return nil, nil
				},
			}

			got, err := driver.Decide(context.Background(), "plan", 0, 0, test.previous(detail, previous), test.maxAttempts)
			if err != nil {
				t.Fatalf("Decide returned error: %v", err)
			}
			if got.StopKind != test.wantKind || len(got.RecurringFiles) != 0 {
				t.Fatalf("decision = %+v, want earlier stop kind %q", got, test.wantKind)
			}
		})
	}
}

func TestDriverDecideLegacyFingerprintPermitsOneAdditionalRound(t *testing.T) {
	first := actionableDriverDetail(1)
	findings := ReviewFindings(first)
	legacy := BatchLocationFindingsFingerprint(findings)
	driver := Driver{
		Resolve: fixedDriverResolver(first),
		Record:  func(detail *plan.PlanDetail) (Record, error) { return &driverRecord{detail: detail}, nil },
	}

	upgraded, err := driver.Decide(context.Background(), "plan", 1, 0, legacy, 5)
	if err != nil {
		t.Fatalf("Decide with legacy fingerprint returned error: %v", err)
	}
	if !upgraded.Reworked || upgraded.StopReason != "" || upgraded.Fingerprint == legacy {
		t.Fatalf("legacy fingerprint decision = %+v, want one upgraded round", upgraded)
	}

	repeated := actionableDriverDetail(2)
	repeated.State.Plan.Review.Findings = findings
	driver = Driver{Resolve: fixedDriverResolver(repeated)}
	stopped, err := driver.Decide(context.Background(), "plan", 1, 1, upgraded.Fingerprint, 5)
	if err != nil {
		t.Fatalf("Decide with upgraded fingerprint returned error: %v", err)
	}
	if stopped.StopKind != StopKindFindingsStalled || stopped.Fingerprint != upgraded.Fingerprint {
		t.Fatalf("upgraded repeat decision = %+v, want equivalent-finding stop", stopped)
	}
}

func TestStopKindForPersistedReason(t *testing.T) {
	tests := []struct {
		name   string
		reason string
		want   StopKind
	}{
		{name: "empty", want: StopKindNone},
		{name: "cap", reason: "automatic rework cap exhausted after 5 cycles", want: StopKindCapExhausted},
		{name: "stalled findings", reason: equivalentFindingsStopReason, want: StopKindFindingsStalled},
		{name: "recurring files", reason: recurringFilesStopReason([]string{"b.go", "a.go"}), want: StopKindRecurringFiles},
		{name: "malformed recurring files", reason: recurringFilesStopReasonPrefix + "not-json", want: StopKindNone},
		{name: "unknown", reason: "automatic rework stopped for another reason", want: StopKindNone},
		{name: "cap prefix only", reason: "automatic rework cap exhausted", want: StopKindNone},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := StopKindForPersistedReason(test.reason); got != test.want {
				t.Fatalf("StopKindForPersistedReason(%q) = %q, want %q", test.reason, got, test.want)
			}
		})
	}
}

func TestGuardAutoReworkRestart(t *testing.T) {
	const (
		stalledReason = "automatic rework stalled on equivalent consecutive findings"
		capReason     = "automatic rework cap exhausted after 5 cycles"
	)
	tests := []struct {
		name         string
		status       string
		events       []plan.Event
		allowRestart bool
		wantStopped  bool
		wantKind     StopKind
		wantReason   string
		wantFiles    []string
		wantError    string
	}{
		{
			name: "latest stopped event refuses a fresh budget",
			events: []plan.Event{
				{Type: plan.EventTypeReworkStopped, Reason: stalledReason},
				{Type: plan.EventTypeReworkRound},
				{Type: plan.EventTypePlanReviewed},
				{Type: plan.EventTypeReworkStopped, Reason: capReason},
			},
			wantStopped: true,
			wantKind:    StopKindCapExhausted,
			wantReason:  capReason,
			wantError: "Automatic rework stopped: attempt cap reached.\nReason: automatic rework cap exhausted after 5 cycles\n" +
				"Read the review and address the remaining findings before re-running.\n\n" +
				"A new automatic-rework budget was not started. To deliberately continue, rerun with --rework-restart",
		},
		{
			name:         "explicit restart grants a new budget",
			events:       []plan.Event{{Type: plan.EventTypeReworkStopped, Reason: stalledReason}},
			allowRestart: true,
			wantStopped:  true,
			wantKind:     StopKindFindingsStalled,
			wantReason:   stalledReason,
		},
		{
			name: "round after stop clears guard",
			events: []plan.Event{
				{Type: plan.EventTypeReworkStopped, Reason: stalledReason},
				{Type: plan.EventTypeReworkRound},
			},
		},
		{
			name: "manual reopen after stop clears guard",
			events: []plan.Event{
				{Type: plan.EventTypeReworkStopped, Reason: stalledReason},
				{Type: plan.EventTypePlanReopened},
				{Type: plan.EventTypePlanReviewed},
			},
		},
		{
			name:        "stop on a non-changes-requested plan does not guard",
			status:      plan.StatusReviewed,
			events:      []plan.Event{{Type: plan.EventTypeReworkStopped, Reason: capReason}},
			wantStopped: false,
		},
		{
			name:         "message-only persisted stop restores its kind",
			events:       []plan.Event{{Type: plan.EventTypeReworkStopped, Message: capReason}},
			allowRestart: true,
			wantStopped:  true,
			wantKind:     StopKindCapExhausted,
			wantReason:   capReason,
		},
		{
			name:         "recurring-file stop restores classification",
			events:       []plan.Event{{Type: plan.EventTypeReworkStopped, Reason: recurringFilesStopReason([]string{"z.go", "a.go"})}},
			allowRestart: true,
			wantStopped:  true,
			wantKind:     StopKindRecurringFiles,
			wantReason:   recurringFilesStopReason([]string{"a.go", "z.go"}),
			wantFiles:    []string{"a.go", "z.go"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			detail := actionableDriverDetail(0)
			if test.status != "" {
				detail.State.Status = test.status
			}
			detail.Events = test.events

			decision, stopped, err := GuardAutoReworkRestart(detail, test.allowRestart)
			if stopped != test.wantStopped {
				t.Fatalf("stopped = %t, want %t; decision=%+v", stopped, test.wantStopped, decision)
			}
			if decision.StopKind != test.wantKind || decision.StopReason != test.wantReason {
				t.Fatalf("stop = (%q, %q), want (%q, %q)", decision.StopKind, decision.StopReason, test.wantKind, test.wantReason)
			}
			if !slices.Equal(decision.RecurringFiles, test.wantFiles) {
				t.Fatalf("recurring files = %#v, want %#v", decision.RecurringFiles, test.wantFiles)
			}
			if test.wantError == "" {
				if err != nil {
					t.Fatalf("GuardAutoReworkRestart returned error: %v", err)
				}
			} else if err == nil || err.Error() != test.wantError {
				t.Fatalf("GuardAutoReworkRestart error = %q, want %q", err, test.wantError)
			}
		})
	}
}

func TestGuardAutoReworkRestartFormatsPersistedRecurringFileFindings(t *testing.T) {
	detail := actionableDriverDetail(0)
	detail.State.Plan.Review.Findings = []plan.ReviewFinding{{
		Severity:   "major",
		File:       "z.go",
		Line:       17,
		Message:    "the latest structured finding",
		Suggestion: "address the newest review",
	}}
	detail.Events = []plan.Event{{
		Type:   plan.EventTypeReworkStopped,
		Reason: recurringFilesStopReason([]string{"z.go", "a.go"}),
	}}

	decision, stopped, err := GuardAutoReworkRestart(detail, false)
	if !stopped || err == nil {
		t.Fatalf("persisted stop = (%+v, %t, %v), want guarded error", decision, stopped, err)
	}
	if decision.StopKind != StopKindRecurringFiles || !slices.Equal(decision.RecurringFiles, []string{"a.go", "z.go"}) || !reflect.DeepEqual(decision.Findings, ReviewFindings(detail)) {
		t.Fatalf("persisted recurring-file decision = %+v", decision)
	}
	for _, want := range []string{"THE SAME FILES KEEP RECURRING", "- a.go", "- z.go", "z.go:17", "the latest structured finding", "address the newest review", "--rework-restart"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("restart refusal %q does not contain %q", err, want)
		}
	}
}

func TestFormatStopMessageDistinguishesStallFromCap(t *testing.T) {
	finding := plan.ReviewFinding{
		Severity:   "major",
		File:       "internal/rework/driver.go",
		Line:       99,
		Message:    "the previous fix did not address the race",
		Suggestion: "serialize the update",
	}
	tests := []struct {
		name      string
		decision  Decision
		want      []string
		doNotWant []string
	}{
		{
			name:     "equivalent findings stall is loud and finding-bearing",
			decision: Decision{StopKind: StopKindFindingsStalled, StopReason: equivalentFindingsStopReason, Findings: []plan.ReviewFinding{finding}},
			want:     []string{"!!!!!!!!!!!!!!!!", "THE LOOP IS GOING IN CIRCLES", "internal/rework/driver.go:99", finding.Message, finding.Suggestion, "before re-running"},
		},
		{
			name:     "recurring files are distinct and finding-bearing",
			decision: Decision{StopKind: StopKindRecurringFiles, StopReason: recurringFilesStopReason([]string{"z.go", "a.go"}), RecurringFiles: []string{"z.go", "a.go"}, Findings: []plan.ReviewFinding{finding}},
			want:     []string{"!!!!!!!!!!!!!!!!", "THE SAME FILES KEEP RECURRING", "three consecutive reviews", "- a.go", "- z.go", finding.Message, finding.Suggestion, "before re-running"},
		},
		{
			name:      "cap exhaustion is milder",
			decision:  Decision{StopKind: StopKindCapExhausted, StopReason: "automatic rework cap exhausted after 5 cycles", Findings: []plan.ReviewFinding{finding}},
			want:      []string{"Automatic rework stopped: attempt cap reached", "automatic rework cap exhausted after 5 cycles"},
			doNotWant: []string{"!!!!!!!!!!!!!!!!", "GOING IN CIRCLES", finding.Message},
		},
		{
			name:      "untyped prose is not inferred",
			decision:  Decision{StopReason: "automatic rework cap exhausted after 5 cycles"},
			want:      []string{"automatic rework cap exhausted after 5 cycles"},
			doNotWant: []string{"Automatic rework stopped: attempt cap reached"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			message := FormatStopMessage(test.decision)
			for _, want := range test.want {
				if !strings.Contains(message, want) {
					t.Errorf("stop message %q does not contain %q", message, want)
				}
			}
			for _, unwanted := range test.doNotWant {
				if strings.Contains(message, unwanted) {
					t.Errorf("stop message %q unexpectedly contains %q", message, unwanted)
				}
			}
		})
	}
}

func TestDriverRunDisabledPolicyExecutesOnceWithoutLoadingState(t *testing.T) {
	driver := Driver{
		Resolve: func(context.Context, string) (*plan.PlanDetail, error) {
			t.Fatal("disabled automatic rework resolved plan detail")
			return nil, nil
		},
		DecideOne: func(context.Context, string, int, int, string, int) (Decision, error) {
			t.Fatal("disabled automatic rework made a decision")
			return Decision{}, nil
		},
	}
	executions := 0
	err := driver.Run(context.Background(), "plan", RunOptions{
		Enabled:     false,
		MaxAttempts: 5,
		Execute: func(context.Context) error {
			executions++
			return nil
		},
		BeforeDecision: func(context.Context) (int, bool, error) {
			t.Fatal("disabled automatic rework checked dynamic policy")
			return 0, false, nil
		},
	})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if executions != 1 {
		t.Fatalf("executions = %d, want 1", executions)
	}
}

func TestDriverRunExecutesThenDecidesWithFreshBudget(t *testing.T) {
	detail := actionableDriverDetail(2)
	var calls []string
	decisions := 0
	driver := Driver{
		Resolve: fixedDriverResolver(detail),
		DecideOne: func(_ context.Context, _ string, baseline, attempts int, previous string, maxAttempts int) (Decision, error) {
			calls = append(calls, fmt.Sprintf("decide:%d:%d:%s:%d", baseline, attempts, previous, maxAttempts))
			decisions++
			if decisions == 1 {
				return Decision{Reworked: true, Round: 3, Fingerprint: "finding-1"}, nil
			}
			return Decision{}, nil
		},
	}
	err := driver.Run(context.Background(), "plan", RunOptions{
		Enabled:     true,
		MaxAttempts: 5,
		Execute: func(context.Context) error {
			calls = append(calls, "execute")
			return nil
		},
		PersistProgress: func(_ context.Context, attempts, round int, fingerprint string) error {
			calls = append(calls, fmt.Sprintf("persist:%d:%d:%s", attempts, round, fingerprint))
			return nil
		},
		LogProgress: func(round int) error {
			calls = append(calls, fmt.Sprintf("log:%d", round))
			return nil
		},
	})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	want := []string{
		"execute",
		"decide:2:0::5",
		"persist:1:3:finding-1",
		"log:3",
		"execute",
		"decide:2:1:finding-1:5",
	}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %#v, want %#v", calls, want)
	}
}

func TestDriverRunAcknowledgedRestartDecidesBeforeFirstExecution(t *testing.T) {
	detail := actionableDriverDetail(0)
	detail.Events = []plan.Event{{Type: plan.EventTypeReworkStopped, Reason: equivalentFindingsStopReason}}
	var calls []string
	decisions := 0
	driver := Driver{
		Resolve: fixedDriverResolver(detail),
		DecideOne: func(context.Context, string, int, int, string, int) (Decision, error) {
			calls = append(calls, "decide")
			decisions++
			if decisions == 1 {
				return Decision{Reworked: true, Round: 1, Fingerprint: "fresh-finding"}, nil
			}
			return Decision{}, nil
		},
	}
	err := driver.Run(context.Background(), "plan", RunOptions{
		Enabled:      true,
		MaxAttempts:  5,
		AllowRestart: true,
		Execute: func(context.Context) error {
			calls = append(calls, "execute")
			return nil
		},
		PersistProgress: func(context.Context, int, int, string) error {
			calls = append(calls, "persist")
			return nil
		},
		LogProgress: func(int) error {
			calls = append(calls, "log")
			return nil
		},
	})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	want := []string{"decide", "persist", "log", "execute", "decide"}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %#v, want %#v", calls, want)
	}
}

func TestDriverRunRefusesPersistedStopBeforeExecution(t *testing.T) {
	detail := actionableDriverDetail(0)
	detail.Events = []plan.Event{{Type: plan.EventTypeReworkStopped, Reason: equivalentFindingsStopReason}}
	driver := Driver{
		Resolve: fixedDriverResolver(detail),
		DecideOne: func(context.Context, string, int, int, string, int) (Decision, error) {
			t.Fatal("guarded run made a decision")
			return Decision{}, nil
		},
	}
	executed := false
	err := driver.Run(context.Background(), "plan", RunOptions{
		Enabled:     true,
		MaxAttempts: 5,
		Execute: func(context.Context) error {
			executed = true
			return nil
		},
	})
	if err == nil || !strings.Contains(err.Error(), "A new automatic-rework budget was not started") {
		t.Fatalf("Run error = %v, want persisted-stop restart refusal", err)
	}
	if executed {
		t.Fatal("guarded run executed the plan")
	}
}

func TestDriverRunAcceptsRecoveredBudgetState(t *testing.T) {
	var gotBaseline, gotAttempts, gotMax int
	var gotPrevious string
	driver := Driver{
		Resolve: func(context.Context, string) (*plan.PlanDetail, error) {
			t.Fatal("recovered automatic rework resolved fresh plan detail")
			return nil, nil
		},
		DecideOne: func(_ context.Context, _ string, baseline, attempts int, previous string, maxAttempts int) (Decision, error) {
			gotBaseline, gotAttempts, gotPrevious, gotMax = baseline, attempts, previous, maxAttempts
			return Decision{}, nil
		},
	}
	err := driver.Run(context.Background(), "plan", RunOptions{
		Enabled:     true,
		MaxAttempts: 7,
		Recovered: &ExecutionState{Budget: Budget{
			BaselineRound:              3,
			Attempts:                   2,
			PreviousFindingFingerprint: "persisted-finding",
		}},
		Execute: func(context.Context) error { return nil },
	})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if gotBaseline != 3 || gotAttempts != 2 || gotPrevious != "persisted-finding" || gotMax != 7 {
		t.Fatalf("recovered decision state = (%d, %d, %q, %d)", gotBaseline, gotAttempts, gotPrevious, gotMax)
	}
}

func TestDriverRunPropagatesHookErrors(t *testing.T) {
	tests := []struct {
		name string
		run  func(error) error
	}{
		{
			name: "execute",
			run: func(want error) error {
				return (Driver{}).Run(context.Background(), "plan", RunOptions{
					Execute: func(context.Context) error { return want },
				})
			},
		},
		{
			name: "dynamic policy and stop check",
			run: func(want error) error {
				return (Driver{Resolve: fixedDriverResolver(actionableDriverDetail(0))}).Run(context.Background(), "plan", RunOptions{
					Enabled:     true,
					MaxAttempts: 5,
					Execute:     func(context.Context) error { return nil },
					BeforeDecision: func(context.Context) (int, bool, error) {
						return 0, false, want
					},
				})
			},
		},
		{
			name: "durable progress",
			run: func(want error) error {
				driver := Driver{
					Resolve: fixedDriverResolver(actionableDriverDetail(0)),
					DecideOne: func(context.Context, string, int, int, string, int) (Decision, error) {
						return Decision{Reworked: true, Round: 1, Fingerprint: "finding"}, nil
					},
				}
				return driver.Run(context.Background(), "plan", RunOptions{
					Enabled:         true,
					MaxAttempts:     5,
					Execute:         func(context.Context) error { return nil },
					PersistProgress: func(context.Context, int, int, string) error { return want },
				})
			},
		},
		{
			name: "progress logging",
			run: func(want error) error {
				driver := Driver{
					Resolve: fixedDriverResolver(actionableDriverDetail(0)),
					DecideOne: func(context.Context, string, int, int, string, int) (Decision, error) {
						return Decision{Reworked: true, Round: 1, Fingerprint: "finding"}, nil
					},
				}
				return driver.Run(context.Background(), "plan", RunOptions{
					Enabled:         true,
					MaxAttempts:     5,
					Execute:         func(context.Context) error { return nil },
					PersistProgress: func(context.Context, int, int, string) error { return nil },
					LogProgress:     func(int) error { return want },
				})
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			want := errors.New(test.name + " failed")
			if err := test.run(want); !errors.Is(err, want) {
				t.Fatalf("Run error = %v, want %v", err, want)
			}
		})
	}
}

func TestDriverRunRecoveredAttemptsNeverDecrease(t *testing.T) {
	decisions := 0
	persistedAttempts := 0
	driver := Driver{DecideOne: func(context.Context, string, int, int, string, int) (Decision, error) {
		decisions++
		if decisions == 1 {
			return Decision{Reworked: true, Round: 11, Fingerprint: "new-finding"}, nil
		}
		return Decision{}, nil
	}}
	err := driver.Run(context.Background(), "plan", RunOptions{
		Enabled:     true,
		MaxAttempts: 9,
		Recovered: &ExecutionState{Budget: Budget{
			BaselineRound: 10,
			Attempts:      4,
		}},
		Execute: func(context.Context) error { return nil },
		PersistProgress: func(_ context.Context, attempts, _ int, _ string) error {
			persistedAttempts = attempts
			return nil
		},
	})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if persistedAttempts != 4 {
		t.Fatalf("persisted attempts = %d, want 4", persistedAttempts)
	}
}

func TestDriverLoopPersistsBeforeEachRerun(t *testing.T) {
	details := []*plan.PlanDetail{
		actionableDriverDetail(0),
		actionableDriverDetail(1),
		{State: plan.State{Status: plan.StatusReviewed}},
	}
	resolveIndex := 0
	driver := Driver{
		Resolve: func(context.Context, string) (*plan.PlanDetail, error) {
			detail := details[resolveIndex]
			resolveIndex++
			return detail, nil
		},
		Record: func(detail *plan.PlanDetail) (Record, error) { return &driverRecord{detail: detail}, nil },
		Now:    func() time.Time { return time.Date(2026, 7, 14, 1, 0, 0, 0, time.FixedZone("offset", 3600)) },
	}
	var calls []string
	err := driver.Loop(context.Background(), "plan", LoopOptions{
		MaxAttempts: 5,
		Execute: func(context.Context) error {
			calls = append(calls, "execute")
			return nil
		},
		PersistProgress: func(_ context.Context, attempts, round int, fingerprint string) error {
			if fingerprint == "" {
				t.Fatal("PersistProgress received an empty fingerprint")
			}
			calls = append(calls, fmt.Sprintf("persist:%d:%d", attempts, round))
			return nil
		},
	})
	if err != nil {
		t.Fatalf("Loop returned error: %v", err)
	}
	want := []string{"execute", "persist:1:1", "execute", "persist:2:2", "execute"}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %#v, want %#v", calls, want)
	}
}

func TestDriverLoopCanReopenBeforeFirstExecution(t *testing.T) {
	detail := actionableDriverDetail(0)
	driver := Driver{
		Resolve: fixedDriverResolver(detail),
		Record:  func(detail *plan.PlanDetail) (Record, error) { return &driverRecord{detail: detail}, nil },
	}
	var calls []string
	err := driver.Loop(context.Background(), "plan", LoopOptions{
		MaxAttempts:         5,
		DecideBeforeExecute: true,
		Execute: func(context.Context) error {
			calls = append(calls, "execute")
			if detail.State.Status != plan.StatusInProgress || len(detail.State.Plan.PendingSlices) == 0 {
				t.Fatalf("first execution saw plan before reopen: status=%q pending=%v", detail.State.Status, detail.State.Plan.PendingSlices)
			}
			detail.State.Status = plan.StatusReviewed
			return nil
		},
		PersistProgress: func(context.Context, int, int, string) error {
			calls = append(calls, "persist")
			return nil
		},
	})
	if err != nil {
		t.Fatalf("Loop returned error: %v", err)
	}
	want := []string{"persist", "execute"}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %#v, want %#v", calls, want)
	}
}

func TestDriverLoopPropagatesExecuteError(t *testing.T) {
	want := errors.New("execute failed")
	driver := Driver{}
	err := driver.Loop(context.Background(), "plan", LoopOptions{
		MaxAttempts: 5,
		Execute:     func(context.Context) error { return want },
	})
	if !errors.Is(err, want) {
		t.Fatalf("Loop error = %v, want %v", err, want)
	}
}

func TestDriverLoopAttemptsNeverDecrease(t *testing.T) {
	details := []*plan.PlanDetail{actionableDriverDetail(0), {State: plan.State{Status: plan.StatusReviewed}}}
	resolveIndex := 0
	driver := Driver{
		Resolve: func(context.Context, string) (*plan.PlanDetail, error) {
			detail := details[resolveIndex]
			resolveIndex++
			return detail, nil
		},
		Record: func(detail *plan.PlanDetail) (Record, error) { return &driverRecord{detail: detail}, nil },
	}
	persistedAttempts := 0
	err := driver.Loop(context.Background(), "plan", LoopOptions{
		Attempts:    4,
		MaxAttempts: 5,
		Execute:     func(context.Context) error { return nil },
		PersistProgress: func(_ context.Context, attempts, _ int, _ string) error {
			persistedAttempts = attempts
			return nil
		},
	})
	if err != nil {
		t.Fatalf("Loop returned error: %v", err)
	}
	if persistedAttempts != 4 {
		t.Fatalf("persisted attempts = %d, want 4", persistedAttempts)
	}
}

func fixedDriverResolver(detail *plan.PlanDetail) PlanResolver {
	return func(context.Context, string) (*plan.PlanDetail, error) { return detail, nil }
}

func recurringDriverDetail() (*plan.PlanDetail, string) {
	detail := actionableDriverDetail(2)
	firstMessage := "Warp drops the recovered record"
	secondMessage := "Warp leaks the write transaction"
	for index := range detail.Slices.Slices {
		detail.Slices.Slices[index].ExpectedFiles = []string{"store/file.go", "store/file_test.go"}
		detail.Slices.Slices[index].Goal = []string{firstMessage, secondMessage}[index]
	}
	detail.State.Plan.Review.Findings = []plan.ReviewFinding{{
		Severity:   "major",
		File:       "store/file.go",
		Line:       42,
		Message:    "Warp corrupts the committed record",
		Suggestion: "preserve the committed value",
	}}
	previous := ReworkFindingsFingerprint([]plan.ReviewFinding{{
		Severity:   "major",
		File:       "store/file.go",
		Line:       42,
		Message:    secondMessage,
		Suggestion: "close the transaction",
	}})
	return detail, previous
}

func actionableDriverDetail(round int) *plan.PlanDetail {
	detail := &plan.PlanDetail{
		State: plan.State{
			Status: plan.StatusChangesRequested,
			Plan: plan.PlanState{
				ID: "plan",
				Review: &plan.PlanReview{
					Status:   plan.ReviewStatusCompleted,
					Verdict:  plan.ReviewVerdictChangesRequested,
					Findings: []plan.ReviewFinding{{Severity: "major", File: fmt.Sprintf("internal/rework/driver-round-%d.go", round), Message: fmt.Sprintf("fix driver round %d", round)}},
				},
			},
		},
	}
	for current := 1; current <= round; current++ {
		detail.Slices.Slices = append(detail.Slices.Slices, plan.Slice{ID: fmt.Sprintf("r%d01-driver", current)})
	}
	return detail
}
