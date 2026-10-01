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

	"github.com/iamseth/tao/internal/agent"
	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/internal/planning"
	"github.com/iamseth/tao/internal/runtimeconfig"
	"github.com/iamseth/tao/internal/taodata"
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

func TestReviewAgentUnavailableDoesNotFallback(t *testing.T) {
	fixture := newRunPlanFixture(t, plan.StatusCompleted, nil, []string{"001-a"}, "001-a", plan.StatusCompleted)
	calls := 0
	app := App{Out: io.Discard, Err: io.Discard, RuntimeEnv: snapshotWith(map[string]string{runtimeconfig.EnvAgent: "pi"}), Registry: func() NoteRegistry { return &fakeNoteRegistry{} }}
	app.CommandRunner = reviewFakeRunner(map[string]string{"status --porcelain": "", "rev-parse HEAD": "head123\n"}, nil)
	app.ProcessStarter = func(_ context.Context, _, name string, _ []string) (agent.Process, error) {
		calls++
		if name != "claude" {
			t.Fatalf("fell back to %s", name)
		}
		return nil, errors.New("review runtime unavailable")
	}
	err := app.review(context.Background(), plan.NewFileRepository(fixture.root), []string{"--run", "--review-agent=claude", fixture.id})
	if err == nil || !strings.Contains(err.Error(), "review runtime unavailable") || calls != 1 {
		t.Fatalf("error=%v calls=%d", err, calls)
	}
}

func TestReviewAgentRejectsInvalidFlags(t *testing.T) {
	app := App{Out: io.Discard, Err: io.Discard}
	for _, args := range [][]string{{"--review-agent=claude", "plan-a"}, {"--run", "--review-agent=invalid", "plan-a"}, {"--run", "--review-agent=", "plan-a"}} {
		if err := app.review(context.Background(), fakeRepository{}, args); err == nil || !strings.Contains(err.Error(), "--review-agent") {
			t.Fatalf("%v: %v", args, err)
		}
	}
}

