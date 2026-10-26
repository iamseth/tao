package merge

import (
	"context"
	"errors"
	"io"
	"testing"

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

// Synthetic fixtures explicitly inject admission; production remains fail-closed.
func testRepositoryAdmission(ctx context.Context, _ repoadmission.Request, operation func(context.Context) error) error {
	return operation(ctx)
}
