package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/iamseth/tao/internal/agent"
	"github.com/iamseth/tao/internal/filelock"
	"github.com/iamseth/tao/internal/plannerroute"
	"github.com/iamseth/tao/internal/planning"
	"github.com/iamseth/tao/internal/runtimeconfig"
	"github.com/iamseth/tao/prompts"
)

func TestPlannerRoutingTypedAssignment(t *testing.T) {
	for _, mode := range []string{"shadow", "randomized"} {
		for _, manual := range []bool{false, true} {
			name := mode
			if manual {
				name += " manual"
			}
			t.Run(name, func(t *testing.T) {
				app, meta, id, _, _ := noteRoutingTestApp(t)
				app.RuntimeEnv = snapshotWith(map[string]string{
					runtimeconfig.EnvPlannerRouting:      mode,
					runtimeconfig.EnvPlannerRoutingArms:  "claude=0.25,pi=0.75",
					runtimeconfig.EnvPlannerRoutingFloor: "0.2",
					runtimeconfig.EnvAgent:               "invalid",
				})
				t.Setenv(runtimeconfig.EnvPlannerRouting, "invalid")
				t.Setenv(runtimeconfig.EnvPlannerRoutingArms, "invalid")
				t.Setenv(runtimeconfig.EnvPlannerRoutingFloor, "invalid")
				var args []string
				if manual {
					args = []string{"--planner-arm=pi"}
				}
				fs, _, err := app.parseArgs("note", args, app.registerNoteFlags)
				if err != nil {
					t.Fatal(err)
				}
				item, err := app.noteRepository(meta).Get(context.Background(), id)
				if err != nil {
					t.Fatal(err)
				}
				permission := agent.PermissionModeBypassPermissions
				got, err := app.resolvePlannerRouting(fs, meta, item, runtimeconfig.AgentClaude, permission)
				if err != nil {
					t.Fatal(err)
				}
				// Independently reconstruct the pre-migration policy, including enrichment
				// before versioning. Typed projection must leave the exact assignment intact.
				arms, err := plannerroute.ParseArms("claude=0.25,pi=0.75")
				if err != nil {
					t.Fatal(err)
				}
				version, err := prompts.TemplateVersion("note-slice")
				if err != nil {
					t.Fatal(err)
				}
				for i := range arms {
					arms[i].Arm.PromptVersion = version
					arms[i].Arm.PermissionMode = string(permission)
				}
				policy, err := plannerroute.NewPolicy(plannerroute.Mode(mode), arms, 0.2)
				if err != nil {
					t.Fatal(err)
				}
				eligible, err := plannerroute.Eligible(policy, []runtimeconfig.AgentKind{runtimeconfig.AgentPi, runtimeconfig.AgentClaude})
				if err != nil {
					t.Fatal(err)
				}
				var override *plannerroute.Arm
				if manual {
					selected := arms[1].Arm
					override = &selected
				}
				want, err := plannerroute.Assign(policy, eligible, plannerroute.UnitKey{RepoID: meta.ID, Kind: "note", ID: id}, override)
				if err != nil {
					t.Fatal(err)
				}
				if !got.Enabled || !reflect.DeepEqual(got.Assignment, want) {
					t.Fatalf("assignment = %+v, want %+v", got, want)
				}
				if got.Context.PromptVersion != version || got.Context.PermissionMode != string(permission) {
					t.Fatalf("missing treatment context: %+v", got.Context)
				}
			})
		}
	}
}

// A watchdog releases the lock so a regression fails rather than hanging the
// whole package. Normal callers must finish while the lock is still held.
func holdPlannerRouteLock(t *testing.T, dir string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, ".routes.lock")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600) // #nosec G304 -- test-owned ledger under t.TempDir.
	if err != nil {
		t.Fatal(err)
	}
	if err := filelock.TryLock(file); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	var once sync.Once
	release := func() {
		once.Do(func() {
			_ = filelock.Unlock(file)
			_ = file.Close()
		})
	}
	timer := time.AfterFunc(10*time.Second, release)
	t.Cleanup(func() {
		if !timer.Stop() {
			t.Error("planner routing did not finish while repository lock was held")
		}
		release()
	})
	return path
}

func TestNoteRunPlannerRoutingHeldLock(t *testing.T) {
	for _, mode := range []string{"shadow", "randomized"} {
		t.Run(mode, func(t *testing.T) {
			app, meta, id, _, errOut := noteRoutingTestApp(t)
			path := holdPlannerRouteLock(t, app.registry().PlannerRoutesDir(meta))
			calls := 0
			failure := errors.New("planner failed")
			app.PlanGenerator = planGeneratorFunc(func(_ context.Context, req planning.GeneratePlanRequest) (*planning.GeneratePlanResult, error) {
				calls++
				if req.AgentKind != runtimeconfig.AgentClaude {
					t.Errorf("shadow changed baseline runtime: %s", req.AgentKind)
				}
				return nil, failure
			})
			err := app.Run(context.Background(), []string{"note", "run", "--planner-routing", mode, id})
			if mode == "randomized" {
				if calls != 0 || err == nil || !strings.Contains(err.Error(), path) {
					t.Fatalf("calls=%d error=%v; want refusal before allocation naming lock", calls, err)
				}
			} else if calls != 1 || !errors.Is(err, failure) || !strings.Contains(errOut.String(), "warning: planner routing ledger unavailable:") || !strings.Contains(errOut.String(), path) {
				t.Fatalf("calls=%d error=%v warnings=%s; want shadow continuation", calls, err, errOut)
			}
			if _, err := os.Stat(app.registry().PlansDir(meta)); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("plan directory allocated: %v", err)
			}
			if records := noteRoutingRecords(t, app, meta); len(records) != 0 {
				t.Fatalf("contended writer persisted routes: %#v", records)
			}
		})
	}
}
