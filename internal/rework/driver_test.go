package rework

import (
	"context"
	"errors"
	"fmt"
	"reflect"
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
