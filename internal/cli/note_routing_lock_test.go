package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/iamseth/tao/internal/filelock"
	"github.com/iamseth/tao/internal/planning"
	"github.com/iamseth/tao/internal/runtimeconfig"
)

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
