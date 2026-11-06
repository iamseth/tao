package merge

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/iamseth/tao/internal/gitops"
	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/internal/repoadmission"
	"github.com/iamseth/tao/internal/testenv"
)

type singleEntryResolver func(context.Context, string) (*plan.PlanDetail, error)

func (f singleEntryResolver) ResolvePlan(ctx context.Context, input string) (*plan.PlanDetail, error) {
	return f(ctx, input)
}

type singleEntryProbe func(context.Context, BatchState) (bool, error)

func (f singleEntryProbe) DefaultReachedLandingIntent(ctx context.Context, state BatchState) (bool, error) {
	return f(ctx, state)
}

func TestSingleOwnedPlanOrder(t *testing.T) {
	for _, outcome := range []string{"success", "callback error", "abandoned", "cancel", "locked", "admission refused", "factory error"} {
		t.Run(outcome, func(t *testing.T) {
			initial := mergeVerifyDetail()
			initial.Dir = t.TempDir()
			refreshed := *initial
			if outcome == "abandoned" {
				refreshed.State.Status = plan.StatusAbandoned
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			failure := errors.New("sentinel")
			var order []string
			resolver := singleEntryResolver(func(_ context.Context, input string) (*plan.PlanDetail, error) {
				order = append(order, "resolve:"+input)
				if input == "selector" {
					return initial, nil
				}
				if input != initial.Dir {
					t.Fatalf("reload %q", input)
				}
				lock, err := plan.AcquireRunLock(initial.Dir, initial.State.Plan.ID, time.Now())
				if err == nil {
					_ = lock.Release()
					t.Fatal("reload outside lock")
				}
				if !errors.Is(err, plan.ErrRunLocked) {
					t.Fatal(err)
				}
				return &refreshed, nil
			})
			if outcome == "locked" {
				lock, err := plan.AcquireRunLock(initial.Dir, initial.State.Plan.ID, time.Now())
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = lock.Release() }()
			}
			root := t.TempDir()
			options := SingleOwnedPlanOptions{StartedAt: time.Now(), RepositoryAdmission: func(ctx context.Context, req repoadmission.Request, next func(context.Context) error) error {
				order = append(order, "admission")
				if req.Root != initial.State.Repo.Root || req.Mode != repoadmission.Merge {
					t.Fatalf("request %+v", req)
				}
				if outcome == "admission refused" {
					return failure
				}
				defer func() { order = append(order, "release") }()
				return next(ctx)
			}, BatchDependencies: func(_ context.Context, got string) (*BatchStore, LandingIntentProbe, error) {
				order = append(order, "batch")
				if got != initial.State.Repo.Root {
					t.Fatalf("root %q", got)
				}
				if outcome == "cancel" {
					cancel()
				}
				if outcome == "factory error" {
					return nil, nil, failure
				}
				store := NewBatchStore(root, filepath.Join(root, "active.json"))
				if err := store.SetActive("batch-1"); err != nil {
					t.Fatal(err)
				}
				if err := store.SaveSnapshot(BatchState{Schema: BatchStateSchema, ID: "batch-1", Status: BatchStatusPlanned, Landing: &BatchLanding{}}); err != nil {
					t.Fatal(err)
				}
				return store, singleEntryProbe(func(context.Context, BatchState) (bool, error) {
					order = append(order, "guard")
					if outcome != "locked" {
						lock, err := plan.AcquireRunLock(initial.Dir, initial.State.Plan.ID, time.Now())
						if err != nil {
							t.Fatalf("guard ran under lock: %v", err)
						}
						_ = lock.Release()
					}
					return true, nil
				}), nil
			}}
			err := WithSingleOwnedPlan(ctx, resolver, "selector", options, func(_ context.Context, got *plan.PlanDetail) error {
				order = append(order, "callback")
				if got != &refreshed {
					t.Fatal("stale snapshot")
				}
				if outcome == "callback error" {
					return failure
				}
				return nil
			})
			want := []string{"resolve:selector", "admission"}
			if outcome != "admission refused" {
				want = append(want, "batch")
				if outcome != "factory error" {
					want = append(want, "guard")
				}
				if outcome != "cancel" && outcome != "locked" && outcome != "factory error" {
					want = append(want, "resolve:"+initial.Dir)
					if outcome != "abandoned" {
						want = append(want, "callback")
					}
				}
				want = append(want, "release")
			}
			if !reflect.DeepEqual(order, want) {
				t.Fatalf("order %v want %v", order, want)
			}
			if (err != nil) != (outcome != "success") {
				t.Fatalf("error %v", err)
			}
			if outcome == "cancel" && !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
			if outcome == "locked" && !errors.Is(err, plan.ErrRunLocked) {
				t.Fatal(err)
			}
			if strings.Contains(outcome, "error") || outcome == "admission refused" {
				if !errors.Is(err, failure) {
					t.Fatal(err)
				}
			}
			if outcome != "locked" {
				lock, err := plan.AcquireRunLock(initial.Dir, initial.State.Plan.ID, time.Now())
				if err != nil {
					t.Fatal(err)
				}
				_ = lock.Release()
			}
		})
	}
}

