package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/iamseth/tao/internal/plan"
	runpkg "github.com/iamseth/tao/internal/run"
)

func TestReworkCommandReopensChangesRequestedPlanWithGeneratedSlices(t *testing.T) {
	root := t.TempDir()
	planID := "20260628-1200-rework"
	finding := plan.ReviewFinding{Severity: "major", File: "internal/cli/rework.go", Line: 42, Message: "Handle rework findings", Suggestion: "Wire the command to the generator."}
	planDir := writeCLIReworkPlan(t, root, planID, plan.StatusCompleted, reworkReview(plan.ReviewVerdictChangesRequested, []plan.ReviewFinding{finding}))
	fixed := time.Date(2026, 6, 28, 12, 30, 0, 0, time.UTC)
	var out bytes.Buffer
	app := App{Out: &out, Err: &out, Now: func() time.Time { return fixed }}

	if err := app.Run(context.Background(), []string{"--plans-dir", root, "rew", "rework"}); err != nil {
		t.Fatal(err)
	}

	detail, err := plan.NewFileRepository(root).ResolvePlan(context.Background(), planID)
	if err != nil {
		t.Fatal(err)
	}
	if detail.State.Status != plan.StatusInProgress {
		t.Fatalf("status = %q, want in_progress", detail.State.Status)
	}
	if len(detail.State.Plan.PendingSlices) != 1 || detail.State.Plan.PendingSlices[0] != "r101-internal-cli-rework-go" {
		t.Fatalf("pending slices = %#v", detail.State.Plan.PendingSlices)
	}
	if len(detail.State.Plan.CompletedSlices) != 1 || detail.State.Plan.CompletedSlices[0] != "001-done" {
		t.Fatalf("completed slices changed: %#v", detail.State.Plan.CompletedSlices)
	}
	created := detail.Slices.Slices[len(detail.Slices.Slices)-1]
	if created.ID != "r101-internal-cli-rework-go" || created.Status != plan.StatusPending || created.Goal != "Handle rework findings" {
		t.Fatalf("unexpected generated slice: %#v", created)
	}
	if !created.Timing.CreatedAt.Equal(fixed) || !created.Timing.UpdatedAt.Equal(fixed) {
		t.Fatalf("generated slice timing = %#v, want %s", created.Timing, fixed)
	}
	wantVerification := []string{"go test ./internal/cli -run TestOld"}
	if !slices.Equal(created.Verification.Commands, wantVerification) {
		t.Fatalf("verification commands = %#v, want %#v", created.Verification.Commands, wantVerification)
	}
	if events := readText(t, filepath.Join(planDir, "events.jsonl")); !strings.Contains(events, `"type":"plan_reopened"`) {
		t.Fatalf("expected plan_reopened event, got %q", events)
	}
	for _, want := range []string{"Rework slices created for " + planID, "- r101-internal-cli-rework-go", "Next: tao run " + planID} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("expected output to contain %q, got %q", want, out.String())
		}
	}
}

func TestReworkRunRetainsLockOwnershipForNestedRun(t *testing.T) {
	root := t.TempDir()
	planID := "20260628-1200-rework-run-lock"
	finding := plan.ReviewFinding{Severity: "major", File: "internal/cli/rework.go", Message: "retain lock ownership"}
	planDir := writeCLIReworkPlan(t, root, planID, plan.StatusCompleted, reworkReview(plan.ReviewVerdictChangesRequested, []plan.ReviewFinding{finding}))
	nestedErr := errors.New("stop nested run")
	nestedRan := false
	oldExecutor := executeSinglePlan
	executeSinglePlan = func(service runpkg.Service, ctx context.Context, request runpkg.Request) error {
		return service.WithPlanRunLock(ctx, request, func(context.Context) error {
			nestedRan = true
			return nestedErr
		})
	}
	t.Cleanup(func() { executeSinglePlan = oldExecutor })

	err := (App{Out: &bytes.Buffer{}}).Run(context.Background(), []string{"--plans-dir", root, "rework", "--run", planID})
	if !errors.Is(err, nestedErr) {
		t.Fatalf("rework --run error = %v, want nested sentinel", err)
	}
	if !nestedRan {
		t.Fatal("nested run did not recognize retained lock ownership")
	}
	if _, err := os.Stat(filepath.Join(planDir, ".run.lock")); !os.IsNotExist(err) {
		t.Fatalf("rework lock was not released after nested error: %v", err)
	}
}

