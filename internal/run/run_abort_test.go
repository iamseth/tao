package run

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/iamseth/tao/internal/filelock"
	"github.com/iamseth/tao/internal/plan"
)

type abortRepository struct {
	*memoryRunRepository
	events          []plan.Event
	appendErr       error
	resolveErr      error
	resolveFromDisk bool
}

func (r *abortRepository) ResolvePlan(ctx context.Context, input string) (*plan.PlanDetail, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if r.resolveFromDisk {
		return plan.NewFileRepository(filepath.Dir(input)).ResolvePlan(ctx, input)
	}
	if r.resolveErr != nil {
		return nil, r.resolveErr
	}
	return r.memoryRunRepository.ResolvePlan(ctx, input)
}
func (r *abortRepository) AppendEvent(_ string, event plan.Event) error {
	r.events = append(r.events, event)
	return r.appendErr
}
func (r *abortRepository) OpenLogAppend(dir string) (*os.File, error) {
	return os.OpenFile(filepath.Join(dir, "agent-run.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600) // #nosec G304 -- test-owned temporary plan directory.
}

func TestServiceJournalsRunAbort(t *testing.T) {
	for _, tc := range []struct {
		name                      string
		err                       error
		kind                      string
		appendFails, resolveFails bool
	}{
		{name: "leak", err: fmt.Errorf("wrapped: %w", ControlCheckoutLeakError{ControlRoot: strings.Repeat("x", 600) + "\nignored"}), kind: plan.RunAbortKindControlCheckoutLeak},
		{name: "canceled", err: context.Canceled, kind: plan.RunAbortKindCanceled},
		{name: "deadline", err: fmt.Errorf("wrapped: %w", context.DeadlineExceeded), kind: plan.RunAbortKindCanceled},
		{name: "text is not authority", err: errors.New("context canceled control checkout leak"), kind: plan.RunAbortKindOther},
		{name: "cannot start", err: fmt.Errorf("wrapped: %w", ErrCannotStart)},
		{name: "locked", err: fmt.Errorf("wrapped: %w", plan.ErrRunLocked)},
		{name: "success"},
		{name: "append failure", err: errors.New("original"), kind: plan.RunAbortKindOther, appendFails: true},
		{name: "resolve failure", err: errors.New("original"), kind: plan.RunAbortKindOther, resolveFails: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "plan-a")
			if err := os.Mkdir(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			slice := "002"
			detail := &plan.PlanDetail{Dir: dir, State: plan.State{Plan: plan.PlanState{ID: "plan-a", CurrentSlice: &slice}}}
			detail.State.Workspace = &plan.Workspace{Path: "/workspace"}
			repo := &abortRepository{memoryRunRepository: &memoryRunRepository{details: []*plan.PlanDetail{detail}}}
			if tc.appendFails {
				repo.appendErr = errors.New("append failed")
			}
			stamp := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
			service := NewService(repo, io.Discard, Options{})
			service.dependencies.Now = func() time.Time { return stamp }
			service.dependencies.CommandRunner = func(ctx context.Context, cwd, name string, args []string, stdout, _ io.Writer) error {
				if ctx.Err() != nil || cwd != "/workspace" || name != "git" || strings.Join(args, " ") != "rev-parse HEAD" {
					t.Fatalf("unexpected head lookup: %v %s %s %v", ctx.Err(), cwd, name, args)
				}
				_, err := io.WriteString(stdout, "abc123\n")
				return err
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			got := service.WithPlanRunLock(ctx, Request{Input: dir}, func(context.Context) error {
				cancel()
				if tc.resolveFails {
					repo.resolveErr = errors.New("reload failed")
				}
				return tc.err
			})
			if got != tc.err && (!errors.Is(tc.err, plan.ErrRunLocked) || !errors.Is(got, tc.err)) { //nolint:errorlint // Assert unchanged error identity, not merely wrapping.
				t.Fatalf("returned %v, want original %v", got, tc.err)
			}
			var aborted []plan.Event
			for _, event := range repo.events {
				if event.Type == plan.EventTypeRunAborted {
					aborted = append(aborted, event)
				}
			}
			if tc.kind == "" {
				if len(aborted) != 0 {
					t.Fatalf("unexpected aborts: %+v", aborted)
				}
				if _, err := os.Stat(filepath.Join(dir, "agent-run.log")); !os.IsNotExist(err) {
					t.Fatalf("unexpected log: %v", err)
				}
				return
			}
			if len(aborted) != 1 {
				t.Fatalf("abort events: %+v", aborted)
			}
			event := aborted[0]
			if event.AbortKind != tc.kind || event.PlanID != "plan-a" || !event.Timestamp.Equal(stamp) || event.Message != plan.BoundRunAbortMessage(tc.err.Error()) {
				t.Fatalf("event: %+v", event)
			}
			if event.SliceID != slice || event.HeadSHA != "abc123" {
				t.Fatalf("event context: %+v", event)
			}
			data, err := os.ReadFile(filepath.Join(dir, "agent-run.log")) // #nosec G304 -- test-owned temporary plan directory.
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(data), "tao run exited ("+tc.kind+"): "+event.Message) || !strings.Contains(string(data), "diagnostic") {
				t.Fatalf("log: %s", data)
			}
		})
	}
}

func TestRunAbortEnrichmentBounded(t *testing.T) {
	for _, stalledGit := range []bool{false, true} {
		t.Run(fmt.Sprintf("stalled_git=%t", stalledGit), func(t *testing.T) {
			dir := t.TempDir()
			detail := &plan.PlanDetail{Dir: dir, State: plan.State{Plan: plan.PlanState{ID: filepath.Base(dir)}, Workspace: &plan.Workspace{Path: dir}}}
			repo := &abortRepository{memoryRunRepository: &memoryRunRepository{details: []*plan.PlanDetail{detail}}}
			service := NewService(repo, io.Discard, Options{})
			unblock := make(chan struct{})
			mutationLock, err := os.OpenFile(filepath.Join(dir, ".mutation.lock"), os.O_CREATE|os.O_RDWR, 0o600) // #nosec G304 -- test-owned temporary plan directory.
			if err != nil {
				t.Fatal(err)
			}
			if err := filelock.Lock(mutationLock); err != nil {
				t.Fatal(err)
			}
			runnerDone := make(chan struct{})
			service.dependencies.CommandRunner = func(ctx context.Context, _, _ string, _ []string, _, _ io.Writer) error {
				defer close(runnerDone)
				if !stalledGit {
					return nil
				}
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-unblock:
					return errors.New("test cleanup")
				}
			}
			original := errors.New("original failure")
			done := make(chan error, 1)
			finished := make(chan struct{})
			defer func() {
				_ = filelock.Unlock(mutationLock)
				_ = mutationLock.Close()
				close(unblock)
				<-finished
			}()
			go func() {
				defer close(finished)
				done <- service.WithPlanRunLock(context.Background(), Request{Input: dir}, func(context.Context) error {
					if !stalledGit {
						repo.resolveFromDisk = true
					}
					return original
				})
			}()
			select {
			case got := <-done:
				if got != original { //nolint:errorlint // Error identity must survive diagnostics.
					t.Fatalf("returned %v, want original error", got)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("diagnostic enrichment blocked original error and run lock release")
			}
			select {
			case <-runnerDone:
			default:
				t.Fatal("command still running after lock release")
			}
			lock, err := plan.AcquireRunLock(dir, detail.State.Plan.ID, time.Now())
			if err != nil {
				t.Fatalf("run lock retained: %v", err)
			}
			defer func() { _ = lock.Release() }()
			abortCount := func() int {
				count := 0
				for _, event := range repo.events {
					if event.Type == plan.EventTypeRunAborted {
						count++
						if event.HeadSHA != "" {
							t.Errorf("unexpected HEAD enrichment: %+v", event)
						}
					}
				}
				return count
			}
			if abortCount() != 1 {
				t.Fatalf("missing minimal abort event: %+v", repo.events)
			}
			before, err := os.ReadFile(plan.LogPath(dir))
			if err != nil {
				t.Fatal(err)
			}
			time.Sleep(20 * time.Millisecond)
			after, err := os.ReadFile(plan.LogPath(dir))
			if err != nil {
				t.Fatal(err)
			}
			if string(before) != string(after) || abortCount() != 1 {
				t.Fatal("journal changed after lock release")
			}
		})
	}
}
