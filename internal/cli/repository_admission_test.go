package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/internal/planning"
	"github.com/iamseth/tao/internal/repoadmission"
	"github.com/iamseth/tao/internal/run"
	"github.com/iamseth/tao/internal/runstatus"
	"github.com/iamseth/tao/internal/taodata"
	"github.com/iamseth/tao/internal/testenv"
)

func TestRepositoryObservationGuardSelectedDirectory(t *testing.T) {
	testenv.SkipIfShort(t)
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "init", root).CombinedOutput(); err != nil { // #nosec G204 -- test-owned temporary repository.
		t.Fatalf("init: %s: %v", out, err)
	}
	t.Setenv("TAO_DATA_HOME", t.TempDir())
	selected := t.TempDir()
	dir := filepath.Join(selected, "sibling")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "state.json"), []byte(fmt.Sprintf(`{"plan":{"id":"sibling"},"repo":{"root":%q}}`, root)), 0600); err != nil {
		t.Fatal(err)
	}
	app := App{Err: io.Discard}
	guard := app.repositoryObservationGuard(plan.NewFileRepository(selected))
	registry := taodata.NewRegistry("")
	repo := taodata.Repo{ID: taodata.RepoID(root), Root: root}
	store := runstatus.NewStore(registry.RuntimeStatusDir(repo), nil)
	now := time.Now()
	if err := store.Write(runstatus.Record{Schema: runstatus.Schema, RepoID: repo.ID, PlanID: "sibling", InvocationID: fmt.Sprintf("%d-1", os.Getppid()), Phase: runstatus.PhaseMergeVerifying, InvocationStartedAt: now, HeartbeatAt: now}); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []repoadmission.Mode{repoadmission.Run, repoadmission.Merge} {
		err := guard(context.Background(), repoadmission.Request{Root: root, Mode: mode}, func(context.Context) error { t.Fatal("entered with fresh sibling merge status"); return nil })
		var busy *repoadmission.ContentionError
		if !errors.As(err, &busy) || !strings.Contains(err.Error(), "merge_verifying") {
			t.Fatalf("error=%v", err)
		}
	}
	path, err := store.Path("sibling")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := guard(context.Background(), repoadmission.Request{Root: root, Mode: repoadmission.Run}, func(context.Context) error { return nil }); err != nil {
		t.Fatalf("malformed status blocked run: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "events.jsonl")); !os.IsNotExist(err) {
		t.Fatalf("refusal events: %v", err)
	}
}

