package run

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/internal/plantest"
)

// testOptions uses the common manual-commit configuration without defaulting
// omitted collaborators. Tests of automatic policies or dependency setup keep
// their explicit literals.
func testOptions(dependencies RunDependencies, mutators ...func(*Options)) Options {
	options := Options{
		ExecutionConfig: ExecutionConfig{ResolvedRunOptions: ResolvedRunOptions{CommitPolicy: CommitPolicyNone}},
		RunDependencies: dependencies,
	}
	for _, mutate := range mutators {
		mutate(&options)
	}
	return options
}

// testDependencies supplies the common in-memory record factory; executor and
// command runner remain explicit so each test retains its own behavior.
func testDependencies(executor SliceExecutor, runner CommandRunner, mutators ...func(*RunDependencies)) RunDependencies {
	dependencies := RunDependencies{
		SliceExecutor:     executor,
		PlanRecordFactory: memoryPlanRecordFactory,
		CommandRunner:     runner,
	}
	for _, mutate := range mutators {
		mutate(&dependencies)
	}
	return dependencies
}

func (r *memoryRunRepository) ResolvePlan(ctx context.Context, input string) (*plan.PlanDetail, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if r.calls >= len(r.details) {
		return r.details[len(r.details)-1], nil
	}
	detail := r.details[r.calls]
	r.calls++
	return detail, nil
}

func (r *memoryRunRepository) GetPlanExact(ctx context.Context, id string) (*plan.PlanDetail, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	for _, detail := range r.details {
		if detail != nil && detail.State.Plan.ID == id {
			return detail, nil
		}
	}
	return nil, nil
}

func (r *memoryRunRepository) PlanRecord(detail *plan.PlanDetail) (*plan.PlanRecord, error) {
	return plan.NewPlanRecord("", detail)
}

func (r *memoryRunRepository) OpenLogAppend(planDir string) (*os.File, error) { return nil, nil }

func (r *memoryRunRepository) AppendEvent(planDir string, event plan.Event) error {
	return plan.NewFileRepository("").AppendEvent(planDir, event)
}

func persistRunArtifacts(t *testing.T, planDir string, detail *plan.PlanDetail) {
	t.Helper()
	record, err := plan.NewPlanRecord(planDir, detail)
	if err != nil {
		t.Fatal(err)
	}
	if err := record.PersistArtifacts(); err != nil {
		t.Fatal(err)
	}
}

type sliceExecutorFunc func(ctx context.Context, run SliceRun) error

type stateOnlyStartStore struct {
	writeSlicesErr error
	appendEventErr error
	appended       int
}

func (s *stateOnlyStartStore) WriteState(planDir string, payload []byte) error {
	return os.WriteFile(filepath.Join(planDir, "state.json"), payload, 0o600)
}

func (s *stateOnlyStartStore) WriteSlices(planDir string, payload []byte) error {
	if s.writeSlicesErr != nil {
		return s.writeSlicesErr
	}
	return os.WriteFile(filepath.Join(planDir, "slices.json"), payload, 0o600)
}

func (s *stateOnlyStartStore) AppendEvent(string, plan.Event) error {
	s.appended++
	return s.appendEventErr
}

func (f sliceExecutorFunc) RunSlice(ctx context.Context, run SliceRun) error {
	return f(ctx, run)
}

type workspaceStatusStore struct {
	*plantest.Repository
	onStateWrite func() error
}

func (s workspaceStatusStore) WriteState(dir string, payload []byte) error {
	if err := s.Repository.WriteState(dir, payload); err != nil {
		return err
	}
	return s.onStateWrite()
}

type callbackPlanRecord struct {
	PlanMutationRecord
	detail     *plan.PlanDetail
	onStart    func(*plan.PlanDetail, string, time.Time) error
	onContinue func(*plan.PlanDetail, time.Time) error
}

func memoryPlanRecordFactory(detail *plan.PlanDetail) (PlanMutationRecord, error) {
	return persistingPlanRecord(plantest.NewPersistingRepository(), detail)
}

func persistingPlanRecord(repository *plantest.Repository, detail *plan.PlanDetail) (*plan.PlanRecord, error) {
	repository.AddDetail(detail)
	snapshot, err := repository.GetPlan(context.Background(), detail.State.Plan.ID)
	if err != nil {
		return nil, err
	}
	repository.AddDetail(snapshot)
	// Bind the caller's execution detail just as FileRepository.PlanRecord does,
	// but keep the seed independent so reads cannot see unpersisted edits or
	// duplicate events from both the bound detail and the store.
	return plan.NewPlanRecordWithStore(repository, detail.Dir, detail)
}

