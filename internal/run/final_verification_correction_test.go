package run

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/internal/plantest"
)

type interruptedCorrectionStore struct{}

func (interruptedCorrectionStore) WriteState(string, []byte) error {
	return fmt.Errorf("interrupted persistence")
}
func (interruptedCorrectionStore) WriteSlices(string, []byte) error {
	return fmt.Errorf("interrupted persistence")
}
func (interruptedCorrectionStore) AppendEvent(string, plan.Event) error {
	return fmt.Errorf("interrupted persistence")
}

func TestFinalVerificationCorrectionPersistenceFailure(t *testing.T) {
	record, request := correctionServiceFixture(t)
	preview, err := plan.PreviewFinalVerificationCorrection(record.Detail(), request)
	if err != nil {
		t.Fatal(err)
	}
	request.Confirmation = preview.Token
	interrupted, err := plan.NewPlanRecordWithStore(interruptedCorrectionStore{}, record.Dir(), record.Detail())
	if err != nil {
		t.Fatal(err)
	}
	root, err := filepath.EvalSymlinks(record.Detail().State.Workspace.Path)
	if err != nil {
		t.Fatal(err)
	}
	runner := correctionReadRunner(t, root, "", false)
	if err := CorrectFinalVerification(context.Background(), interrupted, request, runner); err == nil {
		t.Fatal("persistence failure hidden")
	}
	if len(record.Detail().State.Plan.FinalVerificationCorrections) != 0 {
		t.Fatal("failed write granted authority")
	}
	if err := CorrectFinalVerification(context.Background(), record, request, runner); err != nil {
		t.Fatal(err)
	}
}

func TestCorrectFinalVerificationRejectsMissingRecord(t *testing.T) {
	if err := CorrectFinalVerification(context.Background(), nil, plan.FinalVerificationCorrectionRequest{}, nil); err == nil {
		t.Fatal("missing record accepted")
	}
}

func correctionServiceFixture(t *testing.T) (*plan.PlanRecord, plan.FinalVerificationCorrectionRequest) {
	t.Helper()
	d := plantest.NewPlanDetail("correction").WithStatus(plan.StatusVerificationFailed).Build()
	d.Dir = t.TempDir()
	d.State.Plan.CurrentSlice = nil
	d.State.Plan.PendingSlices = nil
	d.Slices.Slices = nil
	d.State.Workspace = &plan.Workspace{Path: t.TempDir(), Branch: "work", HeadSHA: "head"}
	d.State.Plan.FinalVerification = &plan.FinalVerification{Command: "old gate", CWD: ".", HeadSHA: "head", Result: "failed"}
	for name, value := range map[string]any{"state.json": d.State, "slices.json": d.Slices} {
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(d.Dir, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	record, err := plan.NewPlanRecord(d.Dir, d)
	if err != nil {
		t.Fatal(err)
	}
	return record, plan.FinalVerificationCorrectionRequest{ExpectedHead: "head", Contract: plan.FinalVerificationContract{Revision: 1, Command: "new gate", CWD: ".", Purpose: "tests", Coverage: []string{"tests"}, Provenance: "owner"}, Reason: "wrong gate", CoverageRemoved: []string{"build"}, DeferredChecks: []string{"manual build"}, At: time.Now().UTC()}
}

func correctionReadRunner(t *testing.T, root string, status string, drift bool) CommandRunner {
	t.Helper()
	calls := 0
	return func(_ context.Context, cwd, name string, args []string, stdout, _ io.Writer) error {
		if name != "git" || cwd != "" {
			t.Fatalf("non-passive command %s %v", name, args)
		}
		prefix := []string{"-C", root, "--no-optional-locks", "-c", "diff.autoRefreshIndex=false", "-c", "core.fsmonitor=false", "-c", "core.quotePath=false", "--literal-pathspecs"}
		if len(args) < len(prefix) || !reflect.DeepEqual(args[:len(prefix)], prefix) {
			t.Fatalf("unpinned call %v", args)
		}
		sequence := [][]string{{"rev-parse", "--show-toplevel"}, {"symbolic-ref", "--short", "HEAD"}, {"rev-parse", "HEAD"}, {"status", "--porcelain", "--untracked-files=all", "--no-renames", "-z"}}
		probe := args[len(prefix):]
		if !reflect.DeepEqual(probe, sequence[calls%4]) {
			t.Fatalf("call %d: %v", calls, probe)
		}
		values := []string{root, "work", "head", status}
		value := values[calls%4]
		if drift && calls >= 4 && calls%4 == 2 {
			value = "other"
		}
		calls++
		_, err := fmt.Fprint(stdout, value)
		return err
	}
}

func TestFinalVerificationCorrectionService(t *testing.T) {
	for _, scenario := range []string{"success", "dirty", "stale head", "missing workspace", "confirmation mismatch", "active run", "terminal", "frozen intent", "cancelled", "recheck", "stale state"} {
		t.Run(scenario, func(t *testing.T) {
			record, request := correctionServiceFixture(t)
			d := record.Detail()
			preview, err := plan.PreviewFinalVerificationCorrection(d, request)
			if err != nil {
				t.Fatal(err)
			}
			request.Confirmation = preview.Token
			status := ""
			ctx := context.Background()
			switch scenario {
			case "dirty":
				status = " M unfinished\x00"
			case "stale head":
				request.ExpectedHead = "other"
			case "missing workspace":
				if err := os.Remove(d.State.Workspace.Path); err != nil {
					t.Fatal(err)
				}
			case "confirmation mismatch":
				request.Confirmation = "wrong"
			case "active run":
				lock, err := plan.AcquireRunLock(d.Dir, d.State.Plan.ID, time.Now())
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = lock.Release() }()
			case "stale state":
				d.State.UpdatedAt = request.At.Add(time.Minute)
			case "terminal":
				d.State.Status = plan.StatusCompleted
			case "frozen intent":
				d.Slices.Slices = append(d.Slices.Slices, plan.Slice{ID: "frozen", Status: plan.StatusPending, CommitIntent: &plan.SliceCommitIntent{Message: "frozen"}})
			case "cancelled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			for name, value := range map[string]any{"state.json": d.State, "slices.json": d.Slices} {
				data, _ := json.Marshal(value)
				if err := os.WriteFile(filepath.Join(d.Dir, name), data, 0600); err != nil {
					t.Fatal(err)
				}
			}
			before, _ := os.ReadFile(filepath.Join(d.Dir, "state.json")) // #nosec G304 -- isolated fixture.
			root, _ := filepath.EvalSymlinks(d.State.Workspace.Path)
			runner := correctionReadRunner(t, root, status, scenario == "recheck")
			err = CorrectFinalVerification(ctx, record, request, runner)
			if scenario == "success" {
				if err != nil {
					t.Fatal(err)
				}
				if plan.RequireCurrentFinalVerification(record.Detail()) == nil {
					t.Fatal("fresh verification not required")
				}
				if err := CorrectFinalVerification(ctx, record, request, runner); err != nil {
					t.Fatal(err)
				}
				if len(record.Detail().State.Plan.FinalVerificationCorrections) != 1 {
					t.Fatal("duplicate audit")
				}
			} else {
				if err == nil {
					t.Fatal("unsafe correction accepted")
				}
				after, _ := os.ReadFile(filepath.Join(d.Dir, "state.json")) // #nosec G304 -- isolated fixture.
				if !bytes.Equal(before, after) {
					t.Fatal("refusal mutated state")
				}
			}
		})
	}
}
