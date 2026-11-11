package cli

import (
	"bytes"
	"context"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/iamseth/tao/internal/identity"
	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/internal/plantest"
	"github.com/iamseth/tao/internal/runtimeconfig"
)

// blockedApproveRepo builds a plantest.Repository with a single blocked plan
// that has slice sliceID gated by approval.
func blockedApproveRepo(planID, sliceID, planStatus string) *plantest.Repository {
	detail := plantest.NewPlanDetail(planID).
		WithStatus(planStatus).
		WithCurrentSlice(sliceID).
		WithPendingSlices(sliceID).
		WithRepoRoot("/repo").
		AddSlice(plantest.NewSlice(sliceID).
			WithStatus(plan.StatusBlocked).
			WithVerificationCommands("go test ./internal/cli").
			WithApproval(plantest.Approval(true, "human approval", false)).
			Build()).
		Build()
	repo := plantest.NewRepository()
	repo.AddDetail(detail)
	return repo
}

func TestApproveCommandApprovesCurrentGatedSlice(t *testing.T) {
	const planID = "20260430-1200-run-plan"
	const sliceID = "001-a"
	repo := blockedApproveRepo(planID, sliceID, plan.StatusBlocked)

	var out bytes.Buffer
	app := newTestApp(App{Out: &out, Err: &out, Repository: func(_ string) Repository { return repo }})
	if err := app.Run(context.Background(), []string{"approve", "--by", "Seth", planID, "--slice", sliceID}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"Slice approved: " + sliceID,
		"Next: tao run --continue " + planID,
		"Reason: continue at the preserved boundary after resolving its blocker; use restart only for an eligible newer baseline",
	} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("expected approve output %q, got %q", want, out.String())
		}
	}

	updated, err := repo.GetPlan(context.Background(), planID)
	if err != nil {
		t.Fatal(err)
	}
	sl := findSlice(updated, sliceID)
	if sl == nil || sl.Approval == nil || !sl.Approval.Approved {
		t.Fatalf("expected slice to be approved, got %+v", sl)
	}
	if sl.Approval.ApprovedBy == nil || *sl.Approval.ApprovedBy != "Seth" {
		t.Fatalf("expected approved_by Seth, got %v", sl.Approval.ApprovedBy)
	}
	if sl.Approval.ApprovedAt == nil || *sl.Approval.ApprovedAt == "" {
		t.Fatalf("expected non-empty approved_at")
	}
	foundEvent := false
	for _, e := range updated.Events {
		if e.Type == plan.EventTypeSliceApproved {
			if e.Actor != "operator" || e.Trigger != "approve" || e.AttributionVersion != 1 {
				t.Fatalf("unexpected approval attribution: %+v", e)
			}
			foundEvent = true
		}
	}
	if !foundEvent {
		t.Fatalf("expected slice_approved event, got %v", updated.Events)
	}

	before := append([]plan.Event(nil), updated.Events...)
	if err := app.Run(context.Background(), []string{"approve", "--by", "agent", planID, "--slice", sliceID}); err != nil {
		t.Fatal(err)
	}
	updated, err = repo.GetPlan(context.Background(), planID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, updated.Events) {
		t.Fatalf("repeated approval changed events: %+v", updated.Events)
	}

	// Confirm the approved blocked slice is now continuable.
	record, err := repo.PlanRecord(updated)
	if err != nil {
		t.Fatal(err)
	}
	if err := record.ContinueBlocked(time.Now().UTC()); err != nil {
		t.Fatalf("expected approved blocked slice to be continuable, got %v", err)
	}
}

func TestApproveCommandSnapshotIdentity(t *testing.T) {
	for _, explicit := range []string{"", " Explicit Approver "} {
		t.Run("by="+explicit, func(t *testing.T) {
			const planID, sliceID = "20260430-1200-run-plan", "002-identity-approval"
			repo := blockedApproveRepo(planID, sliceID, plan.StatusBlocked)
			var out bytes.Buffer
			app := newTestApp(App{Out: &out, Err: &out, RuntimeEnv: snapshotWith(map[string]string{
				runtimeconfig.EnvApprovedBy: " Captured Approver ",
				runtimeconfig.EnvAgent:      "invalid", runtimeconfig.EnvPlannerRouting: "invalid",
				runtimeconfig.EnvSessionTimeout: "invalid",
			}), Repository: func(string) Repository { return repo }})
			t.Setenv(runtimeconfig.EnvApprovedBy, "Changed Approver")
			want := identity.Approver(" Captured Approver ")
			args := []string{"approve", planID}
			if explicit != "" {
				args = append(args, "--by", explicit)
				want = strings.TrimSpace(explicit)
			}
			if err := app.Run(context.Background(), args); err != nil {
				t.Fatal(err)
			}
			updated, err := repo.GetPlan(context.Background(), planID)
			if err != nil {
				t.Fatal(err)
			}
			slice := findSlice(updated, sliceID)
			if slice == nil || slice.Approval == nil || slice.Approval.ApprovedBy == nil || *slice.Approval.ApprovedBy != want {
				t.Fatalf("approval = %+v, want approver %q", slice, want)
			}
		})
	}
}

