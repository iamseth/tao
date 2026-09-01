package cli

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/iamseth/tao/internal/plan"
	reworkpkg "github.com/iamseth/tao/internal/rework"
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
	for _, want := range []string{
		"Rework slices created for " + planID,
		"- r101-internal-cli-rework-go",
		"Next: tao run " + planID,
		"Reason: the active slice was interrupted before a durable commit intent",
	} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("expected output to contain %q, got %q", want, out.String())
		}
	}
}

func TestReworkCommandRegistersPullRequestFlagsAndRejectsUnsupportedCombinations(t *testing.T) {
	fs := flag.NewFlagSet("rework", flag.ContinueOnError)
	registerReworkFlags(fs)
	for _, name := range []string{"from-pr", "from-authors", "dry-run"} {
		if fs.Lookup(name) == nil {
			t.Fatalf("rework flag --%s is not registered", name)
		}
	}
	if got := fs.Lookup("from-authors").DefValue; got != "owner" {
		t.Fatalf("--from-authors default = %q, want owner", got)
	}

	tests := []struct {
		args []string
		want string
	}{
		{args: []string{"--from-authors", "all"}, want: "--from-authors requires --from-pr"},
		{args: []string{"--dry-run"}, want: "--dry-run requires --from-pr"},
		{args: []string{"--from-pr", "--force"}, want: "cannot be combined with --force"},
		{args: []string{"--from-pr", "--dry-run", "--run"}, want: "cannot be combined with --run"},
		{args: []string{"--from-pr", "--from-authors", "team"}, want: "want owner or all"},
	}
	for _, test := range tests {
		parsed := flag.NewFlagSet("rework", flag.ContinueOnError)
		registerReworkFlags(parsed)
		if err := parsed.Parse(test.args); err != nil {
			t.Fatal(err)
		}
		err := validateReworkFlagCombination(parsed, flagBoolValue(parsed, "from-pr"), flagBoolValue(parsed, "force"), flagBoolValue(parsed, "run"), flagBoolValue(parsed, "dry-run"), reworkpkg.PRThreadAuthorScope(flagStringValue(parsed, "from-authors")))
		if err == nil || !strings.Contains(err.Error(), test.want) {
			t.Fatalf("flags %v error = %v, want %q", test.args, err, test.want)
		}
	}
}

func TestReworkFromPullRequestRefusesPlanWithoutRecordedPullRequest(t *testing.T) {
	root := t.TempDir()
	planID := "20260628-1200-no-pr"
	writeCLIReworkPlan(t, root, planID, plan.StatusCompleted, reworkReview(plan.ReviewVerdictApprove, nil))

	err := (App{Out: &bytes.Buffer{}}).Run(context.Background(), []string{"--plans-dir", root, "rework", "--from-pr", planID})
	if err == nil || !strings.Contains(err.Error(), "requires plan "+planID+" to have a recorded pull request") {
		t.Fatalf("rework --from-pr error = %v, want missing recorded pull request", err)
	}
}

func TestReworkFromPullRequestDryRunPersistsAndPrintsTriageWithoutSlices(t *testing.T) {
	root := t.TempDir()
	planID := "20260628-1200-pr-dry-run"
	planDir := writeCLIReworkPlan(t, root, planID, plan.StatusCompleted, reworkReview(plan.ReviewVerdictApprove, nil))
	addCLIReworkPullRequest(t, planDir)
	thread := reworkpkg.PRThread{NodeID: "PRRT_change", Path: "internal/cli/rework.go", Comments: []reworkpkg.PRThreadComment{{AuthorLogin: "owner", Body: "Please fix this."}}}
	stubCLIReworkPRPipeline(t, []reworkpkg.PRThread{thread}, []reworkpkg.PRThreadClassification{{ThreadNodeID: thread.NodeID, Kind: reworkpkg.PRThreadKindChange, Rationale: "The behavior needs correction."}})
	var out bytes.Buffer
	fixed := time.Date(2026, 6, 28, 13, 0, 0, 0, time.UTC)

	if err := (App{Out: &out, Now: func() time.Time { return fixed }}).Run(context.Background(), []string{"--plans-dir", root, "rework", "--from-pr", "--dry-run", planID}); err != nil {
		t.Fatal(err)
	}
	detail, err := plan.NewFileRepository(root).ResolvePlan(context.Background(), planID)
	if err != nil {
		t.Fatal(err)
	}
	if detail.State.Status != plan.StatusCompleted || len(detail.State.Plan.PendingSlices) != 0 || len(detail.Slices.Slices) != 1 {
		t.Fatalf("dry run reopened or generated slices: status=%q pending=%#v slices=%d", detail.State.Status, detail.State.Plan.PendingSlices, len(detail.Slices.Slices))
	}
	if got := detail.State.Plan.PRFeedbackTriage[thread.NodeID]; got.Kind != "change" || got.Rationale == "" {
		t.Fatalf("persisted triage = %#v", detail.State.Plan.PRFeedbackTriage)
	}
	for _, want := range []string{"PATH", "AUTHOR", "CLASSIFICATION", "ACTION", "internal/cli/rework.go", "owner", "change", "create rework slice", "Dry run: triage persisted"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("dry-run output missing %q:\n%s", want, out.String())
		}
	}
}