func TestRepositoryAdmissionRefusesCLIEntries(t *testing.T) {
	testenv.SkipIfShort(t)
	root := t.TempDir()
	if out, err := exec.Command("git", "init", root).CombinedOutput(); err != nil { // #nosec G204 -- test-owned temporary repository.
		t.Fatalf("init: %s: %v", out, err)
	}
	for _, command := range []string{"run", "rework", "merge", "merge-force", "merge-restart", "merge-no-squash", "merge-all", "merge-all-restart"} {
		t.Run(command, func(t *testing.T) {
			plans := t.TempDir()
			const id = "20260628-1200-admission"
			dir := writeCLIReworkPlan(t, plans, id, plan.StatusCompleted, reworkReview(plan.ReviewVerdictChangesRequested, []plan.ReviewFinding{{File: "file.go", Message: "fix"}}))
			repo := plan.NewFileRepository(plans)
			detail, err := repo.ResolvePlan(context.Background(), id)
			if err != nil {
				t.Fatal(err)
			}
			detail.State.Repo.Root = root
			record, err := plan.NewPlanRecord(dir, detail)
			if err != nil {
				t.Fatal(err)
			}
			if err := record.PersistArtifacts(); err != nil {
				t.Fatal(err)
			}
			before := readReworkArtifacts(t, dir)
			app := App{Out: io.Discard, Err: io.Discard, RuntimeEnv: snapshotWith(nil)}
			registry := logBatchRegistry{&fakeNoteRegistry{dir: t.TempDir(), current: taodata.Repo{ID: "repo", Root: root}}}
			app.Registry = func() NoteRegistry { return registry }
			mode := repoadmission.Merge
			if strings.HasPrefix(command, "merge") {
				mode = repoadmission.Run
			}
			err = repoadmission.With(context.Background(), repoadmission.Request{Root: root, Mode: mode}, func(context.Context) error {
				var err error
				switch command {
				case "run":
					err = app.run(context.Background(), repo, []string{id})
				case "rework":
					err = app.rework(context.Background(), repo, []string{"--run", id})
				default:
					args := []string{id}
					switch command {
					case "merge-all":
						args = []string{"--all"}
					case "merge-all-restart":
						args = []string{"--all", "--restart"}
					case "merge":
					default:
						args = append([]string{"--" + strings.TrimPrefix(command, "merge-")}, args...)
					}
					err = app.merge(context.Background(), repo, args)
				}
				var busy *repoadmission.ContentionError
				if !errors.As(err, &busy) {
					t.Fatalf("refusal: %v", err)
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if after := readReworkArtifacts(t, dir); after != before {
				t.Fatal("admission refusal mutated plan artifacts/events")
			}
		})
	}
}

func TestRepositoryAdmissionRefusesPromotedExecution(t *testing.T) {
	testenv.SkipIfShort(t)
	clearTaoEnv(t)
	root := t.TempDir()
	if out, err := exec.Command("git", "init", root).CombinedOutput(); err != nil { // #nosec G204 -- test-owned temporary repository.
		t.Fatalf("init: %s: %v", out, err)
	}
	app, out, _ := noteTestApp(t, strings.NewReader(""), taodata.Repo{ID: "tao-123", Name: "tao", Root: root, Branch: "main"})
	app.RepositoryAdmission = nil // Exercise production coordination, not the fixture guard.
	fixture := newRunPlanFixture(t, plan.StatusPlanned, []string{"001-a"}, nil, "001-a", plan.StatusPending)
	repo := plan.NewFileRepository(filepath.Dir(fixture.dir))
	detail, err := repo.ResolvePlan(context.Background(), fixture.id)
	if err != nil {
		t.Fatal(err)
	}
	detail.State.Repo.Root = root
	record, err := plan.NewPlanRecord(fixture.dir, detail)
	if err != nil {
		t.Fatal(err)
	}
	if err := record.PersistArtifacts(); err != nil {
		t.Fatal(err)
	}
	app.Repository = func(string) Repository { return repo }
	app.PlanGenerator = planGeneratorFunc(func(context.Context, planning.GeneratePlanRequest) (*planning.GeneratePlanResult, error) {
		return &planning.GeneratePlanResult{Allocation: planning.PlanAllocation{ID: fixture.id, Dir: fixture.dir}, Validation: planning.ValidationResult{OK: true}}, nil
	})
	if err := app.Run(context.Background(), []string{"note", "create", "implement a fix"}); err != nil {
		t.Fatal(err)
	}
	id := strings.TrimSpace(strings.TrimPrefix(out.String(), "Created note "))
	before := readReworkArtifacts(t, fixture.dir)
	err = repoadmission.With(context.Background(), repoadmission.Request{Root: root, Mode: repoadmission.Merge}, func(context.Context) error {
		err := app.Run(context.Background(), []string{"note", "run", id})
		var busy *repoadmission.ContentionError
		if !errors.Is(err, run.ErrCannotStart) || !errors.As(err, &busy) {
			t.Fatalf("promoted execution: %v", err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if after := readReworkArtifacts(t, fixture.dir); after != before {
		t.Fatal("refusal mutated plan artifacts/events")
	}
}

// Both processes are ready before either attempts admission. The winning callback
// stays live until the other process reports refusal, so a scheduler cannot turn
// an unsafe overlap into an apparently successful serial execution.
func TestMergeCommandSimultaneousAdmission(t *testing.T) {
	testenv.SkipIfShort(t)
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, mergeMode := range []string{"single", "batch"} {
		t.Run(mergeMode, func(t *testing.T) {
			root := t.TempDir()
			if out, err := exec.Command("git", "init", root).CombinedOutput(); err != nil { // #nosec G204 -- test-owned temporary repository.
				t.Fatalf("init: %s: %v", out, err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			type child struct {
				cmd *exec.Cmd
				in  io.WriteCloser
				out *bufio.Reader
			}
			var children []child
			for _, mode := range []string{"run", mergeMode} {
				cmd := exec.CommandContext(ctx, executable, "-test.run=^TestMergeAdmissionChild$") // #nosec G204 -- execute this test binary, never external input.
				cmd.Env = append(os.Environ(), "TAO_TEST_MERGE_ADMISSION="+mode, "TAO_TEST_ADMISSION_ROOT="+root)
				in, err := cmd.StdinPipe()
				if err != nil {
					t.Fatal(err)
				}
				out, err := cmd.StdoutPipe()
				if err != nil {
					t.Fatal(err)
				}
				cmd.Stderr = os.Stderr
				if err := cmd.Start(); err != nil {
					t.Fatal(err)
				}
				children = append(children, child{cmd, in, bufio.NewReader(out)})
			}
			for _, c := range children {
				if line, err := c.out.ReadString('\n'); err != nil || line != "ready\n" {
					t.Fatalf("ready: %q %v", line, err)
				}
			}
			for _, c := range children {
				_, _ = io.WriteString(c.in, "start\n")
			}
			var outcomes []string
			for _, c := range children {
				line, err := c.out.ReadString('\n')
				if err != nil {
					t.Fatal(err)
				}
				outcomes = append(outcomes, line)
			}
			for _, c := range children {
				_, _ = io.WriteString(c.in, "release\n")
				_ = c.in.Close()
			}
			for _, c := range children {
				if err := c.cmd.Wait(); err != nil {
					t.Fatal(err)
				}
			}
			valid := outcomes[0] == "owned\n" && outcomes[1] == "refused\n" || outcomes[1] == "owned\n" && outcomes[0] == "refused\n"
			if !valid {
				t.Fatalf("run and merge overlapped: %q", outcomes)
			}
		})
	}
}

func TestMergeAdmissionChild(t *testing.T) {
	mode := os.Getenv("TAO_TEST_MERGE_ADMISSION")
	if mode == "" {
		t.Skip("subprocess helper")
	}
	root := os.Getenv("TAO_TEST_ADMISSION_ROOT")
	input := bufio.NewReader(os.Stdin)
	guard := func(ctx context.Context, request repoadmission.Request, operation func(context.Context) error) error {
		return repoadmission.With(ctx, request, func(ctx context.Context) error {
			fmt.Println("owned")
			if _, err := input.ReadString('\n'); err != nil {
				return err
			}
			return operation(ctx)
		})
	}
	fmt.Println("ready")
	if _, err := input.ReadString('\n'); err != nil {
		t.Fatal(err)
	}
	var err error
	if mode == "run" {
		err = guard(context.Background(), repoadmission.Request{Root: root, Mode: repoadmission.Run}, func(context.Context) error { return nil })
	} else {
		detail := cliMergeDetail(t)
		detail.State.Repo.Root = root
		registry := logBatchRegistry{&fakeNoteRegistry{dir: t.TempDir(), current: taodata.Repo{ID: "repo", Root: root}}}
		app := App{Out: io.Discard, Err: io.Discard, RuntimeEnv: snapshotWith(nil), RepositoryAdmission: guard, Registry: func() NoteRegistry { return registry }}
		args := []string{"plan-a"}
		repo := fakeRepository{details: map[string]*plan.PlanDetail{"plan-a": detail}}
		if mode == "batch" {
			args = []string{"--all"}
			repo = fakeRepository{}
		} else {
			stubMergeServiceRunner(t, &fakeCLIMergeService{})
		}
		err = app.merge(context.Background(), repo, args)
	}
	var busy *repoadmission.ContentionError
	if errors.As(err, &busy) {
		fmt.Println("refused")
		return
	}
	if err != nil {
		t.Fatal(err)
	}
}

// Synthetic fixtures opt in explicitly; production identity stays fail-closed.
func testRepositoryAdmission(ctx context.Context, _ repoadmission.Request, operation func(context.Context) error) error {
	return operation(ctx)
}