func TestApproveCommandStampsInjectedClock(t *testing.T) {
	const planID = "20260430-1200-run-plan"
	const sliceID = "001-a"
	repo := blockedApproveRepo(planID, sliceID, plan.StatusBlocked)

	fixed := time.Date(2031, 2, 3, 4, 5, 6, 0, time.UTC)
	var out bytes.Buffer
	app := newTestApp(App{Out: &out, Err: &out, Now: func() time.Time { return fixed }, Repository: func(_ string) Repository { return repo }})
	if err := app.Run(context.Background(), []string{"approve", "--by", "Seth", planID, "--slice", sliceID}); err != nil {
		t.Fatal(err)
	}

	updated, err := repo.GetPlan(context.Background(), planID)
	if err != nil {
		t.Fatal(err)
	}
	sl := findSlice(updated, sliceID)
	if sl == nil || sl.Approval == nil || sl.Approval.ApprovedAt == nil {
		t.Fatalf("expected approval with timestamp, got %+v", sl)
	}
	if want := "2031-02-03T04:05:06Z"; *sl.Approval.ApprovedAt != want {
		t.Fatalf("expected injected clock %q, got %q", want, *sl.Approval.ApprovedAt)
	}
}

func TestApproveCommandRejectsAbandonedPlanWithoutMutation(t *testing.T) {
	const planID = "20260430-1200-abandoned"
	const sliceID = "001-a"
	repo := blockedApproveRepo(planID, sliceID, plan.StatusAbandoned)
	detail, err := repo.GetPlan(context.Background(), planID)
	if err != nil {
		t.Fatal(err)
	}
	detail.Events = append(detail.Events, plan.Event{Type: plan.EventTypePlanAbandoned, Reason: "superseded by safer work"})
	repo = plantest.NewRepository()
	repo.AddDetail(detail)
	var out bytes.Buffer

	err = (newTestApp(App{Out: &out, Err: &out})).approve(context.Background(), repo, []string{"--by", "Seth", planID})
	if err == nil || !strings.Contains(err.Error(), "plan "+planID+" is abandoned: superseded by safer work") {
		t.Fatalf("approve error = %v", err)
	}
	updated, resolveErr := repo.GetPlan(context.Background(), planID)
	if resolveErr != nil {
		t.Fatal(resolveErr)
	}
	slice := findSlice(updated, sliceID)
	if slice == nil || slice.Approval == nil || slice.Approval.Approved {
		t.Fatalf("abandoned approval mutated slice: %#v", slice)
	}
	if out.Len() != 0 {
		t.Fatalf("abandoned approval emitted output: %q", out.String())
	}
}

func TestApproveCommandRejectsNonGatedSlice(t *testing.T) {
	const planID = "20260430-1200-run-plan"
	const sliceID = "001-a"
	detail := plantest.NewPlanDetail(planID).
		WithStatus(plan.StatusPlanned).
		WithPendingSlices(sliceID).
		AddSlice(plantest.NewSlice(sliceID).WithStatus(plan.StatusPending).Build()).
		Build()
	repo := plantest.NewRepository()
	repo.AddDetail(detail)

	var out bytes.Buffer
	app := newTestApp(App{Out: &out, Err: &out, Repository: func(_ string) Repository { return repo }})
	err := app.Run(context.Background(), []string{"approve", "--by", "Seth", planID})
	if err == nil || !strings.Contains(err.Error(), "does not require approval") {
		t.Fatalf("expected non-gated rejection, got %v", err)
	}
}

