package merge

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type singleMergeLandingProbe struct {
	reached bool
	err     error
	calls   int
}

func (p *singleMergeLandingProbe) DefaultReachedLandingIntent(context.Context, BatchState) (bool, error) {
	p.calls++
	return p.reached, p.err
}

func TestRequireNoPreLandingBatch(t *testing.T) {
	probeErr := errors.New("probe failed")
	type testCase struct {
		name                                                         string
		status                                                       BatchStatus
		noActive, corruptActive, missingState, emptyState, loadError bool
		landing, landed, reached, probeError                         bool
		candidate, ejected, recordOnly                               bool
		wantError, wantRefusal                                       bool
	}
	tests := []testCase{
		{name: "no active", noActive: true},
		{name: "corrupt active", corruptActive: true, wantError: true},
		{name: "missing state", missingState: true, wantError: true},
		{name: "empty state", emptyState: true, wantError: true},
		{name: "load error", loadError: true, wantError: true},
		{name: "landed", landing: true, landed: true},
		{name: "completed", status: BatchStatusCompleted, landing: true, landed: true},
		{name: "intent reached", landing: true, reached: true},
		{name: "intent not reached", landing: true, wantRefusal: true},
		{name: "probe error", landing: true, probeError: true, wantError: true},
		{name: "sha without intent", landed: true, wantRefusal: true},
		{name: "record candidate", recordOnly: true, candidate: true, wantRefusal: true},
		{name: "record ejected", recordOnly: true, candidate: true, ejected: true, wantRefusal: true},
		{name: "record noncandidate", recordOnly: true},
	}
	for _, status := range []BatchStatus{BatchStatusPlanned, BatchStatusIntegrating, BatchStatusResolving, BatchStatusReviewing, BatchStatusReadyToLand, BatchStatusBlocked} {
		tests = append(tests, testCase{name: string(status), status: status, wantRefusal: true})
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			active := filepath.Join(root, "active.json")
			store := NewBatchStore(filepath.Join(root, "batches"), active)
			state := BatchState{Schema: BatchStateSchema, ID: "batch-1", Status: tt.status}
			if state.Status == "" {
				state.Status = BatchStatusPlanned
			}
			if tt.landing {
				state.Landing = &BatchLanding{}
			}
			if tt.landed {
				state.LandedSHA = "landed-sha"
			}
			if tt.candidate {
				state.Candidates = []BatchCandidate{{PlanID: "plan-1"}}
			}
			if tt.ejected {
				state.Candidates[0].Deferred = &BatchDeferral{PlanID: "plan-1"}
				state.Ejection = &BatchEjection{PlanID: "plan-1", Status: batchEjectionPending}
			}
			if !tt.noActive {
				if err := store.SetActive(state.ID); err != nil {
					t.Fatal(err)
				}
				if !tt.missingState && !tt.emptyState && !tt.loadError {
					if err := store.SaveSnapshot(state); err != nil {
						t.Fatal(err)
					}
				}
			}
			if tt.corruptActive {
				if err := os.WriteFile(active, []byte("{"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if tt.emptyState {
				if err := os.MkdirAll(filepath.Dir(store.snapshotPath(state.ID)), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(store.snapshotPath(state.ID), []byte("{}"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if tt.loadError {
				if err := os.MkdirAll(store.logPath(state.ID), 0700); err != nil {
					t.Fatal(err)
				}
			}
			before := singleMergeBatchFiles(t, root)
			probe := &singleMergeLandingProbe{reached: tt.reached}
			if tt.probeError {
				probe.err = probeErr
			}
			err := RequireNoPreLandingBatch(context.Background(), store, probe, "plan-1", tt.recordOnly)
			var refusal *SingleMergeBatchRefusal
			if got := errors.As(err, &refusal); got != tt.wantRefusal {
				t.Fatalf("refusal = %v, error = %v", got, err)
			}
			if (err != nil) != (tt.wantError || tt.wantRefusal) {
				t.Fatalf("unexpected error: %v", err)
			}
			if tt.probeError && !errors.Is(err, probeErr) {
				t.Fatalf("lost probe error: %v", err)
			}
			if refusal != nil {
				if refusal.BatchID != state.ID || refusal.Status != state.Status || refusal.PlanID != "plan-1" || refusal.Candidate != tt.candidate || refusal.Ejected != tt.ejected || refusal.RecordOnly != tt.recordOnly {
					t.Fatalf("incorrect refusal: %+v", refusal)
				}
				membership := "not a candidate"
				if tt.candidate {
					membership = "a candidate"
				}
				if tt.ejected {
					membership = "an ejected candidate"
				}
				for _, text := range []string{state.ID, string(state.Status), membership, "tao merge --all", "tao merge --all --restart --dry-run", "tao merge --all --restart", "retry"} {
					if !strings.Contains(err.Error(), text) {
						t.Errorf("error %q missing %q", err, text)
					}
				}
			}
			wantCalls := 0
			if tt.landing && !tt.landed {
				wantCalls = 1
			}
			if probe.calls != wantCalls {
				t.Errorf("probe calls = %d, want %d", probe.calls, wantCalls)
			}
			after := singleMergeBatchFiles(t, root)
			if len(before) != len(after) {
				t.Fatal("batch file set changed")
			}
			for path, content := range before {
				if !bytes.Equal(content, after[path]) {
					t.Errorf("batch file changed: %s", path)
				}
			}
		})
	}
}

func TestRequireNoPreLandingBatchInvalidActiveIdentity(t *testing.T) {
	for _, content := range []string{"", "{}", `{"schema":"wrong","batch_id":"batch-1"}`} {
		t.Run(content, func(t *testing.T) {
			root := t.TempDir()
			active := filepath.Join(root, "active.json")
			if err := os.WriteFile(active, []byte(content), 0600); err != nil {
				t.Fatal(err)
			}
			store := NewBatchStore(filepath.Join(root, "batches"), active)
			if err := RequireNoPreLandingBatch(context.Background(), store, &singleMergeLandingProbe{}, "plan", true); err == nil {
				t.Fatal("invalid active identity accepted")
			}
		})
	}
}

func TestRequireNoPreLandingBatchUnreadableActiveIdentity(t *testing.T) {
	root := t.TempDir()
	active := filepath.Join(root, "active.json")
	if err := os.Mkdir(active, 0700); err != nil {
		t.Fatal(err)
	}
	store := NewBatchStore(filepath.Join(root, "batches"), active)
	if err := RequireNoPreLandingBatch(context.Background(), store, &singleMergeLandingProbe{}, "plan", false); err == nil {
		t.Fatal("unreadable active identity accepted")
	}
}

func TestRequireNoPreLandingBatchRequiresDependencies(t *testing.T) {
	store := NewBatchStore(t.TempDir(), filepath.Join(t.TempDir(), "active.json"))
	if err := RequireNoPreLandingBatch(context.Background(), nil, &singleMergeLandingProbe{}, "plan", false); err == nil {
		t.Fatal("nil store accepted")
	}
	if err := RequireNoPreLandingBatch(context.Background(), store, nil, "plan", false); err == nil {
		t.Fatal("nil probe accepted")
	}
}

func singleMergeBatchFiles(t *testing.T, root string) map[string][]byte {
	t.Helper()
	files := map[string][]byte{}
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		content, err := os.ReadFile(path) // #nosec G304 G122 -- test-owned temporary directory has no symlinks or concurrent writers.
		if err != nil {
			return err
		}
		files[path] = content
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}