func TestReworkCommandRefusesWithoutMutating(t *testing.T) {
	finding := plan.ReviewFinding{File: "internal/cli/rework.go", Message: "Fix it"}
	tests := []struct {
		name   string
		status string
		review *plan.PlanReview
		want   string
	}{
		{name: "approved", status: plan.StatusCompleted, review: reworkReview(plan.ReviewVerdictApprove, nil), want: "review verdict is approve"},
		{name: "awaiting review", status: plan.StatusInReview, review: reworkReview(plan.ReviewVerdictChangesRequested, []plan.ReviewFinding{finding}), want: "is awaiting review; run `tao review --run 20260628-1200-awaiting-review` first"},
		{name: "not completed", status: plan.StatusInProgress, review: reworkReview(plan.ReviewVerdictChangesRequested, []plan.ReviewFinding{finding}), want: "only reviewed plans can be reopened"},
		{name: "no findings", status: plan.StatusCompleted, review: reworkReview(plan.ReviewVerdictChangesRequested, nil), want: "no findings to convert"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			planID := "20260628-1200-" + strings.ReplaceAll(test.name, " ", "-")
			planDir := writeCLIReworkPlan(t, root, planID, test.status, test.review)
			before := readReworkArtifacts(t, planDir)
			var out bytes.Buffer
			app := App{Out: &out, Err: &out}

			err := app.Run(context.Background(), []string{"--plans-dir", root, "rework", planID})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("expected %q error, got %v", test.want, err)
			}
			if after := readReworkArtifacts(t, planDir); after != before {
				t.Fatalf("rework refusal mutated artifacts\nbefore:\n%s\nafter:\n%s", before, after)
			}
		})
	}
}

func TestReworkCommandForceBypassesReviewAndFindingGate(t *testing.T) {
	finding := plan.ReviewFinding{File: "internal/cli/rework.go", Message: "Fix it"}
	tests := []struct {
		name   string
		status string
		review *plan.PlanReview
	}{
		{name: "approved", status: plan.StatusCompleted, review: reworkReview(plan.ReviewVerdictApprove, nil)},
		{name: "not-completed", status: plan.StatusInProgress, review: reworkReview(plan.ReviewVerdictChangesRequested, []plan.ReviewFinding{finding})},
		{name: "no-findings", status: plan.StatusCompleted, review: reworkReview(plan.ReviewVerdictChangesRequested, nil)},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			planID := "20260628-1200-force-" + test.name
			writeCLIReworkPlan(t, root, planID, test.status, test.review)
			var out bytes.Buffer
			app := App{Out: &out, Err: &out}

			if err := app.Run(context.Background(), []string{"--plans-dir", root, "rework", "--force", planID}); err != nil {
				t.Fatal(err)
			}
			detail, err := plan.NewFileRepository(root).ResolvePlan(context.Background(), planID)
			if err != nil {
				t.Fatal(err)
			}
			if detail.State.Status != plan.StatusInProgress || !hasPendingReworkSlice(detail) {
				t.Fatalf("expected forced rework to reopen plan, got status=%q pending=%#v", detail.State.Status, detail.State.Plan.PendingSlices)
			}
			if !strings.Contains(out.String(), "Rework slices created for "+planID) {
				t.Fatalf("unexpected force output %q", out.String())
			}
		})
	}
}

func hasPendingReworkSlice(detail *plan.PlanDetail) bool {
	return slices.Contains(detail.State.Plan.PendingSlices, "r101-internal-cli-rework-go")
}