// fileApproveFixture writes a file-backed plan with one approval-gated blocked
// slice so tests can inspect the raw slices.json and events.jsonl bytes.
func fileApproveFixture(t *testing.T) (root, dir, planID, sliceID string) {
	t.Helper()
	fixture := newRunPlanFixture(t, plan.StatusBlocked, []string{"001-a"}, nil, "001-a", plan.StatusBlocked)
	detail, err := plan.NewFileRepository(fixture.root).ResolvePlan(context.Background(), fixture.id)
	if err != nil {
		t.Fatal(err)
	}
	detail.State.Plan.CurrentSlice = &detail.Slices.Slices[0].ID
	detail.Slices.Slices[0].Approval = plantest.Approval(true, "human approval", false)
	record, err := plan.NewPlanRecord(fixture.dir, detail)
	if err != nil {
		t.Fatal(err)
	}
	if err := record.PersistArtifacts(); err != nil {
		t.Fatal(err)
	}
	return fixture.root, fixture.dir, fixture.id, "001-a"
}

func TestApproveCommandViaRecordsProvenanceLabel(t *testing.T) {
	root, dir, planID, sliceID := fileApproveFixture(t)
	const label = "claude-code-workflow wf_43dba409-ce8"
	var out bytes.Buffer
	app := newTestApp(App{Out: &out, Err: &out, Repository: func(string) Repository { return plan.NewFileRepository(root) }})
	if err := app.Run(context.Background(), []string{"approve", "--by", "Seth", "--via", label, planID}); err != nil {
		t.Fatal(err)
	}
	detail, err := plan.NewFileRepository(root).ResolvePlan(context.Background(), planID)
	if err != nil {
		t.Fatal(err)
	}
	sl := findSlice(detail, sliceID)
	if sl == nil || sl.Approval == nil || !sl.Approval.Approved || sl.Approval.Via == nil || *sl.Approval.Via != label {
		t.Fatalf("approval = %+v, want via %q", sl, label)
	}
	found := false
	for _, event := range detail.Events {
		if event.Type == plan.EventTypeSliceApproved {
			found = true
			if event.Via != label || event.Actor != "operator" || event.Trigger != "approve" {
				t.Fatalf("slice_approved event = %+v, want via %q", event, label)
			}
		}
	}
	if !found {
		t.Fatalf("expected slice_approved event, got %+v", detail.Events)
	}
	if !strings.Contains(readText(t, filepath.Join(dir, "slices.json")), `"via": "`+label+`"`) {
		t.Fatalf("slices.json missing via: %s", readText(t, filepath.Join(dir, "slices.json")))
	}
}

func TestApproveCommandWithoutViaOmitsField(t *testing.T) {
	root, dir, planID, sliceID := fileApproveFixture(t)
	var out bytes.Buffer
	app := newTestApp(App{Out: &out, Err: &out, Repository: func(string) Repository { return plan.NewFileRepository(root) }})
	if err := app.Run(context.Background(), []string{"approve", "--by", "Seth", planID}); err != nil {
		t.Fatal(err)
	}
	detail, err := plan.NewFileRepository(root).ResolvePlan(context.Background(), planID)
	if err != nil {
		t.Fatal(err)
	}
	if sl := findSlice(detail, sliceID); sl == nil || sl.Approval == nil || !sl.Approval.Approved {
		t.Fatalf("expected approval, got %+v", sl)
	}
	for _, name := range []string{"slices.json", "events.jsonl"} {
		if text := readText(t, filepath.Join(dir, name)); strings.Contains(text, `"via"`) {
			t.Fatalf("%s unexpectedly contains a via key:\n%s", name, text)
		}
	}
}

func TestApproveCommandRejectsInvalidViaBeforeMutation(t *testing.T) {
	for name, label := range map[string]string{
		"oversized": strings.Repeat("x", 129),
		"newline":   "orchestrator\nrun",
	} {
		t.Run(name, func(t *testing.T) {
			root, _, planID, sliceID := fileApproveFixture(t)
			var out bytes.Buffer
			app := newTestApp(App{Out: &out, Err: &out, Repository: func(string) Repository { return plan.NewFileRepository(root) }})
			err := app.Run(context.Background(), []string{"approve", "--by", "Seth", "--via", label, planID})
			if err == nil || !strings.Contains(err.Error(), "--via:") {
				t.Fatalf("approve error = %v, want --via rejection", err)
			}
			detail, err := plan.NewFileRepository(root).ResolvePlan(context.Background(), planID)
			if err != nil {
				t.Fatal(err)
			}
			if sl := findSlice(detail, sliceID); sl == nil || sl.Approval == nil || sl.Approval.Approved {
				t.Fatalf("invalid via mutated approval: %+v", sl)
			}
			for _, event := range detail.Events {
				if event.Type == plan.EventTypeSliceApproved {
					t.Fatalf("invalid via appended slice_approved: %+v", event)
				}
			}
		})
	}
}