func TestSingleOwnedPlanBatchPolicy(t *testing.T) {
	for _, name := range []string{"ordinary", "record candidate", "record ejected", "record noncandidate", "record restart", "landed", "corrupt", "missing state", "nil factory", "nil store", "nil probe"} {
		t.Run(name, func(t *testing.T) {
			detail := mergeVerifyDetail()
			detail.Dir = t.TempDir()
			root := t.TempDir()
			active := filepath.Join(root, "active.json")
			store := NewBatchStore(root, active)
			state := BatchState{Schema: BatchStateSchema, ID: "batch-1", Status: BatchStatusPlanned}
			if name == "record candidate" || name == "record ejected" {
				state.Candidates = []BatchCandidate{{PlanID: detail.State.Plan.ID}}
			}
			if name == "record ejected" {
				state.Candidates[0].Deferred = &BatchDeferral{PlanID: detail.State.Plan.ID}
				state.Ejection = &BatchEjection{PlanID: detail.State.Plan.ID, Status: batchEjectionPending}
			}
			if name == "landed" {
				state.Landing = &BatchLanding{}
				state.LandedSHA = "landed"
			}
			if err := store.SetActive(state.ID); err != nil {
				t.Fatal(err)
			}
			if name != "missing state" {
				if err := store.SaveSnapshot(state); err != nil {
					t.Fatal(err)
				}
			}
			if name == "corrupt" {
				if err := os.WriteFile(active, []byte("{"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			admitted, called, resolved := false, false, 0
			options := SingleOwnedPlanOptions{RecordOnly: strings.HasPrefix(name, "record"), Restart: name == "record restart", RepositoryAdmission: func(ctx context.Context, _ repoadmission.Request, next func(context.Context) error) error {
				admitted = true
				return next(ctx)
			}, BatchDependencies: func(context.Context, string) (*BatchStore, LandingIntentProbe, error) {
				if name == "nil store" {
					return nil, &singleMergeLandingProbe{}, nil
				}
				if name == "nil probe" {
					return store, nil, nil
				}
				return store, &singleMergeLandingProbe{}, nil
			}}
			if name == "nil factory" {
				options.BatchDependencies = nil
			}
			err := WithSingleOwnedPlan(context.Background(), singleEntryResolver(func(context.Context, string) (*plan.PlanDetail, error) { resolved++; return detail, nil }), "selector", options, func(context.Context, *plan.PlanDetail) error { called = true; return nil })
			allowed := name == "record noncandidate" || name == "landed"
			if (err == nil) != allowed || called != allowed {
				t.Fatalf("called=%t error=%v", called, err)
			}
			if name != "nil factory" && admitted != (!options.RecordOnly || options.Restart) {
				t.Fatalf("admitted=%t", admitted)
			}
			if !allowed && resolved > 1 {
				t.Fatal("refusal reached reload")
			}
		})
	}
}

func TestSingleOwnedPlanNestedRestart(t *testing.T) {
	testenv.SkipIfShort(t)
	fixture := newRealGitWorktree(t)
	parent := realGitOutput(t, fixture.repoRoot, "rev-parse", fixture.defaultBranch)
	runRealGit(t, fixture.worktreePath, "commit", "--allow-empty", "-m", "source work")
	source := realGitOutput(t, fixture.repoRoot, "rev-parse", fixture.planBranch)
	runRealGit(t, fixture.repoRoot, "commit", "--allow-empty", "-m", "advance default")
	dir, _ := persistSingleMergeRestartPlan(t, fixture, parent, source)
	storeRoot := t.TempDir()
	options := SingleOwnedPlanOptions{RecordOnly: true, Restart: true, BatchDependencies: func(context.Context, string) (*BatchStore, LandingIntentProbe, error) {
		return NewBatchStore(storeRoot, filepath.Join(storeRoot, "active.json")), &singleMergeLandingProbe{}, nil
	}}
	service := Service{Git: gitops.NewClient(fixture.repoRoot, nil), NewGit: func(dir string) GitClient { return gitops.NewClient(dir, nil) }}
	err := WithSingleOwnedPlan(context.Background(), plan.NewFileRepository(filepath.Dir(dir)), dir, options, func(ctx context.Context, detail *plan.PlanDetail) error {
		// A background reader must contend even with record-only on a restart.
		err := repoadmission.With(context.Background(), repoadmission.Request{Root: fixture.repoRoot, Mode: repoadmission.Run}, func(context.Context) error { t.Fatal("restart admission exempted"); return nil })
		var busy *repoadmission.ContentionError
		if !errors.As(err, &busy) {
			t.Fatalf("admission: %v", err)
		}
		result, err := service.RestartSingleMerge(ctx, detail.Dir)
		if err == nil && result.Discarded == nil {
			t.Fatal("restart did not discard stale intent")
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestSingleOwnedPlanProductionAdmissionAndNestedService(t *testing.T) {
	testenv.SkipIfShort(t)
	fixture := newRealGitWorktree(t)
	detail := mergeVerifyDetail()
	detail.Dir = t.TempDir()
	detail.State.Repo.Root = fixture.repoRoot
	root := t.TempDir()
	factoryCalls := 0
	options := SingleOwnedPlanOptions{BatchDependencies: func(context.Context, string) (*BatchStore, LandingIntentProbe, error) {
		factoryCalls++
		return NewBatchStore(root, filepath.Join(root, "active.json")), &singleMergeLandingProbe{}, nil
	}}
	resolver := singleEntryResolver(func(context.Context, string) (*plan.PlanDetail, error) { return detail, nil })
	err := repoadmission.With(context.Background(), repoadmission.Request{Root: fixture.repoRoot, Mode: repoadmission.Run}, func(context.Context) error {
		err := WithSingleOwnedPlan(context.Background(), resolver, "selector", options, func(context.Context, *plan.PlanDetail) error { t.Fatal("entered under contention"); return nil })
		var busy *repoadmission.ContentionError
		if !errors.As(err, &busy) || factoryCalls != 0 {
			t.Fatalf("factory=%d error=%v", factoryCalls, err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	failure := errors.New("verification failure")
	svc := Service{Git: mergeVerifyGit(), Cleaner: successfulCleanup(), Events: &fakeEventAppender{}}
	// Production defaults on both entry and service must reuse the same lease.
	svc.Runner = func(context.Context, string, string, []string, io.Writer, io.Writer) error { return failure }
	err = WithSingleOwnedPlan(context.Background(), resolver, "selector", options, func(ctx context.Context, got *plan.PlanDetail) error {
		return svc.Merge(ctx, got, Options{Force: true, VerifyCommand: "gate"})
	})
	if !errors.Is(err, failure) {
		t.Fatalf("nested merge: %v", err)
	}
	if err := repoadmission.With(context.Background(), repoadmission.Request{Root: fixture.repoRoot, Mode: repoadmission.Run}, func(context.Context) error { return nil }); err != nil {
		t.Fatalf("lease leaked: %v", err)
	}
}
