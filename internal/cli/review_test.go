package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/iamseth/tao/internal/plan"
)

func TestStalenessReportsChangedExpectedFilesSinceBaseCommit(t *testing.T) {
	detail := &plan.PlanDetail{
		State: plan.State{
			Repo: plan.Repo{Root: "/repo", BaseCommit: "aaaaaaaaaaaa1111"},
			Plan: plan.PlanState{ID: "plan-a", PendingSlices: []string{"001-a", "002-b"}},
		},
		Slices: plan.SlicesFile{Slices: []plan.Slice{
			{ID: "001-a", Status: plan.StatusPending, ExpectedFiles: []string{"internal/run/run.go", "README.md"}},
			{ID: "002-b", Status: plan.StatusPending, ExpectedFiles: []string{"docs"}},
		}},
	}
	repo := fakeRepository{details: map[string]*plan.PlanDetail{"plan-a": detail}}
	var out bytes.Buffer
	app := App{Out: &out, CommandRunner: reviewFakeRunner(map[string]string{
		"rev-parse HEAD": "bbbbbbbbbbbb2222\n",
		"merge-base --is-ancestor aaaaaaaaaaaa1111 HEAD": "",
		"diff --name-only aaaaaaaaaaaa1111..HEAD":        "internal/run/run.go\ndocs/plan-format.md\n",
	}, nil)}

	if err := app.staleness(context.Background(), repo, []string{"plan-a"}); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	for _, want := range []string{"Staleness: plan-a", "repository HEAD changed", "2 file(s) changed", "pending slice 001-a expects file(s) changed since planning: internal/run/run.go", "pending slice 002-b expects file(s) changed since planning: docs"} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected staleness output to contain %q, got %q", want, text)
		}
	}
}

func TestStalenessWarnsWhenBaseCommitIsNotAncestor(t *testing.T) {
	detail := &plan.PlanDetail{
		State: plan.State{
			Repo: plan.Repo{Root: "/repo", BaseCommit: "aaaaaaaaaaaa1111"},
			Plan: plan.PlanState{ID: "plan-a"},
		},
	}
	repo := fakeRepository{details: map[string]*plan.PlanDetail{"plan-a": detail}}
	var out bytes.Buffer
	app := App{Out: &out, CommandRunner: reviewFakeRunner(map[string]string{
		"rev-parse HEAD": "bbbbbbbbbbbb2222\n",
		"diff --name-only aaaaaaaaaaaa1111..HEAD": "",
	}, map[string]error{
		"merge-base --is-ancestor aaaaaaaaaaaa1111 HEAD": errors.New("exit status 1"),
	})}

	if err := app.staleness(context.Background(), repo, []string{"plan-a"}); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	want := "recorded base commit is not an ancestor of current HEAD; the plan may have been created on a different history"
	if count := strings.Count(text, want); count != 1 {
		t.Fatalf("expected one non-ancestor warning, got count=%d output=%q", count, text)
	}
	if strings.Contains(text, "could not confirm recorded base") {
		t.Fatalf("expected collapsed ancestry warning, got %q", text)
	}
}

func TestStalenessWarnsWhenBaseCommitMissing(t *testing.T) {
	detail := &plan.PlanDetail{State: plan.State{Repo: plan.Repo{Root: "/repo"}, Plan: plan.PlanState{ID: "plan-a"}}}
	repo := fakeRepository{details: map[string]*plan.PlanDetail{"plan-a": detail}}
	var out bytes.Buffer
	app := App{Out: &out, CommandRunner: reviewFakeRunner(nil, nil)}

	if err := app.staleness(context.Background(), repo, []string{"plan-a"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "no recorded repo.base_commit") {
		t.Fatalf("expected missing base warning, got %q", out.String())
	}
}

func TestReviewPrintsPersistedReviewArtifact(t *testing.T) {
	detail := &plan.PlanDetail{
		State:  plan.State{Plan: plan.PlanState{ID: "plan-a", Review: &plan.PlanReview{Verdict: "approve", Summary: "ready"}}},
		Review: plan.PlanReviewArtifact{Content: "# Review\nLooks good."},
	}
	repo := fakeRepository{details: map[string]*plan.PlanDetail{"plan-a": detail}}
	var out bytes.Buffer

	if err := (App{Out: &out, Err: &out}).review(context.Background(), repo, []string{"plan-a"}); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); got != "# Review\nLooks good.\n" {
		t.Fatalf("expected persisted review artifact, got %q", got)
	}
}