func TestReviewPrintsPersistedReviewArtifact(t *testing.T) {
	detail := &plan.PlanDetail{
		State:  plan.State{Status: plan.StatusInReview, Plan: plan.PlanState{ID: "plan-a", Review: &plan.PlanReview{Verdict: "approve", Summary: "ready"}}},
		Review: plan.PlanReviewArtifact{Content: "# Review\nLooks good."},
	}
	repo := fakeRepository{details: map[string]*plan.PlanDetail{"plan-a": detail}}
	var out bytes.Buffer
	reporter := newRecordingCLIStatusReporter()

	if err := (App{Out: &out, Err: &out, StatusReporter: reporter}).review(context.Background(), repo, []string{"plan-a"}); err != nil {
		t.Fatal(err)
	}
	if len(reporter.calls) != 0 {
		t.Fatalf("persisted review display unexpectedly reported activity: %#v", reporter.calls)
	}
	if got := out.String(); got != "# Review\nLooks good.\nNext: tao review --run plan-a\nReason: completed slice work needs a current approved review\n" {
		t.Fatalf("expected persisted review artifact with projected recommendation and reason, got %q", got)
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
	for _, want := range []string{"Review: plan-a", "Review Status: completed", "Verdict: approve", "Summary: ready to merge", "Commit Subject: feat(review): persist approved commit proposals", "Commit Body:\nWhat:\nPersist the proposal.\n\nWhy:\nReuse reviewed context.", "Findings: 2", "Base: base123", "Head: head456", "Agent: pi", "Reviewed At: 2026-06-28T15:00:00Z"} {
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
			State:  plan.State{Status: plan.StatusInReview, Plan: plan.PlanState{ID: "plan-a", Review: &plan.PlanReview{Status: plan.ReviewStatusCompleted, Verdict: plan.ReviewVerdictChangesRequested}}},
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

	t.Run("reopened pending work advertises run before review", func(t *testing.T) {
		text := renderFor(t, &plan.PlanDetail{
			State: plan.State{Status: plan.StatusInProgress, Plan: plan.PlanState{ID: "plan-a", PendingSlices: []string{"r101-fix"}, Review: &plan.PlanReview{Status: plan.ReviewStatusCompleted, Verdict: plan.ReviewVerdictChangesRequested}}},
			Events: []plan.Event{
				{Type: plan.EventTypePlanReviewed},
				{Type: plan.EventTypePlanReopened},
			},
		})
		if !strings.Contains(text, "Next: tao run plan-a\nReason: the active slice was interrupted before a durable commit intent\n") || strings.Contains(text, "Next: tao review --run") || strings.Contains(text, "Next: tao rework") {
			t.Fatalf("reopened executable work should recommend and explain only tao run, got %q", text)
		}
	})

	t.Run("current approved review keeps merge hint", func(t *testing.T) {
		text := renderFor(t, &plan.PlanDetail{
			State: plan.State{Plan: plan.PlanState{ID: "plan-a", Review: &plan.PlanReview{Status: plan.ReviewStatusCompleted, Verdict: plan.ReviewVerdictApprove}}},
		})
		if !strings.Contains(text, "Next: tao merge plan-a\nReason: the current review approves the completed plan\n") {
			t.Fatalf("current approved review should recommend and explain tao merge, got %q", text)
		}
		for _, unsafe := range []string{"tao merge --force", "--no-squash", "--pull-request", "tao rework"} {
			if strings.Contains(text, unsafe) {
				t.Fatalf("ordinary approval guidance must not present %q alongside the safe default, got %q", unsafe, text)
			}
		}
	})

	t.Run("pull request completion points to host merge and cleanup", func(t *testing.T) {
		text := renderFor(t, &plan.PlanDetail{
			State: plan.State{Status: plan.StatusCompleted, Plan: plan.PlanState{
				ID:          "plan-a",
				Review:      &plan.PlanReview{Status: plan.ReviewStatusCompleted, Verdict: plan.ReviewVerdictApprove, Head: "head123"},
				PullRequest: &plan.PullRequest{HeadSHA: "head123"},
			}},
		})
		for _, want := range []string{"host's Squash and merge action", "`tao cleanup --dry-run`", "`tao cleanup`"} {
			if !strings.Contains(text, want) {
				t.Fatalf("PR-complete guidance should contain %q, got %q", want, text)
			}
		}
		if strings.Contains(text, "Next: tao merge") {
			t.Fatalf("PR-complete guidance must not advertise tao merge, got %q", text)
		}
	})
}

func TestReviewPrintsClearMessageWhenNoReviewExists(t *testing.T) {
	detail := &plan.PlanDetail{State: plan.State{Status: plan.StatusInReview, Plan: plan.PlanState{ID: "plan-a"}}}
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
	for _, tt := range []struct {
		name       string
		modelFlag  []string
		wantModel  string
		envOnly    bool
		baseOnly   bool
		malformed  bool
		verdict    string
		failNotice bool
	}{
		{name: "environment role", envOnly: true, wantModel: "env-review"},
		{name: "base fallback", envOnly: true, baseOnly: true, wantModel: "env-base"},
		{name: "repository base with environment role", baseOnly: true, wantModel: "env-review"},
		{name: "repository default", wantModel: "repo-review", verdict: "comment"},
		{name: "unrelated malformed settings", malformed: true, wantModel: "repo-review"},
		{name: "explicit override", modelFlag: []string{"--model", "provider/override"}, wantModel: "provider/override", verdict: "changes_requested"},
		{name: "notice write failure", wantModel: "repo-review", verdict: "comment", failNotice: true},
		{name: "empty inherits", modelFlag: []string{"--model="}, wantModel: "repo-review"},
		{name: "explicit reviewer", modelFlag: []string{"--review-agent=pi"}, wantModel: "repo-review"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			clearTaoEnv(t)
			t.Setenv(runtimeconfig.EnvAgent, "claude")
			t.Setenv(runtimeconfig.EnvReviewAgent, "claude")
			t.Setenv(runtimeconfig.EnvModel, "env-base")
			if !tt.baseOnly || !tt.envOnly {
				t.Setenv(runtimeconfig.EnvReviewModel, "env-review")
			}
			t.Setenv(runtimeconfig.EnvSessionTimeout, "37s")
			t.Setenv(runtimeconfig.EnvSkipPermissions, "false")
			for _, key := range []string{runtimeconfig.EnvCommitPolicy, runtimeconfig.EnvExecutionMode, runtimeconfig.EnvPullRequest, runtimeconfig.EnvReview, runtimeconfig.EnvAutoRework, runtimeconfig.EnvMaxReworkAttempts, runtimeconfig.EnvRunModel} {
				t.Setenv(key, "invalid value")
			}
			if !tt.malformed {
				t.Setenv(runtimeconfig.EnvCommitPolicy, "none")
				t.Setenv(runtimeconfig.EnvExecutionMode, "current")
			}
			registered := taodata.Repo{ID: "repo-a", RunDefaults: &taodata.RepoRunDefaults{PullRequest: new(true), Models: &taodata.RepoModelDefaults{Base: "repo-base", Review: "repo-review"}}}
			if tt.envOnly {
				registered.RunDefaults.Models = nil
			} else if tt.baseOnly {
				registered.RunDefaults.Models.Review = ""
			}
			registered = registered.WithReviewAgentDefault("pi")
			registry := &commandOptionsRegistry{fakeNoteRegistry: fakeNoteRegistry{current: registered}}

			fixture := newRunPlanFixture(t, plan.StatusCompleted, nil, []string{"001-a"}, "001-a", plan.StatusCompleted)
			if tt.verdict != "" {
				detail, err := plan.NewFileRepository(fixture.root).ResolvePlan(context.Background(), fixture.id)
				if err != nil {
					t.Fatal(err)
				}
				detail.State.Repo.BaseCommit = "base123"
				detail.State.Plan.Review = &plan.PlanReview{Status: "completed", Verdict: tt.verdict, Base: "base123", Head: "head123"}
				record, err := plan.NewPlanRecord(fixture.dir, detail)
				if err != nil {
					t.Fatal(err)
				}
				if err := record.PersistState(); err != nil {
					t.Fatal(err)
				}
			}
			reviewOutput := "Fresh review\n```tao-review-json\n{\"verdict\":\"approve\",\"summary\":\"ready\",\"commit_message\":{\"subject\":\"feat(review): persist approved commit proposals\",\"body\":\"What:\\nPersist the proposal for the exact reviewed diff.\\n\\nWhy:\\nReuse review context during merge.\"},\"findings\":[]}\n```"
			var out bytes.Buffer
			var prompt string
			reporter := newRecordingCLIStatusReporter()
			app := App{Out: &out, Err: &out, StatusReporter: reporter, CommandRunner: func(ctx context.Context, cwd string, name string, args []string, stdout io.Writer, stderr io.Writer) error {
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

			failingWriter := &noticeFailWriter{Buffer: &out}
			if tt.failNotice {
				app.Out = failingWriter
			}
			snapshot := runtimeconfig.RuntimeEnv()
			app.RuntimeEnv = &snapshot
			t.Setenv(runtimeconfig.EnvSessionTimeout, "invalid")
			t.Setenv(runtimeconfig.EnvReviewModel, "changed-after-capture")
			app.Registry = func() NoteRegistry { return registry }
			starter := app.ProcessStarter
			app.ProcessStarter = func(ctx context.Context, cwd, name string, args []string) (agent.Process, error) {
				if name != "pi" {
					t.Fatalf("wrong review runtime: %s", name)
				}
				if tt.failNotice && !failingWriter.attempted {
					t.Fatal("notice write was not attempted before startup")
				}
				if tt.verdict != "" && !tt.failNotice && !strings.Contains(out.String(), "unchanged committed review range") {
					t.Fatalf("notice missing before startup: %s", out.String())
				}
				if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) <= 0 || time.Until(deadline) > 37*time.Second {
					t.Fatalf("review session deadline = %v, present = %v", deadline, ok)
				}
				if len(args) < 2 || args[len(args)-2] != "--model" || args[len(args)-1] != tt.wantModel {
					t.Fatalf("review process args = %v, want model %q", args, tt.wantModel)
				}
				return starter(ctx, cwd, name, args[:len(args)-2])
			}
			args := append([]string{"--run", fixture.id}, tt.modelFlag...)
			if err := app.review(context.Background(), plan.NewFileRepository(fixture.root), args); err != nil {
				t.Fatal(err)
			}
			if registry.calls != 1 {
				t.Fatalf("repository lookups = %d, want 1", registry.calls)
			}
			reporter.requireCall(t, "run run-plan", "idle")
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
			if !strings.Contains(text, "Review completed: "+fixture.id) || !strings.Contains(text, "Review Status: completed") || !strings.Contains(text, "Verdict: approve") || !strings.Contains(text, "Next: tao merge "+fixture.id) {
				t.Fatalf("expected refreshed review completion and merge guidance, got %q", text)
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
		})
	}
}

func TestAuxiliaryGeneratorsInheritBaseModel(t *testing.T) {
	clearTaoEnv(t)
	t.Setenv(runtimeconfig.EnvModel, "provider/base")
	t.Setenv(runtimeconfig.EnvRunModel, "provider/run")
	t.Setenv(runtimeconfig.EnvReviewModel, "provider/review")
	snapshot := runtimeconfig.RuntimeEnv()
	defaults, err := (App{RuntimeEnv: &snapshot}).runEnvDefaults()
	if err != nil {
		t.Fatal(err)
	}
	app := App{Out: io.Discard, Err: io.Discard, RuntimeEnv: &snapshot, CommandRunner: reviewFakeRunner(nil, nil)}
	config, err := defaults.runConfig(runtimeconfig.RunOptionsPatch{})
	if err != nil {
		t.Fatal(err)
	}
	generator := app.planGenerator(config.ResolvedOptions())
	if got := generator.(*planning.Service).Model; got != "provider/base" {
		t.Fatalf("planning model = %q", got)
	}
	starter := fakeCLIProcessStarter(t, "triaged", nil)
	app.ProcessStarter = func(ctx context.Context, cwd, name string, args []string) (agent.Process, error) {
		if len(args) < 2 || args[len(args)-2] != "--model" || args[len(args)-1] != "provider/base" {
			t.Fatalf("triage process args = %v", args)
		}
		return starter(ctx, cwd, name, args[:len(args)-2])
	}
	triage, err := newReworkTriageTextGenerator(app, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := triage.GenerateText(context.Background(), t.TempDir(), "classify threads"); err != nil {
		t.Fatal(err)
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

func TestUnchangedReviewNotice(t *testing.T) {
	var empty bytes.Buffer
	presentUnchangedReviewRange(&empty, nil, "base", "head")
	if empty.Len() != 0 {
		t.Fatal("nil detail produced a notice")
	}
	for _, tt := range []struct {
		name, verdict, status, base, head string
		absent                            bool
		want                              bool
	}{
		{name: "comment", verdict: "comment", status: "completed", base: "base", head: "head", want: true},
		{name: "changes requested", verdict: "changes_requested", status: "completed", base: "base", head: "head", want: true},
		{name: "approve", verdict: "approve", status: "completed", base: "base", head: "head"},
		{name: "changed base", verdict: "comment", status: "completed", base: "other", head: "head"},
		{name: "changed head", verdict: "comment", status: "completed", base: "base", head: "other"},
		{name: "empty base", verdict: "comment", status: "completed", head: "head"},
		{name: "empty head", verdict: "comment", status: "completed", base: "base"},
		{name: "incomplete", verdict: "comment", base: "base", head: "head"},
		{name: "absent", absent: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			detail := &plan.PlanDetail{}
			if !tt.absent {
				detail.State.Plan.Review = &plan.PlanReview{Status: tt.status, Verdict: tt.verdict, Base: tt.base, Head: tt.head}
			}
			var out bytes.Buffer
			for _, resolved := range [][2]string{{"", "head"}, {"base", ""}, {"", ""}} {
				presentUnchangedReviewRange(&out, detail, resolved[0], resolved[1])
				if out.Len() != 0 {
					t.Fatal("empty resolved range produced a notice")
				}
			}
			presentUnchangedReviewRange(&out, detail, "base", "head")
			if got := out.String(); (got != "") != tt.want {
				t.Fatalf("notice = %q, want visible %v", got, tt.want)
			}
			if tt.want && (!strings.Contains(out.String(), "reviewed at unknown") || !strings.Contains(out.String(), "review proceeds") || !strings.Contains(out.String(), "--model")) {
				t.Fatalf("notice = %q", out.String())
			}
		})
	}
}

type noticeFailWriter struct {
	*bytes.Buffer
	attempted bool
}

func (w *noticeFailWriter) Write(p []byte) (int, error) {
	if bytes.HasPrefix(p, []byte("Notice:")) {
		w.attempted = true
		return 0, errors.New("notice unavailable")
	}
	return w.Buffer.Write(p)
}
func (w *noticeFailWriter) WriteString(s string) (int, error) { return w.Write([]byte(s)) }

func TestUnchangedReviewNoticeGuidanceAndSafeMetadata(t *testing.T) {
	for _, actionable := range []bool{false, true} {
		for _, superseded := range []bool{false, true} {
			detail := &plan.PlanDetail{}
			detail.State.Plan.ID = "plan\n\x1b[31m"
			detail.State.Status = plan.StatusReviewed
			detail.State.Plan.Review = &plan.PlanReview{Status: "completed", Verdict: "comment", Base: "base\n", Head: "head\x1b", ReviewedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.FixedZone("offset", 3600))}
			if actionable {
				detail.State.Plan.Review.Findings = []plan.ReviewFinding{{Message: "fix it"}}
			}
			if superseded {
				detail.Events = []plan.Event{{Type: plan.EventTypePlanReopened}}
			}
			var out bytes.Buffer
			presentUnchangedReviewRange(&out, detail, "base\n", "head\x1b")
			text := out.String()
			if strings.Count(text, "\n") != 1 || strings.Contains(text, "\x1b") || !strings.Contains(text, "2026-01-02T02:04:05Z") {
				t.Fatalf("unsafe notice/time: %q", text)
			}
			if strings.Contains(text, "tao rework") != (actionable && !superseded) {
				t.Fatalf("rework guidance: %q", text)
			}
			if superseded && strings.Contains(text, "--force") {
				t.Fatalf("historical merge guidance: %q", text)
			}
			if !superseded && (!strings.Contains(text, "tao merge --force") || !strings.Contains(text, "intentionally bypasses")) {
				t.Fatalf("missing force warning: %q", text)
			}
		}
	}
}
