package merge

import (
	"bytes"
	"errors"
	"io"
	"os"
	"reflect"
	"testing"
)

func TestFormatBatchTransitionLine(t *testing.T) {
	t.Parallel()
	state := testBatchState()
	state.Candidates = append(state.Candidates, BatchCandidate{PlanID: "plan-b", Deferred: &BatchDeferral{PlanID: "plan-b"}})
	state.Ejection = &BatchEjection{PlanID: "plan-b", Status: "completed"}
	state.LandedSHA = "abc123"
	transition := BatchTransition{At: "2026-09-26T21:00:00Z", Sequence: 12, From: BatchStatusSettling, To: BatchStatusCompleted, State: state}
	want := "2026-09-26T21:00:00Z merge-batch 20260715-merge-all #12 settling -> completed candidates=2 deferred=1 ejected=1 landed=abc123"
	if got := FormatBatchTransitionLine(transition); got != want {
		t.Fatalf("line = %q, want %q", got, want)
	}
}

func TestBatchProgressStore(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"nil", "buffer", "write error"} {
		t.Run(mode, func(t *testing.T) {
			var buffer bytes.Buffer
			var out io.Writer
			switch mode {
			case "buffer":
				out = &buffer
			case "write error":
				out = progressErrorWriter{}
			}
			plain := newTestBatchStore(t)
			store := NewBatchProgressStore(newTestBatchStore(t), out)
			const at = "2026-09-26T21:00:00Z"
			initial := testBatchState()
			want, wantErr := plain.Initialize(initial, at)
			got, err := store.Initialize(initial, at)
			if wantErr != nil || err != nil || !reflect.DeepEqual(got, want) {
				t.Fatalf("Initialize = %#v, %v; plain = %#v, %v", got, err, want, wantErr)
			}
			// The incoming status is the target, not the durable source status.
			next := got
			next.Status = BatchStatusIntegrating
			want, wantErr = plain.Transition(next, at)
			got, err = store.Transition(next, at)
			if wantErr != nil || err != nil || !reflect.DeepEqual(got, want) {
				t.Fatalf("Transition = %#v, %v; plain = %#v, %v", got, err, want, wantErr)
			}
			next.Status = BatchStatusPlanned
			if _, err := store.Transition(next, at); err == nil {
				t.Fatal("expected invalid transition to fail")
			}
			if _, err := store.Initialize(initial, at); err == nil {
				t.Fatal("expected repeated initialization to fail")
			}
			wantOutput := ""
			if mode == "buffer" {
				wantOutput = at + " merge-batch 20260715-merge-all #1  -> planned candidates=1 deferred=0 ejected=0\n" +
					at + " merge-batch 20260715-merge-all #2 planned -> integrating candidates=1 deferred=0 ejected=0\n"
			}
			if buffer.String() != wantOutput {
				t.Fatalf("output = %q, want %q", buffer.String(), wantOutput)
			}
			loaded, err := store.Load(initial.ID)
			if err != nil || !reflect.DeepEqual(loaded, want) {
				t.Fatalf("durable state = %#v, %v; want %#v", loaded, err, want)
			}
			plainLog, err := os.ReadFile(plain.logPath(initial.ID))
			if err != nil {
				t.Fatal(err)
			}
			progressLog, err := os.ReadFile(store.logPath(initial.ID))
			if err != nil || !bytes.Equal(plainLog, progressLog) {
				t.Fatalf("durable logs differ: %v", err)
			}
		})
	}
}

type progressErrorWriter struct{}

func (progressErrorWriter) Write([]byte) (int, error) {
	return 0, errors.New("progress unavailable")
}