func TestReviewPrintsStateReviewWhenArtifactMissing(t *testing.T) {
	reviewedAt := time.Date(2026, 6, 28, 15, 0, 0, 0, time.UTC)
	detail := &plan.PlanDetail{State: plan.State{Plan: plan.PlanState{ID: "plan-a", Review: &plan.PlanReview{Status: plan.ReviewStatusCompleted, Verdict: "approve", Summary: "ready to merge", FindingsCount: 2, CommitMessage: &plan.ReviewCommitMessage{Subject: "feat(review): persist approved commit proposals", Body: "What:\nPersist the proposal.\n\nWhy:\nReuse reviewed context."}, Base: "base123", Head: "head456", Agent: "pi", ReviewedAt: reviewedAt}}}}
	repo := fakeRepository{details: map[string]*plan.PlanDetail{"plan-a": detail}}
	var out bytes.Buffer

	if err := (App{Out: &out, Err: &out}).review(context.Background(), repo, []string{"plan-a"}); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	for _, want := range []string{"Review: plan-a", "Status: completed", "Verdict: approve", "Summary: ready to merge", "Commit Subject: feat(review): persist approved commit proposals", "Commit Body:\nWhat:\nPersist the proposal.\n\nWhy:\nReuse reviewed context.", "Findings: 2", "Base: base123", "Head: head456", "Agent: pi", "Reviewed At: 2026-06-28T15:00:00Z"} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected review summary to contain %q, got %q", want, text)
		}
	}
}

// TestReviewSuppressesStaleNextStepHints guards `tao review <plan>` output
// against advertising actions that no longer apply: a merged plan must not
// suggest `tao merge`, and a review superseded by a reopen must not suggest
// reworking the stale verdict — unattended tooling parses these hints.
func TestReviewSuppressesStaleNextStepHints(t *testing.T) {
	renderFor := func(t *testing.T, detail *plan.PlanDetail) string {
		t.Helper()
		repo := fakeRepository{details: map[string]*plan.PlanDetail{"plan-a": detail}}
		var out bytes.Buffer
		if err := (App{Out: &out, Err: &out}).review(context.Background(), repo, []string{"plan-a"}); err != nil {
			t.Fatal(err)
		}
		return out.String()
	}

	t.Run("merged plan drops merge hint", func(t *testing.T) {
		text := renderFor(t, &plan.PlanDetail{
			State:  plan.State{Plan: plan.PlanState{ID: "plan-a", Review: &plan.PlanReview{Status: plan.ReviewStatusCompleted, Verdict: plan.ReviewVerdictApprove}}},
			Events: []plan.Event{{Type: plan.EventTypePlanMerged}},
		})
		if strings.Contains(text, "Next: tao merge") {
			t.Fatalf("merged plan must not advertise tao merge, got %q", text)
		}
		if !strings.Contains(text, "already merged") {
			t.Fatalf("expected already-merged notice, got %q", text)
		}
	})

	t.Run("superseded review drops rework hint", func(t *testing.T) {
		text := renderFor(t, &plan.PlanDetail{
			State: plan.State{Plan: plan.PlanState{ID: "plan-a", Review: &plan.PlanReview{Status: plan.ReviewStatusCompleted, Verdict: plan.ReviewVerdictChangesRequested}}},
			Events: []plan.Event{
				{Type: plan.EventTypePlanReviewed},
				{Type: plan.EventTypePlanReopened},
			},
		})
		if strings.Contains(text, "Next: tao rework") {
			t.Fatalf("superseded review must not advertise tao rework, got %q", text)
		}
		if !strings.Contains(text, "superseded by reopen") {
			t.Fatalf("expected superseded notice, got %q", text)
		}
	})

	t.Run("superseded artifact is labeled historical", func(t *testing.T) {
		text := renderFor(t, &plan.PlanDetail{
			State:  plan.State{Plan: plan.PlanState{ID: "plan-a", Review: &plan.PlanReview{Status: plan.ReviewStatusCompleted, Verdict: plan.ReviewVerdictChangesRequested}}},
			Review: plan.PlanReviewArtifact{Content: "# Review\nChanges requested."},
			Events: []plan.Event{
				{Type: plan.EventTypePlanReviewed},
				{Type: plan.EventTypePlanReopened},
			},
		})
		for _, want := range []string{"Review superseded by reopen", "Next: tao review --run plan-a", "Historical review content:", "# Review\nChanges requested.\n"} {
			if !strings.Contains(text, want) {
				t.Fatalf("expected superseded artifact output to contain %q, got %q", want, text)
			}
		}
		for _, notWant := range []string{"Next: tao rework", "Next: tao merge"} {
			if strings.Contains(text, notWant) {
				t.Fatalf("superseded artifact must not advertise %q, got %q", notWant, text)
			}
		}
		if strings.Index(text, "Historical review content:") > strings.Index(text, "# Review") {
			t.Fatalf("historical label should precede artifact body, got %q", text)
		}
	})

	t.Run("current approved review keeps merge hint", func(t *testing.T) {
		text := renderFor(t, &plan.PlanDetail{
			State: plan.State{Plan: plan.PlanState{ID: "plan-a", Review: &plan.PlanReview{Status: plan.ReviewStatusCompleted, Verdict: plan.ReviewVerdictApprove}}},
		})
		if !strings.Contains(text, "Next: tao merge plan-a") {
			t.Fatalf("current approved review should advertise tao merge, got %q", text)
		}
	})
}

