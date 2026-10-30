package merge

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/iamseth/tao/internal/commandrunner"
	"github.com/iamseth/tao/internal/gitops"
	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/internal/repoadmission"
	"github.com/iamseth/tao/internal/testenv"
)

func TestExportedMutatorsRequireAdmission(t *testing.T) {
	refused := errors.New("admission refused")
	svc := Service{RepositoryAdmission: func(context.Context, repoadmission.Request, func(context.Context) error) error { return refused }}
	detail := &plan.PlanDetail{}
	for name, operation := range map[string]func() error{
		"cleanup":   func() error { _, err := svc.Cleanup(context.Background(), detail, Options{Force: true}); return err },
		"verify":    func() error { return svc.Verify(context.Background(), detail, "main", "head", Options{}) },
		"squash":    func() error { return svc.IntegrateSquash(context.Background(), detail) },
		"integrate": func() error { return svc.Integrate(context.Background(), detail) },
		"merge":     func() error { return svc.Merge(context.Background(), detail, Options{Force: true}) },
	} {
		t.Run(name, func(t *testing.T) {
			if err := operation(); !errors.Is(err, refused) {
				t.Fatalf("wanted admission refusal, got %v", err)
			}
		})
	}
}

// The trigger is a merge advancing the control checkout while another plan's
// execution worktree remains clean. If changed paths are outside that run's
// ownership, attribution can classify this as an unattributed checkout change;
// that diagnostic is not proof of an abort. This reproduces the actual HEAD/tree
// change in disposable repositories, not a claimed historical run_aborted event.
func TestRepositoryAdmissionPreventsCheckoutChangeDuringRun(t *testing.T) {
	testenv.SkipIfShort(t)
	for _, guarded := range []bool{false, true} {
		name := "unguarded control reproduces checkout change"
		if guarded {
			name = "admission prevents checkout change"
		}
		t.Run(name, func(t *testing.T) {
			fixture := newRealGitWorktree(t)
			commitRealPlanChange(t, fixture)
			before := realGitOutput(t, fixture.repoRoot, "rev-parse", "HEAD")
			source := realGitOutput(t, fixture.repoRoot, "rev-parse", fixture.planBranch)
			detail := mergeReadyDetail(before)
			detail.State.Repo.Root = fixture.repoRoot
			detail.State.Workspace.Branch = fixture.planBranch
			detail.State.Workspace.BaseBranch = fixture.defaultBranch
			setSingleMergeIntent(t, detail, source, before)
			svc := Service{Git: gitops.NewClient(fixture.repoRoot, nil)}
			if !guarded {
				svc.RepositoryAdmission = testRepositoryAdmission
			}
			err := repoadmission.With(context.Background(), repoadmission.Request{Root: fixture.worktreePath, Mode: repoadmission.Run}, func(context.Context) error {
				err := svc.IntegrateSquash(context.Background(), detail)
				var busy *repoadmission.ContentionError
				if guarded {
					if !errors.As(err, &busy) {
						t.Fatalf("expected contention, got %v", err)
					}
				} else if err != nil {
					t.Fatal(err)
				}
				after := realGitOutput(t, fixture.repoRoot, "rev-parse", "HEAD")
				if (after == before) != guarded {
					t.Fatalf("guarded=%t: HEAD before=%s after=%s", guarded, before, after)
				}
				if status := realGitOutput(t, fixture.worktreePath, "status", "--porcelain"); status != "" {
					t.Fatalf("execution worktree unexpectedly dirty: %s", status)
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestMergeAdmissionHeldThroughVerificationAndReleasedOnError(t *testing.T) {
	testenv.SkipIfShort(t)
	fixture := newRealGitWorktree(t)
	detail := mergeVerifyDetail()
	detail.State.Repo.Root = fixture.repoRoot
	failure := errors.New("verification failed")
	called := false
	svc := Service{Git: mergeVerifyGit(), Cleaner: successfulCleanup(), Events: &fakeEventAppender{}}
	svc.RepositoryAdmission = func(ctx context.Context, req repoadmission.Request, operation func(context.Context) error) error {
		return repoadmission.With(ctx, req, func(ctx context.Context) error {
			return repoadmission.With(ctx, req, operation)
		})
	}
	svc.Runner = func(_ context.Context, _, _ string, _ []string, _, _ io.Writer) error {
		called = true
		err := repoadmission.With(context.Background(), repoadmission.Request{Root: fixture.worktreePath, Mode: repoadmission.Run}, func(context.Context) error { t.Fatal("run entered during merge verification"); return nil })
		var busy *repoadmission.ContentionError
		if !errors.As(err, &busy) {
			t.Fatalf("run admission: %v", err)
		}
		return failure
	}
	if err := svc.Merge(context.Background(), detail, Options{VerifyCommand: "test gate"}); !errors.Is(err, failure) || !called {
		t.Fatalf("merge: called=%t error=%v", called, err)
	}
	if err := repoadmission.With(context.Background(), repoadmission.Request{Root: fixture.repoRoot, Mode: repoadmission.Run}, func(context.Context) error { return nil }); err != nil {
		t.Fatalf("merge leaked admission: %v", err)
	}
}

func TestRecordOnlyExemptsExclusiveAdmission(t *testing.T) {
	svc := Service{Git: mergeVerifyGit(), Cleaner: successfulCleanup(), Events: &fakeEventAppender{}, RepositoryAdmission: func(context.Context, repoadmission.Request, func(context.Context) error) error {
		t.Fatal("record-only requested exclusive admission")
		return nil
	}}
	// Ordinary eligibility still applies; exemption is not lifecycle authority.
	_ = svc.Merge(context.Background(), mergeVerifyDetail(), Options{RecordOnly: true})
}

func newTestService(service Service) Service {
	if service.RepositoryAdmission == nil {
		service.RepositoryAdmission = testRepositoryAdmission
	}
	return service
}

func newTestBatchCoordinator(seams BatchCoordinatorSeams) *BatchCoordinator {
	if seams.RepositoryAdmission == nil {
		seams.RepositoryAdmission = testRepositoryAdmission
	}
	return NewBatchCoordinator(seams)
}

func testBatchWorkspaceDefaults(owner *BatchWorkspace) *BatchWorkspace {
	if owner.RepositoryAdmission == nil {
		owner.RepositoryAdmission = testRepositoryAdmission
	}
	return owner
}

func newTestBatchWorkspace(root, batches string, runner commandrunner.Runner) (*BatchWorkspace, error) {
	owner, err := NewBatchWorkspace(root, batches, runner)
	if err != nil {
		return nil, err
	}
	return testBatchWorkspaceDefaults(owner), nil
}

func TestOrdinaryMergeFixtureGuardsPreserved(t *testing.T) {
	type contextKey struct{}
	ctx := context.WithValue(context.Background(), contextKey{}, "caller")
	failure := errors.New("operation failed")
	request := repoadmission.Request{Root: "synthetic-only", Mode: repoadmission.Merge}
	for _, deny := range []bool{false, true} {
		calls := 0
		guard := func(got context.Context, req repoadmission.Request, operation func(context.Context) error) error {
			calls++
			if got != ctx || req.Root != request.Root || req.Mode != request.Mode {
				t.Fatal("guard lost context or request")
			}
			if deny {
				return failure
			}
			return operation(context.WithValue(got, contextKey{}, "guard"))
		}
		git := mergeVerifyGit()
		service := newTestService(Service{RepositoryAdmission: guard, Git: git})
		owner := &BatchWorkspace{RepositoryAdmission: guard, repoRoot: request.Root}
		if testBatchWorkspaceDefaults(owner) != owner || owner.repoRoot != request.Root || service.Git != git {
			t.Fatal("defaults changed unrelated fields")
		}
		coordinator := newTestBatchCoordinator(BatchCoordinatorSeams{RepositoryAdmission: guard, Workspace: owner, Planner: service})
		if coordinator.seams.Workspace != owner {
			t.Fatal("workspace replaced")
		}
		for _, kept := range []repoadmission.Guard{service.RepositoryAdmission, coordinator.seams.RepositoryAdmission, owner.RepositoryAdmission} {
			called := false
			err := kept(ctx, request, func(got context.Context) error {
				called = true
				if got.Value(contextKey{}) != "guard" {
					t.Fatal("callback context lost")
				}
				return failure
			})
			if !errors.Is(err, failure) || called == deny {
				t.Fatalf("deny=%t callback=%t error=%v", deny, called, err)
			}
		}
		if calls != 3 {
			t.Fatalf("custom guard calls: %d", calls)
		}
	}
	// An ordinary outer coordinator must not overwrite a nested owner's denial.
	owner := testBatchWorkspaceDefaults(&BatchWorkspace{RepositoryAdmission: func(context.Context, repoadmission.Request, func(context.Context) error) error { return failure }})
	coordinator := newTestBatchCoordinator(BatchCoordinatorSeams{Workspace: owner})
	err := coordinator.seams.RepositoryAdmission(ctx, request, func(ctx context.Context) error {
		_, err := owner.Restart(ctx, BatchState{})
		return err
	})
	if !errors.Is(err, failure) {
		t.Fatalf("nested workspace denial: %v", err)
	}
	// The ordinary guard preserves callback context and errors without Git.
	for _, guard := range []repoadmission.Guard{newTestService(Service{}).RepositoryAdmission, coordinator.seams.RepositoryAdmission, testBatchWorkspaceDefaults(&BatchWorkspace{}).RepositoryAdmission} {
		if err := guard(ctx, request, func(got context.Context) error {
			if got != ctx {
				t.Fatal("ordinary guard changed context")
			}
			return failure
		}); !errors.Is(err, failure) {
			t.Fatalf("callback error: %v", err)
		}
	}
}

func TestOrdinaryMergeAdmissionDefaults(t *testing.T) {
	// Missing defaults fail deterministically without resolving any repository.
	if newTestService(Service{}).RepositoryAdmission == nil {
		t.Fatal("ordinary service missing admission default")
	}
	if newTestBatchCoordinator(BatchCoordinatorSeams{}).seams.RepositoryAdmission == nil {
		t.Fatal("ordinary coordinator missing admission default")
	}
	if testBatchWorkspaceDefaults(&BatchWorkspace{}).RepositoryAdmission == nil {
		t.Fatal("ordinary workspace missing admission default")
	}
}

func TestOrdinaryMergeFixturesUnderRepositoryLease(t *testing.T) {
	testenv.SkipIfShort(t)
	fixture := newRealGitWorktree(t)
	for _, mode := range []repoadmission.Mode{repoadmission.Run, repoadmission.Merge} {
		t.Run(string(mode), func(t *testing.T) {
			err := repoadmission.With(context.Background(), repoadmission.Request{Root: fixture.repoRoot, Mode: mode}, func(context.Context) error {
				detail := mergeVerifyDetail()
				detail.State.Repo.Root = fixture.repoRoot
				failure := errors.New("intended verification failure")
				service := Service{Git: mergeVerifyGit(), Runner: func(context.Context, string, string, []string, io.Writer, io.Writer) error { return failure }}
				var busy *repoadmission.ContentionError
				// Independent contender contexts must not inherit the outer lease.
				if err := service.Verify(context.Background(), detail, "main", "head", Options{VerifyCommand: "test gate"}); !errors.As(err, &busy) {
					t.Fatalf("production admission: %v", err)
				}
				if err := newTestService(service).Verify(context.Background(), detail, "main", "head", Options{VerifyCommand: "test gate"}); !errors.Is(err, failure) {
					t.Fatalf("ordinary verification: %v", err)
				}
				owner, err := newTestBatchWorkspace(fixture.repoRoot, filepath.Join(t.TempDir(), "batches"), nil)
				if err != nil {
					return err
				}
				ownership, err := owner.AcquireOwnership(BatchState{}, time.Now())
				if err != nil {
					return err
				}
				defer func() { _ = ownership.Release() }()
				productionOwner := *owner
				productionOwner.RepositoryAdmission = nil
				if _, err := productionOwner.Restart(context.Background(), BatchState{}); !errors.As(err, &busy) {
					t.Fatalf("production workspace: %v", err)
				}
				if _, err := owner.Restart(context.Background(), BatchState{}); err == nil || !strings.Contains(err.Error(), "merge batch ownership is held") {
					t.Fatalf("ordinary workspace did not reach batch ownership: %v", err)
				}
				seams := BatchCoordinatorSeams{Workspace: owner}
				if _, err := NewBatchCoordinator(seams).Run(context.Background(), BatchCoordinatorOptions{}); !errors.As(err, &busy) {
					t.Fatalf("production coordinator: %v", err)
				}
				if _, err := newTestBatchCoordinator(seams).Run(context.Background(), BatchCoordinatorOptions{}); err == nil || err.Error() != "batch coordinator store is required" {
					t.Fatalf("ordinary coordinator: %v", err)
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

// Synthetic fixtures explicitly inject admission; production remains fail-closed.
func testRepositoryAdmission(ctx context.Context, _ repoadmission.Request, operation func(context.Context) error) error {
	return operation(ctx)
}