func TestReworkFromPullRequestDoesNotReconvertConsumedThreadAfterCompletedCycle(t *testing.T) {
	root := t.TempDir()
	planID := "20260628-1200-pr-consumed"
	planDir := writeCLIReworkPlan(t, root, planID, plan.StatusCompleted, reworkReview(plan.ReviewVerdictApprove, nil))
	addCLIReworkPullRequest(t, planDir)
	original := reworkpkg.PRThread{NodeID: "PRRT_change", Path: "internal/cli/rework.go", Comments: []reworkpkg.PRThreadComment{{AuthorLogin: "owner", Body: "Please fix this."}}}
	originalClassification := reworkpkg.PRThreadClassification{ThreadNodeID: original.NodeID, Kind: reworkpkg.PRThreadKindChange, Rationale: "The behavior needs correction."}
	stubCLIReworkPRPipeline(t, []reworkpkg.PRThread{original}, []reworkpkg.PRThreadClassification{originalClassification})
	firstAt := time.Date(2026, 6, 28, 13, 0, 0, 0, time.UTC)

	if err := (App{Out: &bytes.Buffer{}, Now: func() time.Time { return firstAt }}).Run(context.Background(), []string{"--plans-dir", root, "rework", "--from-pr", planID}); err != nil {
		t.Fatal(err)
	}
	repo := plan.NewFileRepository(root)
	detail, err := repo.ResolvePlan(context.Background(), planID)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(detail.State.Plan.PRFeedbackConsumedThreadIDs, []string{original.NodeID}) || len(detail.State.Plan.PendingSlices) != 1 {
		t.Fatalf("first reopen = consumed %#v pending %#v", detail.State.Plan.PRFeedbackConsumedThreadIDs, detail.State.Plan.PendingSlices)
	}

	// Complete and re-approve the generated work, then refresh the recorded pull
	// request to the new head before simulating another --from-pr invocation.
	record, err := repo.PlanRecord(detail)
	if err != nil {
		t.Fatal(err)
	}
	sliceID := detail.State.Plan.PendingSlices[0]
	if err := record.StartSlice(sliceID, firstAt.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := record.CompleteSlice(sliceID, "fixed", nil, firstAt.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	newHead := "head789"
	if err := record.RecordReviewCompleted(plan.PlanReview{Status: plan.ReviewStatusCompleted, Verdict: plan.ReviewVerdictApprove, Head: newHead, ReviewedAt: firstAt.Add(3 * time.Minute)}, "pi"); err != nil {
		t.Fatal(err)
	}
	if err := record.RecordPullRequest(plan.PullRequest{Number: 42, URL: "https://github.com/iamseth/tao/pull/42", CreatedAt: firstAt.Add(4 * time.Minute)}, "feature", newHead); err != nil {
		t.Fatal(err)
	}

	stubCLIReworkPRPipeline(t, []reworkpkg.PRThread{original}, []reworkpkg.PRThreadClassification{originalClassification})
	err = (App{Out: &bytes.Buffer{}, Now: func() time.Time { return firstAt.Add(5 * time.Minute) }}).Run(context.Background(), []string{"--plans-dir", root, "rework", "--from-pr", planID})
	if err == nil || !strings.Contains(err.Error(), "no change-request threads to convert") {
		t.Fatalf("second --from-pr error = %v, want consumed-thread refusal", err)
	}
	detail, err = repo.ResolvePlan(context.Background(), planID)
	if err != nil {
		t.Fatal(err)
	}
	if detail.State.Status != plan.StatusCompleted || len(detail.State.Plan.PendingSlices) != 0 || len(detail.Slices.Slices) != 2 {
		t.Fatalf("consumed thread created duplicate work: status=%q pending=%#v slices=%d", detail.State.Status, detail.State.Plan.PendingSlices, len(detail.Slices.Slices))
	}

	newThread := reworkpkg.PRThread{NodeID: "PRRT_new", Path: "internal/cli/rework_test.go", Comments: []reworkpkg.PRThreadComment{{AuthorLogin: "owner", Body: "Please address this new issue."}}}
	stubCLIReworkPRPipeline(t, []reworkpkg.PRThread{original, newThread}, []reworkpkg.PRThreadClassification{
		originalClassification,
		{ThreadNodeID: newThread.NodeID, Kind: reworkpkg.PRThreadKindChange, Rationale: "A newly arrived request."},
	})
	if err := (App{Out: &bytes.Buffer{}, Now: func() time.Time { return firstAt.Add(6 * time.Minute) }}).Run(context.Background(), []string{"--plans-dir", root, "rework", "--from-pr", planID}); err != nil {
		t.Fatalf("new thread --from-pr: %v", err)
	}
	detail, err = repo.ResolvePlan(context.Background(), planID)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(detail.State.Plan.PRFeedbackConsumedThreadIDs, []string{original.NodeID, newThread.NodeID}) || len(detail.State.Plan.PendingSlices) != 1 {
		t.Fatalf("new-thread reopen = consumed %#v pending %#v", detail.State.Plan.PRFeedbackConsumedThreadIDs, detail.State.Plan.PendingSlices)
	}
}

func TestReworkFromPullRequestAllQuestionsProducesNoSlices(t *testing.T) {
	root := t.TempDir()
	planID := "20260628-1200-pr-questions"
	planDir := writeCLIReworkPlan(t, root, planID, plan.StatusCompleted, reworkReview(plan.ReviewVerdictApprove, nil))
	addCLIReworkPullRequest(t, planDir)
	threads := []reworkpkg.PRThread{
		{NodeID: "PRRT_q1", Path: "internal/cli/rework.go", Comments: []reworkpkg.PRThreadComment{{AuthorLogin: "owner"}}},
		{NodeID: "PRRT_q2", Path: "internal/cli/rework_test.go", Comments: []reworkpkg.PRThreadComment{{AuthorLogin: "reviewer"}}},
	}
	stubCLIReworkPRPipeline(t, threads, []reworkpkg.PRThreadClassification{
		{ThreadNodeID: "PRRT_q1", Kind: reworkpkg.PRThreadKindQuestion, Rationale: "This asks for an explanation."},
		{ThreadNodeID: "PRRT_q2", Kind: reworkpkg.PRThreadKindQuestion, Rationale: "This asks about intent."},
	})
	var out bytes.Buffer

	err := (App{Out: &out}).Run(context.Background(), []string{"--plans-dir", root, "rework", "--from-pr", "--from-authors", "all", planID})
	if err == nil || !strings.Contains(err.Error(), "no change-request threads to convert") {
		t.Fatalf("all-question rework error = %v", err)
	}
	detail, resolveErr := plan.NewFileRepository(root).ResolvePlan(context.Background(), planID)
	if resolveErr != nil {
		t.Fatal(resolveErr)
	}
	if detail.State.Status != plan.StatusCompleted || len(detail.State.Plan.PendingSlices) != 0 || len(detail.Slices.Slices) != 1 {
		t.Fatalf("all-question triage generated work: status=%q pending=%#v slices=%d", detail.State.Status, detail.State.Plan.PendingSlices, len(detail.Slices.Slices))
	}
	if strings.Count(out.String(), "report question") != 2 {
		t.Fatalf("question triage output = %q", out.String())
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

func TestReworkCommandRejectsAbandonedPlanAcrossAuthorityArmsWithoutSideEffects(t *testing.T) {
	oldRead := readReworkPRThreads
	prReads := 0
	readReworkPRThreads = func(context.Context, App, reworkpkg.PRThreadReadRequest) (reworkpkg.PRThreadReadResult, error) {
		prReads++
		return reworkpkg.PRThreadReadResult{}, nil
	}
	t.Cleanup(func() { readReworkPRThreads = oldRead })

	for _, test := range []struct {
		name string
		args []string
	}{
		{name: "ordinary", args: nil},
		{name: "forced", args: []string{"--force"}},
		{name: "from pull request", args: []string{"--from-pr"}},
		{name: "from pull request dry run", args: []string{"--from-pr", "--dry-run"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			planID := "20260628-1200-abandoned-" + strings.ReplaceAll(test.name, " ", "-")
			finding := plan.ReviewFinding{File: "internal/cli/rework.go", Message: "must not reopen"}
			planDir := writeCLIReworkPlan(t, root, planID, plan.StatusAbandoned, reworkReview(plan.ReviewVerdictChangesRequested, []plan.ReviewFinding{finding}))
			addCLIReworkPullRequest(t, planDir)
			before := readReworkArtifacts(t, planDir)
			var out bytes.Buffer
			args := append([]string{"--plans-dir", root, "rework"}, test.args...)
			args = append(args, planID)

			err := (App{Out: &out, Err: &out}).Run(context.Background(), args)
			if err == nil || !strings.Contains(err.Error(), "plan "+planID+" is abandoned") {
				t.Fatalf("rework error = %v", err)
			}
			if after := readReworkArtifacts(t, planDir); after != before {
				t.Fatalf("abandoned rework mutated artifacts\nbefore:\n%s\nafter:\n%s", before, after)
			}
			if out.Len() != 0 {
				t.Fatalf("abandoned rework emitted output: %q", out.String())
			}
		})
	}
	if prReads != 0 {
		t.Fatalf("abandoned --from-pr read forge threads %d times", prReads)
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

func addCLIReworkPullRequest(t *testing.T, planDir string) {
	t.Helper()
	repo := plan.NewFileRepository(filepath.Dir(planDir))
	detail, err := repo.ResolvePlan(context.Background(), planDir)
	if err != nil {
		t.Fatal(err)
	}
	detail.State.Plan.PullRequest = &plan.PullRequest{
		Number: 42, URL: "https://github.com/iamseth/tao/pull/42", Branch: "feature", HeadSHA: "head456",
		CreatedAt: time.Date(2026, 6, 28, 12, 5, 0, 0, time.UTC),
	}
	record, err := repo.PlanRecord(detail)
	if err != nil {
		t.Fatal(err)
	}
	if err := record.PersistArtifacts(); err != nil {
		t.Fatal(err)
	}
}

func stubCLIReworkPRPipeline(t *testing.T, threads []reworkpkg.PRThread, classifications []reworkpkg.PRThreadClassification) {
	t.Helper()
	oldRead := readReworkPRThreads
	oldClassify := classifyReworkPRThreads
	readReworkPRThreads = func(_ context.Context, _ App, request reworkpkg.PRThreadReadRequest) (reworkpkg.PRThreadReadResult, error) {
		if request.RepositoryOwner != "iamseth" || request.RepositoryName != "tao" || request.PullRequestNumber != 42 {
			t.Fatalf("pull-request read request = %#v", request)
		}
		return reworkpkg.PRThreadReadResult{OwnerLogin: "owner", Threads: threads}, nil
	}
	classifyReworkPRThreads = func(_ context.Context, _ App, _ string, got []reworkpkg.PRThread) ([]reworkpkg.PRThreadClassification, error) {
		if len(got) != len(threads) {
			t.Fatalf("classified threads = %d, want %d", len(got), len(threads))
		}
		return classifications, nil
	}
	t.Cleanup(func() {
		readReworkPRThreads = oldRead
		classifyReworkPRThreads = oldClassify
	})
}

func readReworkArtifacts(t *testing.T, planDir string) string {
	t.Helper()
	return strings.Join([]string{
		readText(t, filepath.Join(planDir, "state.json")),
		readText(t, filepath.Join(planDir, "slices.json")),
		readText(t, filepath.Join(planDir, "events.jsonl")),
	}, "\n---\n")
}
