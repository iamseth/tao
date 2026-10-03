package plan

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestBoundRunAbortMessage(t *testing.T) {
	for _, tt := range []struct{ name, input, want string }{
		{"empty", "", ""},
		{"first line", "  first error  \r\nsecond error", "first error"},
		{"blank first line", " \nsecond error", ""},
		{"rune cap", "  " + strings.Repeat("界", 513) + "\nsecond error", strings.Repeat("界", 512)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := BoundRunAbortMessage(tt.input); got != tt.want {
				t.Fatalf("BoundRunAbortMessage = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestLatestRunAbort(t *testing.T) {
	abort := Event{Type: EventTypeRunAborted, AbortKind: RunAbortKindOther, Message: "error", SliceID: "001-a", HeadSHA: "head"}
	for _, tt := range []struct {
		name   string
		events []Event
		want   *Event
	}{
		{"empty", nil, nil},
		{"non abort", []Event{{Type: EventTypeRunContext}}, nil},
		{"context supersedes", []Event{abort, {Type: EventTypeRunContext}}, nil},
		{"slice supersedes", []Event{abort, {Type: EventTypeSliceStarted}}, nil},
		{"last abort", []Event{{Type: EventTypeRunContext}, abort}, &abort},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := LatestRunAbort(tt.events)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("LatestRunAbort = %+v, want %+v", got, tt.want)
			}
			if got != nil {
				got.Message = "changed"
				if tt.events[len(tt.events)-1].Message != abort.Message {
					t.Fatal("projection must return a copy")
				}
			}
		})
	}
}

func TestRunAbortDerivationNeutrality(t *testing.T) {
	for _, status := range []string{StatusPlanned, StatusInProgress} {
		t.Run(status, func(t *testing.T) {
			detail := &PlanDetail{
				State:  State{Status: status, Plan: PlanState{ID: "plan", PendingSlices: []string{"001-a"}}},
				Slices: SlicesFile{Slices: []Slice{{ID: "001-a", Status: StatusPending}}},
				Events: []Event{{Type: EventTypeRunContext}},
			}
			if status == StatusInProgress {
				detail.State.Plan.CurrentSlice = ptrString("001-a")
				detail.Slices.Slices[0].Status = StatusInProgress
				detail.Events = append(detail.Events, Event{Type: EventTypeSliceStarted, SliceID: "001-a"})
			}
			before := Derive(detail, time.Time{})
			capabilities := AnalyzeRunCapabilities(detail)
			detail.Events = append(detail.Events, Event{Type: EventTypeRunAborted, AbortKind: RunAbortKindControlCheckoutLeak, Message: "diagnostic only", SliceID: "001-a"})
			after := Derive(detail, time.Time{})
			if !reflect.DeepEqual(before.Capabilities, after.Capabilities) {
				t.Fatal("abort changed derived capabilities")
			}
			if !reflect.DeepEqual(before.NextAction, after.NextAction) {
				t.Fatal("abort changed next action")
			}
			if got := AnalyzeRunCapabilities(detail); !reflect.DeepEqual(capabilities, got) {
				t.Fatal("abort changed run capabilities")
			}
		})
	}
}
