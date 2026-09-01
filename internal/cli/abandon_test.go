package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	mergepkg "github.com/iamseth/tao/internal/merge"
	"github.com/iamseth/tao/internal/plan"
	runpkg "github.com/iamseth/tao/internal/run"
	"github.com/iamseth/tao/internal/taodata"
)

func TestAbandonRecordsTrimmedReasonAndIsIdempotent(t *testing.T) {
	isolateAbandonBatchData(t)
	fixture := newRunPlanFixture(t, plan.StatusPlanned, []string{"001-a"}, nil, "001-a", plan.StatusPending)
	repo := plan.NewFileRepository(fixture.root)
	var out bytes.Buffer
	now := time.Date(2026, 9, 1, 17, 0, 0, 0, time.UTC)
	app := App{Out: &out, Err: &out, Now: func() time.Time { return now }}

	if err := app.abandon(context.Background(), repo, []string{"--reason", "  superseded by a smaller change  ", fixture.id}); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); got != "Plan abandoned: "+fixture.id+"\n" {
		t.Fatalf("output = %q", got)
	}
	detail, err := repo.ResolvePlan(context.Background(), fixture.id)
	if err != nil {
		t.Fatal(err)
	}
	if detail.State.Status != plan.StatusAbandoned {
		t.Fatalf("status = %q, want abandoned", detail.State.Status)
	}
	evidence := plan.ProjectAbandonment(detail.Events)
	if evidence == nil || evidence.Reason != "superseded by a smaller change" || !evidence.AbandonedAt.Equal(now) {
		t.Fatalf("abandonment evidence = %#v", evidence)
	}

	out.Reset()
	if err := app.abandon(context.Background(), repo, []string{fixture.id, "--reason", "a later reason"}); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); got != "Plan already abandoned: "+fixture.id+"\n" {
		t.Fatalf("idempotent output = %q", got)
	}
	detail, err = repo.ResolvePlan(context.Background(), fixture.id)
	if err != nil {
		t.Fatal(err)
	}
	evidence = plan.ProjectAbandonment(detail.Events)
	if evidence == nil || evidence.Reason != "superseded by a smaller change" {
		t.Fatalf("idempotent abandonment replaced first evidence: %#v", evidence)
	}
	count := 0
	for _, event := range detail.Events {
		if event.Type == plan.EventTypePlanAbandoned {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("plan_abandoned event count = %d, want 1", count)
	}
}

func TestAbandonValidatesArgumentsAndReasonBound(t *testing.T) {
	isolateAbandonBatchData(t)
	fixture := newRunPlanFixture(t, plan.StatusPlanned, []string{"001-a"}, nil, "001-a", plan.StatusPending)
	repo := plan.NewFileRepository(fixture.root)
	app := App{Out: &bytes.Buffer{}, Err: &bytes.Buffer{}}
	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "missing plan", args: []string{"--reason", "obsolete"}, want: abandonUsage},
		{name: "extra plan", args: []string{"--reason", "obsolete", fixture.id, "extra"}, want: abandonUsage},
		{name: "missing reason", args: []string{fixture.id}, want: "abandonment reason is required"},
		{name: "blank reason", args: []string{"--reason", "  ", fixture.id}, want: "abandonment reason is required"},
		{name: "long reason", args: []string{"--reason", strings.Repeat("x", plan.MaxAbandonmentReasonBytes+1), fixture.id}, want: "at most 1000 bytes"},
		{name: "unknown flag", args: []string{"--unknown", fixture.id}, want: "flag provided but not defined"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := app.abandon(context.Background(), repo, test.args)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want containing %q", err, test.want)
			}
		})
	}
	detail, err := repo.ResolvePlan(context.Background(), fixture.id)
	if err != nil {
		t.Fatal(err)
	}
	if detail.State.Status == plan.StatusAbandoned {
		t.Fatal("invalid command abandoned plan")
	}
}

