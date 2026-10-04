package cli

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/iamseth/tao/internal/herdr"
	"github.com/iamseth/tao/internal/monitor"
	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/internal/runtimeconfig"
	"github.com/iamseth/tao/internal/taodata"
)

func planFixFixture(t *testing.T) (App, monitor.Row) {
	t.Helper()
	app, repo, _ := planningFixture(t)
	app.RuntimeEnv = snapshotWith(map[string]string{runtimeconfig.EnvAgent: "pi"})
	id := "20261004-015325-fix"
	dir := writeRunPlan(t, app.registry().PlansDir(repo), id, plan.StatusBlocked, []string{"001-a"}, nil, "001-a", plan.StatusBlocked)
	return app, monitor.Row{PlanID: id, PlanDir: dir, RepositoryID: repo.ID, RepositoryRoot: repo.Root}
}

func TestUIPlanFixCommand(t *testing.T) {
	for _, agent := range []string{"pi", "claude"} {
		for _, model := range []string{"", "captured-model"} {
			t.Run(agent+model, func(t *testing.T) {
				t.Setenv("HERDR", "1")
				t.Setenv("HERDR_PANE_ID", "private")
				app, row := planFixFixture(t)
				values := map[string]string{runtimeconfig.EnvAgent: agent}
				if model != "" {
					values[runtimeconfig.EnvModel] = model
				}
				app.RuntimeEnv = snapshotWith(values)
				launcher := newUIPlanFixLauncher(app, app.In, app.Out)
				calls := 0
				launcher.run = func(cmd *exec.Cmd) error {
					calls++
					args := []string{agent}
					if model != "" {
						args = append(args, "--model", model)
					}
					args = append(args, "/tao-fix-plan "+row.PlanID)
					if !reflect.DeepEqual(cmd.Args, args) || cmd.Dir != row.RepositoryRoot {
						t.Fatalf("command = %+v", cmd)
					}
					if cmd.Stdin != app.In || cmd.Stdout != app.Out || cmd.Stderr != app.Err {
						t.Fatal("streams not attached")
					}
					if !reflect.DeepEqual(cmd.Env, herdr.StripInjectedEnv(os.Environ())) {
						t.Fatal("environment mismatch")
					}
					return nil
				}
				if err := launcher.Launch(context.Background(), row); err != nil {
					t.Fatal(err)
				}
				if calls != 1 {
					t.Fatalf("calls = %d", calls)
				}
			})
		}
	}
}

func TestUIPlanFixRefusals(t *testing.T) {
	for _, damage := range []string{"empty-plan", "empty-repo", "empty-root", "root", "identity", "health", "missing-plan", "prefix", "path", "live-lock", "cancelled"} {
		t.Run(damage, func(t *testing.T) {
			app, row := planFixFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			want := ""
			switch damage {
			case "empty-plan":
				row.PlanID = ""
			case "empty-repo":
				row.RepositoryID = ""
			case "empty-root":
				row.RepositoryRoot = ""
			case "root":
				row.RepositoryRoot = "/stale"
				want = "plan repository identity or root changed"
			case "identity":
				app.registry().(*fakeNoteRegistry).repos[0].ID = "wrong"
			case "health":
				app.RepoHealthCheck = func(context.Context, taodata.Repo) taodata.RepoHealth {
					return taodata.RepoHealth{Error: true, Status: taodata.RepoHealthNotGitRepo}
				}
			case "missing-plan":
				row.PlanID = "missing"
			case "prefix":
				row.PlanID = row.PlanID[:8]
				want = "plan identity does not match selection"
			case "path":
				row.PlanID = row.PlanDir
			case "live-lock":
				lock, err := plan.AcquireRunLock(row.PlanDir, row.PlanID, time.Now())
				if err != nil {
					t.Fatal(err)
				}
				defer func() {
					if err := lock.Release(); err != nil {
						t.Error(err)
					}
				}()
				want = "plan has a live run"
			case "cancelled":
				cancel()
			}
			launcher := newUIPlanFixLauncher(app, app.In, app.Out)
			launcher.run = func(*exec.Cmd) error { t.Fatal("refused selection executed"); return nil }
			err := launcher.Launch(ctx, row)
			if err == nil || (want != "" && !strings.Contains(err.Error(), want)) {
				t.Fatalf("error = %v, want %q", err, want)
			}
		})
	}
}

type planFixLaunchFunc func(context.Context, monitor.Row) error

func (f planFixLaunchFunc) Launch(ctx context.Context, row monitor.Row) error { return f(ctx, row) }

func TestUIPlanFixSignalWiring(t *testing.T) {
	app, row := planFixFixture(t)
	scope := newUIPlanningSignalsWith(context.Background(), func(chan<- os.Signal, ...os.Signal) {}, func(chan<- os.Signal) {})
	defer scope.Close()
	composed := app.uiPlanFixLauncher(app.In, scope).(scopedPlanFixLauncher)
	if adapter, ok := composed.launcher.(*uiPlanFixLauncher); !ok || adapter.input != app.In || adapter.output != app.Out {
		t.Fatal("default launcher not wired")
	}
	failure := errors.New("foreground failure")
	calls := 0
	app.UIPlanFixLauncher = planFixLaunchFunc(func(ctx context.Context, got monitor.Row) error {
		calls++
		if got.PlanID != row.PlanID || ctx.Err() != nil {
			t.Fatal("selection or context changed")
		}
		return failure
	})
	if err := app.uiPlanFixLauncher(app.In, scope).Launch(context.Background(), row); !errors.Is(err, failure) || calls != 1 {
		t.Fatalf("injected launch: calls=%d error=%v", calls, err)
	}
	if scope.Context().Err() != nil {
		t.Fatal("foreground failure cancelled dashboard")
	}
	scope.cancel()
	<-scope.done
	if err := app.uiPlanFixLauncher(app.In, scope).Launch(context.Background(), row); !errors.Is(err, context.Canceled) || calls != 1 {
		t.Fatalf("shutdown launch: calls=%d error=%v", calls, err)
	}
}

func TestUIPlanFixProcessError(t *testing.T) {
	app, row := planFixFixture(t)
	launcher := newUIPlanFixLauncher(app, app.In, app.Out)
	failure := errors.New("start failed")
	launcher.run = func(*exec.Cmd) error { return failure }
	if err := launcher.Launch(context.Background(), row); !errors.Is(err, failure) || !strings.Contains(err.Error(), "foreground plan fix") {
		t.Fatalf("error = %v", err)
	}
}