func TestReviewPrintsClearMessageWhenNoReviewExists(t *testing.T) {
	detail := &plan.PlanDetail{State: plan.State{Plan: plan.PlanState{ID: "plan-a"}}}
	repo := fakeRepository{details: map[string]*plan.PlanDetail{"plan-a": detail}}
	var out bytes.Buffer

	if err := (App{Out: &out, Err: &out}).review(context.Background(), repo, []string{"plan-a"}); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	for _, want := range []string{"No review yet for plan-a", "tao review --run plan-a"} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected no-review message to contain %q, got %q", want, text)
		}
	}
	for _, notWant := range []string{"Preparing review:", "Verifying completed branch:", "Running agent review:"} {
		if strings.Contains(text, notWant) {
			t.Fatalf("persisted review display emitted fresh-review phase %q: %q", notWant, text)
		}
	}
}

func TestReviewRunTriggersFreshReview(t *testing.T) {
	clearTaoEnv(t)
	fixture := newRunPlanFixture(t, plan.StatusCompleted, nil, []string{"001-a"}, "001-a", plan.StatusCompleted)
	reviewOutput := "Fresh review\n```tao-review-json\n{\"verdict\":\"approve\",\"summary\":\"ready\",\"commit_message\":{\"subject\":\"feat(review): persist approved commit proposals\",\"body\":\"What:\\nPersist the proposal for the exact reviewed diff.\\n\\nWhy:\\nReuse review context during merge.\"},\"findings\":[]}\n```"
	var out bytes.Buffer
	var prompt string
	app := App{Out: &out, Err: &out, CommandRunner: func(ctx context.Context, cwd string, name string, args []string, stdout io.Writer, stderr io.Writer) error {
		if name != "git" {
			t.Fatalf("unexpected command %s %v", name, args)
			return nil
		}
		switch reviewCommandKey(args) {
		case "status --porcelain":
			return nil
		case "rev-parse HEAD":
			_, _ = io.WriteString(stdout, "head123\n")
		default:
			t.Fatalf("unexpected git command %v", args)
		}
		return nil
	}, ProcessStarter: fakeCLIProcessStarter(t, reviewOutput, func(value string) {
		prompt = value
	})}

	if err := app.review(context.Background(), plan.NewFileRepository(fixture.root), []string{"--run", fixture.id}); err != nil {
		t.Fatal(err)
	}
	state, err := plan.ReadState(fixture.dir)
	if err != nil {
		t.Fatal(err)
	}
	if state.Plan.Review == nil || state.Plan.Review.Verdict != "approve" || state.Plan.Review.Summary != "ready" || state.Plan.Review.Head != "head123" || state.Plan.Review.CommitMessage == nil || state.Plan.Review.CommitMessage.Subject != "feat(review): persist approved commit proposals" {
		t.Fatalf("unexpected persisted review: %#v", state.Plan.Review)
	}
	if artifact := readText(t, filepath.Join(fixture.dir, plan.ReviewFile)); !strings.Contains(artifact, "Fresh review") {
		t.Fatalf("expected review artifact, got %q", artifact)
	}
	if !strings.Contains(prompt, "Plan directory: `"+fixture.dir+"`") || !strings.Contains(prompt, "Head: `head123`") {
		t.Fatalf("expected review prompt with plan dir and head, got %q", prompt)
	}
	text := out.String()
	if !strings.Contains(text, "Review completed: "+fixture.id) || !strings.Contains(text, "Verdict: approve") {
		t.Fatalf("expected review completion output, got %q", text)
	}
	previous := -1
	for _, phase := range []string{"Preparing review: " + fixture.id, "Verifying completed branch: ", "Running agent review: pi", "Review completed: " + fixture.id} {
		index := strings.Index(text, phase)
		if index < 0 || index <= previous {
			t.Fatalf("review phase %q missing or out of order in %q", phase, text)
		}
		previous = index
	}
	if strings.Contains(text, "\x1b[") {
		t.Fatalf("review progress contains terminal control sequence: %q", text)
	}
}

func reviewFakeRunner(outputs map[string]string, failures map[string]error) CommandRunner {
	return func(ctx context.Context, cwd string, name string, args []string, stdout io.Writer, stderr io.Writer) error {
		key := reviewCommandKey(args)
		if err := failures[key]; err != nil {
			return err
		}
		if out, ok := outputs[key]; ok {
			_, _ = io.WriteString(stdout, out)
		}
		return nil
	}
}

func reviewCommandKey(args []string) string {
	if len(args) >= 2 && args[0] == "-C" {
		args = args[2:]
	}
	return strings.Join(args, " ")
}