func callbackPlanRecordFactory(onStart func(*plan.PlanDetail, string, time.Time) error, onContinue func(*plan.PlanDetail, time.Time) error) PlanRecordFactory {
	return func(detail *plan.PlanDetail) (PlanMutationRecord, error) {
		record, err := memoryPlanRecordFactory(detail)
		if err != nil {
			return nil, err
		}
		return callbackPlanRecord{PlanMutationRecord: record, detail: detail, onStart: onStart, onContinue: onContinue}, nil
	}
}

func (r callbackPlanRecord) StartSlice(sliceID string, request plan.SliceStartRequest) error {
	if r.onStart != nil {
		return r.onStart(r.detail, sliceID, request.StartedAt)
	}
	return r.PlanMutationRecord.StartSlice(sliceID, request)
}

func (r callbackPlanRecord) ContinueBlocked(now time.Time) error {
	if r.onContinue != nil {
		return r.onContinue(r.detail, now)
	}
	return r.PlanMutationRecord.ContinueBlocked(now)
}

func testRunExecution(config ExecutionConfig, dependencies RunDependencies) runExecution {
	return newRunExecution(config, dependencies)
}

func testRunExecutionWithOptions(options Options) runExecution {
	return runExecutionFromOptions(options)
}

func settleRunTestSlice(detail *plan.PlanDetail) {
	const sliceID = "001-a"
	for i := range detail.Slices.Slices {
		if detail.Slices.Slices[i].ID == sliceID {
			detail.Slices.Slices[i].ExecutionStart = &plan.SliceExecutionStart{Branch: "feature", Head: "head123"}
			detail.Slices.Slices[i].Completion = &plan.SliceCompletionOutcome{Outcome: plan.SliceCompletionNoChanges, CommitSHA: "head123"}
			return
		}
	}
}

func runPlanDetail(status string, pending []string, completed []string, sliceID string, sliceStatus string, startedAt *time.Time, completedAt *time.Time) *plan.PlanDetail {
	return &plan.PlanDetail{
		Dir: "/plans/plan-a",
		State: plan.State{
			Status:    status,
			Repo:      plan.Repo{Root: ".", Branch: "feature"},
			Workspace: &plan.Workspace{Strategy: plan.WorkspaceStrategyCurrent},
			Plan:      plan.PlanState{ID: "plan-a", Title: "Plan A", CompletedSlices: completed, PendingSlices: pending, Timing: plan.PlanTiming{StartedAt: startedAt, CompletedAt: completedAt}},
		},
		Slices: plan.SlicesFile{Slices: []plan.Slice{{ID: sliceID, Status: sliceStatus, Verification: plan.Verification{Commands: []string{"go test ."}}}}},
	}
}

// scriptedGitRunner is the shared scripted-git test double used by finalize,
// review-cleanliness, and leak-guard tests. It serves successive scripted
// responses from configured queues and records git calls for assertion.
//
// Deliberate exceptions retain their embedded assertions and specialized seams:
// interruptedServiceGitRunner validates linked-worktree cwd/commands (also used
// as Fallback by boundary tests); piTransportGitRunner distinguishes control-repo
// cleanliness from live worktree status; pullRequestCommandRunner also scripts gh;
// fakeReviewGit implements the review interface rather than CommandRunner.
// The inline runners in TestServiceExecuteDoesNotRebaseCurrentMode,
// TestExecutionRootResolverUsesCurrentWorkspaceRoot, and
// TestExecutionRootResolverPreparesWorktreeStrategyMetadata retain their
// command/cwd/no-call assertions verbatim.
type scriptedGitRunner struct {
	// Scripted responses (consumed in sequence).
	Branch    string   // response for "branch --show-current" (default "feature")
	Head      string   // response for "rev-parse HEAD" (default "head123")
	Origin    string   // response for "symbolic-ref --quiet --short refs/remotes/origin/HEAD" (default "origin/main")
	Statuses  []string // successive responses for "status --porcelain"
	Diffs     []string // successive responses for "diff HEAD"
	DiffNames []string // successive responses for "diff --name-only HEAD"

	Outputs   map[string]string        // fixed responses for additional git keys
	Responses map[string]func() string // live responses tied to test state
	Fallback  CommandRunner            // delegate unmatched keys to an assertion-bearing exception

	// Error overrides: if a git key matches, return this error.
	Errors       map[string]error
	DefaultError error  // keys without an Errors entry fail when non-nil
	ErrorOutput  string // stderr accompanying errors

	// Observations.
	Calls      []string  // git subcommand keys in call order (no prefix)
	Order      *[]string // if non-nil, records "git <key>" (for ordering assertions)
	CallLog    *[]string // optional external key log for existing callers
	CommandLog *[]string // optional full command log, including -C
	CallCount  *int

	// internal sequence indices.
	statusIdx int
	diffIdx   int
	nameIdx   int
}