func TestAbandonRefusesCompletedPlan(t *testing.T) {
	isolateAbandonBatchData(t)
	fixture := newRunPlanFixture(t, plan.StatusCompleted, nil, []string{"001-a"}, "001-a", plan.StatusCompleted)
	err := (App{Out: &bytes.Buffer{}, Err: &bytes.Buffer{}}).abandon(context.Background(), plan.NewFileRepository(fixture.root), []string{"--reason", "obsolete", fixture.id})
	if err == nil || !strings.Contains(err.Error(), "completed and cannot be abandoned") {
		t.Fatalf("error = %v", err)
	}
}

func TestAbandonRefusesUnsettledTransactions(t *testing.T) {
	isolateAbandonBatchData(t)
	tests := []struct {
		name   string
		mutate func(*plan.PlanDetail)
		want   string
	}{
		{
			name: "slice completion",
			mutate: func(detail *plan.PlanDetail) {
				detail.Slices.Slices[0].CommitIntent = &plan.SliceCommitIntent{Policy: "slice"}
			},
			want: "automatic completion transaction is unsettled",
		},
		{
			name: "workspace rebase",
			mutate: func(detail *plan.PlanDetail) {
				detail.State.Workspace.RebaseIntent = &plan.WorkspaceRebaseIntent{}
			},
			want: "workspace rebase transaction is unsettled",
		},
		{
			name: "merge",
			mutate: func(detail *plan.PlanDetail) {
				detail.State.Plan.MergeCommitIntent = &plan.SingleMergeCommitIntent{}
			},
			want: "merge transaction is unsettled",
		},
		{
			name: "pull request",
			mutate: func(detail *plan.PlanDetail) {
				detail.State.Plan.PullRequestIntent = &plan.PullRequest{}
			},
			want: "pull-request transaction is unsettled",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newRunPlanFixture(t, plan.StatusInProgress, []string{"001-a"}, nil, "001-a", plan.StatusInProgress)
			repo := plan.NewFileRepository(fixture.root)
			detail, err := repo.ResolvePlan(context.Background(), fixture.id)
			if err != nil {
				t.Fatal(err)
			}
			test.mutate(detail)
			writeAbandonDetail(t, detail)

			err = (App{Out: &bytes.Buffer{}, Err: &bytes.Buffer{}}).abandon(context.Background(), repo, []string{"--reason", "obsolete", fixture.id})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want containing %q", err, test.want)
			}
			reloaded, reloadErr := repo.ResolvePlan(context.Background(), fixture.id)
			if reloadErr != nil {
				t.Fatal(reloadErr)
			}
			if reloaded.State.Status == plan.StatusAbandoned {
				t.Fatal("transaction refusal abandoned plan")
			}
		})
	}
}

func TestAbandonRefusesEffectiveCandidateInUnsettledMergeBatch(t *testing.T) {
	tests := []struct {
		name   string
		status mergepkg.BatchStatus
	}{
		{name: "before landing", status: mergepkg.BatchStatusReadyToLand},
		{name: "after default movement before settlement", status: mergepkg.BatchStatusLanded},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			isolateAbandonBatchData(t)
			fixture := newRunPlanFixture(t, plan.StatusReviewed, nil, []string{"001-a"}, "001-a", plan.StatusCompleted)
			repo := plan.NewFileRepository(fixture.root)
			detail, err := repo.ResolvePlan(context.Background(), fixture.id)
			if err != nil {
				t.Fatal(err)
			}
			if detail.State.Plan.MergeCommitIntent != nil {
				t.Fatal("fixture unexpectedly has plan-local merge intent")
			}

			registry := taodata.NewRegistry("")
			registered, err := registry.RepoForRoot(detail.State.Repo.Root)
			if err != nil {
				t.Fatal(err)
			}
			store := mergepkg.NewBatchStore(registry.MergeBatchesDir(registered), registry.ActiveMergeBatchPath(registered))
			state := mergepkg.BatchState{
				Schema: mergepkg.BatchStateSchema, ID: "batch-interrupted", Status: test.status,
				RepoRoot: registered.Root, DefaultBranch: "main", DefaultStartSHA: "base",
				Candidates:  []mergepkg.BatchCandidate{{PlanID: fixture.id, PlanDir: fixture.dir}},
				ChosenOrder: []string{fixture.id},
			}
			if test.status == mergepkg.BatchStatusLanded {
				state.Landing = &mergepkg.BatchLanding{
					DefaultParentSHA: "base", IntegrationHead: "landed", ExpectedFastForward: true,
					Plans: []mergepkg.BatchLandingPlan{{PlanID: fixture.id, SquashSHA: "squash"}},
				}
				state.LandedSHA = "landed"
			}
			if _, err := store.Initialize(state, "2026-09-01T17:00:00Z"); err != nil {
				t.Fatal(err)
			}

			err = (App{Out: &bytes.Buffer{}, Err: &bytes.Buffer{}}).abandon(context.Background(), repo, []string{"--reason", "obsolete", fixture.id})
			if err == nil || !strings.Contains(err.Error(), "effective candidate in unsettled merge batch batch-interrupted") {
				t.Fatalf("error = %v, want unsettled batch refusal", err)
			}
			reloaded, reloadErr := repo.ResolvePlan(context.Background(), fixture.id)
			if reloadErr != nil {
				t.Fatal(reloadErr)
			}
			if reloaded.State.Status == plan.StatusAbandoned || plan.ProjectAbandonment(reloaded.Events) != nil {
				t.Fatal("unsettled batch refusal abandoned plan")
			}
		})
	}
}