func reworkReview(verdict string, findings []plan.ReviewFinding) *plan.PlanReview {
	return &plan.PlanReview{
		Status:        plan.ReviewStatusCompleted,
		Verdict:       verdict,
		Summary:       "review summary",
		FindingsCount: len(findings),
		Findings:      findings,
		Base:          "base123",
		Head:          "head456",
		Agent:         "pi",
		ReviewedAt:    time.Date(2026, 6, 28, 12, 0, 0, 0, time.UTC),
	}
}

func writeCLIReworkPlan(t *testing.T, root string, planID string, status string, review *plan.PlanReview) string {
	t.Helper()
	planDir := filepath.Join(root, planID)
	if err := os.MkdirAll(planDir, 0o750); err != nil {
		t.Fatal(err)
	}
	repoRoot, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	created := time.Date(2026, 6, 28, 11, 0, 0, 0, time.UTC)
	completed := time.Date(2026, 6, 28, 11, 30, 0, 0, time.UTC)
	pending := []string{}
	completedAt := &completed
	if !plan.IsPostSliceStatus(status) {
		pending = []string{"002-pending"}
		completedAt = nil
	}
	state := plan.State{
		Schema:    "tao.plan.state.v1",
		Status:    status,
		CreatedAt: created,
		UpdatedAt: completed,
		Repo:      plan.Repo{Name: "tao", Root: repoRoot, Branch: "feature"},
		Plan: plan.PlanState{
			ID:              planID,
			Title:           "Rework Plan",
			CompletedSlices: []string{"001-done"},
			PendingSlices:   pending,
			Timing:          plan.PlanTiming{StartedAt: &created, CompletedAt: completedAt, LastActivityAt: &completed},
			Review:          review,
		},
		GlobalInvariants: []string{},
		OpenQuestions:    []string{},
	}
	slicesFile := plan.SlicesFile{
		Schema:    "tao.plan.slices.v1",
		PlanID:    planID,
		Execution: plan.Execution{Mode: "serial", ParallelSafe: false},
		Slices: []plan.Slice{{
			ID:            "001-done",
			Title:         "Done",
			Status:        plan.StatusCompleted,
			DependsOn:     []string{},
			Timing:        plan.SliceTiming{CreatedAt: created, StartedAt: &created, CompletedAt: &completed, UpdatedAt: completed, LastActivityAt: &completed},
			Goal:          "Original work",
			Context:       "Original completed slice",
			Tasks:         []string{"complete original work"},
			ExpectedFiles: []string{"internal/cli/rework.go"},
			Verification:  plan.Verification{Commands: []string{"go test ./internal/cli -run TestOld"}, ManualChecks: []string{}},
		}},
	}
	if !plan.IsPostSliceStatus(status) {
		slicesFile.Slices = append(slicesFile.Slices, plan.Slice{
			ID:            "002-pending",
			Title:         "Pending",
			Status:        plan.StatusPending,
			DependsOn:     []string{},
			Timing:        plan.SliceTiming{CreatedAt: created, UpdatedAt: created},
			Goal:          "Pending work",
			Context:       "Not completed yet",
			Tasks:         []string{"finish pending work"},
			ExpectedFiles: []string{"internal/cli/run.go"},
			Verification:  plan.Verification{Commands: []string{"go test ./internal/cli"}, ManualChecks: []string{}},
		})
	}
	record, err := plan.NewPlanRecord(planDir, &plan.PlanDetail{Dir: planDir, State: state, Slices: slicesFile})
	if err != nil {
		t.Fatal(err)
	}
	if err := record.PersistArtifacts(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(planDir, "events.jsonl"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	return planDir
}

func readReworkArtifacts(t *testing.T, planDir string) string {
	t.Helper()
	return strings.Join([]string{
		readText(t, filepath.Join(planDir, "state.json")),
		readText(t, filepath.Join(planDir, "slices.json")),
		readText(t, filepath.Join(planDir, "events.jsonl")),
	}, "\n---\n")
}