// newScriptedGitRunner returns a runner that serves statuses in order and
// defaults to branch="feature" and origin="origin/main".
func newScriptedGitRunner(statuses ...string) *scriptedGitRunner {
	return &scriptedGitRunner{Branch: "feature", Head: "head123", Origin: "origin/main", Statuses: statuses}
}

// Run implements CommandRunner.
func (r *scriptedGitRunner) Run(ctx context.Context, cwd, name string, args []string, stdout, stderr io.Writer) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if r.CallCount != nil {
		*r.CallCount++
	}
	if name != "git" {
		if r.Fallback != nil {
			return r.Fallback(ctx, cwd, name, args, stdout, stderr)
		}
		return r.DefaultError
	}
	key := runGitKey(args)
	r.Calls = append(r.Calls, key)
	if r.Order != nil {
		*r.Order = append(*r.Order, "git "+key)
	}
	if r.CallLog != nil {
		*r.CallLog = append(*r.CallLog, key)
	}
	if r.CommandLog != nil {
		*r.CommandLog = append(*r.CommandLog, name+" "+strings.Join(args, " "))
	}
	err, overridden := r.Errors[key]
	if !overridden {
		err = r.DefaultError
	}
	if err != nil {
		if r.ErrorOutput != "" {
			_, _ = io.WriteString(stderr, r.ErrorOutput)
		}
		return err
	}
	if overridden {
		return nil
	}
	if output, ok := r.Outputs[key]; ok {
		_, _ = io.WriteString(stdout, output)
		return nil
	}
	if response, ok := r.Responses[key]; ok {
		_, _ = io.WriteString(stdout, response())
		return nil
	}
	if r.Fallback != nil {
		return r.Fallback(ctx, cwd, name, args, stdout, stderr)
	}
	switch key {
	case "branch --show-current":
		if r.Branch != "" {
			_, _ = io.WriteString(stdout, r.Branch+"\n")
		}
	case "rev-parse HEAD":
		if r.Head != "" {
			_, _ = io.WriteString(stdout, r.Head+"\n")
		}
	case "symbolic-ref --quiet --short refs/remotes/origin/HEAD":
		if r.Origin != "" {
			_, _ = io.WriteString(stdout, r.Origin+"\n")
		}
	case "status --porcelain":
		if r.statusIdx < len(r.Statuses) {
			_, _ = io.WriteString(stdout, r.Statuses[r.statusIdx])
			r.statusIdx++
		}
	case "diff HEAD":
		if r.diffIdx < len(r.Diffs) {
			_, _ = io.WriteString(stdout, r.Diffs[r.diffIdx])
			r.diffIdx++
		}
	case "diff --name-only HEAD":
		if r.nameIdx < len(r.DiffNames) {
			_, _ = io.WriteString(stdout, r.DiffNames[r.nameIdx])
			r.nameIdx++
		}
	}
	return nil
}

// These compatibility presets keep callers (including pull_request_test.go)
// unchanged; all command dispatch belongs to scriptedGitRunner.
func runGitFake(calls *[]string, failures map[string]error) CommandRunner {
	runner := newScriptedGitRunner()
	runner.CallLog = calls
	runner.Errors = failures
	runner.ErrorOutput = "checkout failed"
	return runner.Run
}

func runWorkspaceGitFake(calls *[]string) CommandRunner {
	runner := newScriptedGitRunner()
	runner.Branch = "tao/plan-a"
	runner.CallLog = calls
	runner.Outputs = map[string]string{
		"branch --format=%(refname:short) --list main": "main\n",
		"rev-parse main":    "base123\n",
		"rev-parse feature": "base123\n",
	}
	return runner.Run
}