func TestAbandonContendsWithOrdinaryPlanLock(t *testing.T) {
	isolateAbandonBatchData(t)
	fixture := newRunPlanFixture(t, plan.StatusPlanned, []string{"001-a"}, nil, "001-a", plan.StatusPending)
	repo := plan.NewFileRepository(fixture.root)
	detail, err := repo.ResolvePlan(context.Background(), fixture.id)
	if err != nil {
		t.Fatal(err)
	}
	var abandonErr error
	err = runpkg.WithPlanRunLock(context.Background(), detail, time.Now(), func(context.Context) error {
		abandonErr = (App{Out: &bytes.Buffer{}, Err: &bytes.Buffer{}}).abandon(context.Background(), repo, []string{"--reason", "obsolete", fixture.id})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if abandonErr == nil || !strings.Contains(abandonErr.Error(), "already running") {
		t.Fatalf("contention error = %v", abandonErr)
	}
}

type abandonReloadRepository struct {
	inner        *plan.FileRepository
	beforeReload func()
	calls        int
}

func (r *abandonReloadRepository) ResolvePlanRecord(ctx context.Context, input string) (*plan.PlanRecord, error) {
	r.calls++
	if r.calls == 2 && r.beforeReload != nil {
		r.beforeReload()
	}
	return r.inner.ResolvePlanRecord(ctx, input)
}

func TestAbandonReloadsAfterAcquiringLock(t *testing.T) {
	isolateAbandonBatchData(t)
	fixture := newRunPlanFixture(t, plan.StatusPlanned, []string{"001-a"}, nil, "001-a", plan.StatusPending)
	inner := plan.NewFileRepository(fixture.root)
	repo := &abandonReloadRepository{inner: inner}
	repo.beforeReload = func() {
		detail, err := inner.ResolvePlan(context.Background(), fixture.id)
		if err != nil {
			t.Fatal(err)
		}
		detail.State.Status = plan.StatusCompleted
		writeAbandonDetail(t, detail)
	}

	err := (App{Out: &bytes.Buffer{}, Err: &bytes.Buffer{}}).abandon(context.Background(), repo, []string{"--reason", "obsolete", fixture.id})
	if err == nil || !strings.Contains(err.Error(), "completed and cannot be abandoned") {
		t.Fatalf("post-lock reload error = %v", err)
	}
	if repo.calls != 2 {
		t.Fatalf("ResolvePlanRecord calls = %d, want 2", repo.calls)
	}
}

func isolateAbandonBatchData(t *testing.T) {
	t.Helper()
	t.Setenv("TAO_DATA_HOME", t.TempDir())
}

func writeAbandonDetail(t *testing.T, detail *plan.PlanDetail) {
	t.Helper()
	state, err := json.Marshal(detail.State)
	if err != nil {
		t.Fatal(err)
	}
	slices, err := json.Marshal(detail.Slices)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(detail.Dir, "state.json"), state, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(detail.Dir, "slices.json"), slices, 0o600); err != nil {
		t.Fatal(err)
	}
}