func interruptedServiceRunDetail(t *testing.T, root string) *plan.PlanDetail {
	t.Helper()
	current := "001-a"
	started := time.Date(2026, 7, 16, 17, 0, 0, 0, time.UTC)
	return &plan.PlanDetail{
		Dir: t.TempDir(),
		State: plan.State{
			Status: plan.StatusInProgress,
			Repo:   plan.Repo{Root: t.TempDir(), Branch: "master"},
			Workspace: &plan.Workspace{
				Strategy: plan.WorkspaceStrategyWorktree, Root: filepath.Dir(root), Path: root,
				Branch: "tao/plan-a", HeadSHA: "base", LifecycleStatus: plan.WorkspaceStatusReady,
			},
			Plan: plan.PlanState{
				ID: "plan-a", CurrentSlice: &current, PendingSlices: []string{current},
				LastRunCommitPolicy: CommitPolicySlice.String(), LastRunStartingDirty: []string{},
				Timing: plan.PlanTiming{StartedAt: &started},
			},
		},
		Slices: plan.SlicesFile{Slices: []plan.Slice{{
			ID: current, Status: plan.StatusInProgress, ExecutionRoot: root,
			ExecutionStart: &plan.SliceExecutionStart{Branch: "tao/plan-a", Head: "base", CommitPolicy: CommitPolicySlice.String(), WorkspaceStrategy: plan.WorkspaceStrategyWorktree},
			Timing:         plan.SliceTiming{StartedAt: &started}, Verification: plan.Verification{Commands: []string{"go test ."}},
		}}},
		Events: []plan.Event{{Type: plan.EventTypeSliceStarted, Timestamp: started, PlanID: "plan-a", SliceID: current, Message: "Work started on slice"}},
	}
}

func writeLinkedWorktreeSequencer(t *testing.T, root string) {
	t.Helper()
	gitDir := filepath.Join(t.TempDir(), "repo.git", "worktrees", "plan-a")
	if err := os.MkdirAll(filepath.Join(gitDir, "sequencer"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".git"), []byte("gitdir: "+gitDir+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func interruptedServiceGitRunner(t *testing.T, root string, calls *[]string, status func() string, branch string, head string) CommandRunner {
	t.Helper()
	commonDir := filepath.Join(t.TempDir(), "repo.git")
	gitDir := filepath.Join(commonDir, "worktrees", "plan-a")
	if err := os.MkdirAll(gitDir, 0o750); err != nil {
		t.Fatal(err)
	}
	return func(ctx context.Context, cwd string, name string, args []string, stdout io.Writer, stderr io.Writer) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if name != "git" {
			t.Fatalf("unexpected dependency command %s", name)
		}
		actualCWD := cwd
		if len(args) >= 2 && args[0] == "-C" {
			actualCWD = args[1]
		}
		key := runGitKey(args)
		*calls = append(*calls, key)
		switch key {
		case "rev-parse --show-toplevel":
			_, _ = io.WriteString(stdout, actualCWD+"\n")
		case "rev-parse --git-common-dir":
			_, _ = io.WriteString(stdout, commonDir+"\n")
		case "rev-parse --git-dir":
			if actualCWD != root {
				t.Fatalf("git-dir cwd = %q, want immutable root %q", actualCWD, root)
			}
			_, _ = io.WriteString(stdout, gitDir+"\n")
		case "branch --show-current":
			if actualCWD != root {
				t.Fatalf("branch cwd = %q, want immutable root %q", actualCWD, root)
			}
			_, _ = io.WriteString(stdout, branch+"\n")
		case "rev-parse HEAD":
			if actualCWD != root {
				t.Fatalf("HEAD cwd = %q, want immutable root %q", actualCWD, root)
			}
			_, _ = io.WriteString(stdout, head+"\n")
		case "status --porcelain":
			if actualCWD != root {
				t.Fatalf("status cwd = %q, want immutable root %q", actualCWD, root)
			}
			_, _ = io.WriteString(stdout, status())
		case "ls-files --stage -z", "ls-files --others --exclude-standard -z":
		default:
			t.Fatalf("unexpected git command %q", key)
		}
		return nil
	}
}

func runClock(times ...time.Time) func() time.Time {
	index := 0
	return func() time.Time {
		if index >= len(times) {
			return times[len(times)-1]
		}
		now := times[index]
		index++
		return now
	}
}

func runGitKey(args []string) string {
	if len(args) >= 2 && args[0] == "-C" {
		args = args[2:]
	}
	return strings.Join(args, " ")
}

func runHasGitCall(calls []string, want string) bool {
	return slices.Contains(calls, want)
}

func runHasGitCallPrefix(calls []string, prefix string) bool {
	for _, call := range calls {
		if strings.HasPrefix(call, prefix) {
			return true
		}
	}
	return false
}

func runCallBefore(calls []string, before string, after string) bool {
	beforeIndex := -1
	afterIndex := -1
	for i, call := range calls {
		if call == before {
			beforeIndex = i
		}
		if call == after {
			afterIndex = i
		}
	}
	return beforeIndex >= 0 && afterIndex >= 0 && beforeIndex < afterIndex
}
