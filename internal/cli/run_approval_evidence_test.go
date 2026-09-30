package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/internal/run"
)

func approvalEvidenceFixture(t *testing.T, blocked, approved bool) runPlanFixture {
	t.Helper()
	clearTaoEnv(t)
	fixture := newRunPlanFixture(t, plan.StatusPlanned, []string{"001-a"}, nil, "001-a", plan.StatusPending)
	record, err := plan.NewFileRepository(fixture.root).ResolvePlanRecord(context.Background(), fixture.id)
	if err != nil {
		t.Fatal(err)
	}
	record.Detail().Slices.Slices[0].Approval = &plan.Approval{Required: true, Reason: "Approval must include factual observations from production."}
	if err := record.PersistArtifacts(); err != nil {
		t.Fatal(err)
	}
	if approved {
		if err := record.ApproveSlice("001-a", "operator", time.Date(2026, 9, 30, 16, 0, 0, 0, time.UTC)); err != nil {
			t.Fatal(err)
		}
	}
	if blocked {
		if err := record.BlockSlice("001-a", "Observations are still missing.", time.Date(2026, 9, 30, 16, 1, 0, 0, time.UTC)); err != nil {
			t.Fatal(err)
		}
	}
	return fixture
}

func approvalEvidenceApp(calls *int, out io.Writer) App {
	return App{Out: out, Err: out, CommandRunner: func(_ context.Context, _ string, _ string, args []string, stdout, _ io.Writer) error {
		writeRunGitOutput(stdout, args)
		return nil
	}, ProcessStarter: func(context.Context, string, string, []string) (run.Process, error) {
		*calls++
		return nil, errors.New("test provider admission reached")
	}}
}

func runApprovalEvidence(app App, fixture runPlanFixture, blocked bool) error {
	args := []string{"--plans-dir", fixture.root, "run", "--execution-mode", "current", "--commit-policy", "none", "--no-review", fixture.id}
	if blocked {
		args = append(args, "--continue")
	}
	return app.Run(context.Background(), args)
}

func loadApprovalEvidence(t *testing.T, fixture runPlanFixture) *plan.PlanDetail {
	t.Helper()
	detail, err := plan.NewFileRepository(fixture.root).ResolvePlan(context.Background(), fixture.id)
	if err != nil {
		t.Fatal(err)
	}
	return detail
}

func requireApprovalEvidenceRefusal(t *testing.T, err error) {
	t.Helper()
	var evidence *plan.ApprovalEvidenceError
	if !errors.Is(err, run.ErrCannotStart) || !errors.As(err, &evidence) {
		t.Fatalf("error = %v, want typed refusal", err)
	}
	for _, want := range []string{"tao edit amend", "--reason-file", "--goal-file", "complete replacement goal", "--add-task", `"approval must include factual observations from production"`} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("missing %q in guidance:\n%v", want, err)
		}
	}
	if strings.Contains(err.Error(), "Resolve the required action") || strings.Contains(err.Error(), "Resolve this blocker") {
		t.Fatalf("generic guidance masked missing facts: %v", err)
	}
}

func TestRunApprovalEvidenceGuidanceGolden(t *testing.T) {
	var out bytes.Buffer
	for _, blocked := range []bool{false, true} {
		fixture := approvalEvidenceFixture(t, blocked, true)
		calls := 0
		app := approvalEvidenceApp(&calls, io.Discard)
		before := loadApprovalEvidence(t, fixture)
		for range 2 {
			err := runApprovalEvidence(app, fixture, blocked)
			requireApprovalEvidenceRefusal(t, err)
			if strings.Contains(err.Error(), "tao approve") {
				t.Fatalf("re-approval suggested: %v", err)
			}
			if calls != 0 {
				t.Fatalf("provider calls = %d", calls)
			}
			after := loadApprovalEvidence(t, fixture)
			if !reflect.DeepEqual(before.State, after.State) || !reflect.DeepEqual(before.Slices, after.Slices) || !reflect.DeepEqual(before.Events, after.Events) {
				t.Fatal("refusal mutated durable lifecycle or block state")
			}
		}
		if out.Len() > 0 {
			out.WriteByte('\n')
		}
		fmt.Fprintf(&out, "Blocked: %t\nError: %s\n", blocked, runApprovalEvidence(app, fixture, blocked))
	}
	fixture := interruptedApprovalEvidenceFixture(t)
	calls := 0
	app := interruptedApprovalEvidenceApp(t, &calls, func() string { return "base" })
	err := app.Run(context.Background(), []string{"--plans-dir", fixture.root, "run", "--no-review", fixture.id})
	requireApprovalEvidenceRefusal(t, err)
	if calls != 0 {
		t.Fatalf("provider calls = %d", calls)
	}
	fmt.Fprintf(&out, "\nInterrupted: true\nError: %s\n", strings.ReplaceAll(err.Error(), fixture.dir, "<plan-dir>"))
	assertGolden(t, "testdata/run_approval_evidence.golden", out.Bytes())
}

func TestRunApprovalEvidenceAmendmentWorkflow(t *testing.T) {
	for _, blocked := range []bool{false, true} {
		for _, field := range []string{"goal", "tasks"} {
			t.Run(fmt.Sprintf("blocked=%t/%s", blocked, field), func(t *testing.T) {
				fixture := approvalEvidenceFixture(t, blocked, true)
				calls := 0
				app := approvalEvidenceApp(&calls, io.Discard)
				requireApprovalEvidenceRefusal(t, runApprovalEvidence(app, fixture, blocked))
				before := loadApprovalEvidence(t, fixture)
				if err := app.Run(context.Background(), []string{"--plans-dir", fixture.root, "approve", "--slice", "001-a", fixture.id}); err != nil {
					t.Fatal(err)
				}
				after := loadApprovalEvidence(t, fixture)
				if !reflect.DeepEqual(before.Slices, after.Slices) || !reflect.DeepEqual(before.Events, after.Events) {
					t.Fatal("re-approval changed approval or journal")
				}
				requireApprovalEvidenceRefusal(t, runApprovalEvidence(app, fixture, blocked))
				args := []string{"--plans-dir", fixture.root, "edit", "amend", fixture.id, "001-a", "--reason-file", writeAmendInput(t, "reason.txt", "Record operator observations in the contract.")}
				if field == "goal" {
					args = append(args, "--goal-file", writeAmendInput(t, "goal.txt", "Diagnose startup using the operator's observation: the test screen stayed blank for 30 seconds."))
				} else {
					args = append(args, "--add-task", "Use the operator's observation: the test screen stayed blank for 30 seconds.")
				}
				if err := app.Run(context.Background(), args); err != nil {
					t.Fatal(err)
				}
				amended := loadApprovalEvidence(t, fixture)
				slice := &amended.Slices.Slices[0]
				if slice.Status != before.Slices.Slices[0].Status || slice.BlockerNote != before.Slices.Slices[0].BlockerNote || !reflect.DeepEqual(slice.Approval, before.Slices.Slices[0].Approval) || plan.CheckSliceApprovalEvidence(slice) != nil {
					t.Fatalf("unexpected amended slice: %+v", slice)
				}
				found := false
				for _, event := range amended.Events {
					if event.Type == plan.EventTypeSliceAmended && event.SliceID == slice.ID && reflect.DeepEqual(event.AmendedFields, []string{field}) {
						found = true
					}
				}
				if !found {
					t.Fatal("missing recorded contract amendment")
				}
				if calls != 0 {
					t.Fatalf("provider started before remediation: %d", calls)
				}
				err := runApprovalEvidence(app, fixture, blocked)
				if calls != 1 || err == nil || !strings.Contains(err.Error(), "test provider admission reached") {
					t.Fatalf("ordinary admission not reached: calls=%d err=%v", calls, err)
				}
			})
		}
	}
}

func interruptedApprovalEvidenceFixture(t *testing.T) runPlanFixture {
	t.Helper()
	fixture := approvalEvidenceFixture(t, false, true)
	record, err := plan.NewFileRepository(fixture.root).ResolvePlanRecord(context.Background(), fixture.id)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	detail := record.Detail()
	detail.State.Repo.Root = t.TempDir()
	detail.State.Workspace = &plan.Workspace{
		Strategy: plan.WorkspaceStrategyWorktree, Root: filepath.Dir(root), Path: root,
		Branch: "feature", HeadSHA: "base", LifecycleStatus: plan.WorkspaceStatusReady,
	}
	detail.State.Plan.LastRunCommitPolicy = "slice"
	detail.State.Plan.LastRunStartingDirty = []string{}
	slice := &detail.Slices.Slices[0]
	slice.ExecutionRoot = root
	slice.ExecutionStart = &plan.SliceExecutionStart{Branch: "feature", Head: "base", CommitPolicy: "slice", WorkspaceStrategy: plan.WorkspaceStrategyWorktree}
	slice.Verification.Commands = []string{"true"}
	if err := record.PersistArtifacts(); err != nil {
		t.Fatal(err)
	}
	if err := record.StartSlice(slice.ID, plan.SliceStartRequest{ExecutionRoot: root, Boundary: slice.ExecutionStart, StartedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	return fixture
}

func approvalEvidenceGuidanceCommands(t *testing.T, guidance string) [][]string {
	t.Helper()
	var commands [][]string
	for _, line := range strings.Split(guidance, "\n") {
		if !strings.HasPrefix(line, "  tao ") {
			continue
		}
		cmd := exec.CommandContext(context.Background(), "sh", "-c", "tao() { printf '%s\\000' \"$@\"; }; "+strings.TrimSpace(line)) //nolint:gosec // G204: capture test-controlled guidance argv without executing Tao.
		cmd.Dir = t.TempDir()
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("decode guidance: %v: %s", err, output)
		}
		commands = append(commands, strings.Split(strings.TrimSuffix(string(output), "\x00"), "\x00"))
	}
	return commands
}

func interruptedApprovalEvidenceApp(t *testing.T, calls *int, liveHead func() string) App {
	t.Helper()
	app := approvalEvidenceApp(calls, io.Discard)
	commonDir := t.TempDir()
	gitDir := filepath.Join(commonDir, "worktrees", "plan")
	if err := os.MkdirAll(gitDir, 0o750); err != nil {
		t.Fatal(err)
	}
	app.CommandRunner = func(_ context.Context, cwd, name string, args []string, stdout, _ io.Writer) error {
		if name != "git" {
			t.Fatalf("unexpected command %s", name)
		}
		switch key := cleanupCommandKey(args); key {
		case "branch --show-current":
			_, _ = io.WriteString(stdout, "feature\n")
		case "rev-parse HEAD":
			_, _ = io.WriteString(stdout, liveHead()+"\n")
		case "rev-parse --git-dir":
			_, _ = io.WriteString(stdout, gitDir+"\n")
		case "rev-parse --git-common-dir":
			_, _ = io.WriteString(stdout, commonDir+"\n")
		case "rev-parse --show-toplevel":
			if len(args) >= 2 && args[0] == "-C" {
				cwd = args[1]
			}
			_, _ = io.WriteString(stdout, cwd+"\n")
		case "status --porcelain", "ls-files --stage -z", "ls-files --others --exclude-standard -z", "diff HEAD", "diff --name-only HEAD":
		default:
			t.Fatalf("unexpected git command %q", key)
		}
		return nil
	}
	return app
}

func TestRunApprovalEvidenceInterruptedRemediation(t *testing.T) {
	for _, field := range []string{"goal", "tasks"} {
		for _, drift := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/drift=%t", field, drift), func(t *testing.T) {
				fixture := interruptedApprovalEvidenceFixture(t)
				calls := 0
				liveHead := "base"
				app := interruptedApprovalEvidenceApp(t, &calls, func() string { return liveHead })
				before := loadApprovalEvidence(t, fixture)
				err := app.Run(context.Background(), []string{"--plans-dir", fixture.root, "run", "--no-review", fixture.id})
				requireApprovalEvidenceRefusal(t, err)
				after := loadApprovalEvidence(t, fixture)
				if !reflect.DeepEqual(before.State, after.State) || !reflect.DeepEqual(before.Slices, after.Slices) || !reflect.DeepEqual(before.Events, after.Events) || calls != 0 {
					t.Fatal("refusal mutated interrupted slice or launched provider")
				}
				commands := approvalEvidenceGuidanceCommands(t, err.Error())
				if len(commands) != 4 || commands[0][0] != "slice-blocked" || !reflect.DeepEqual(commands[3], []string{"run", "--continue", fixture.id}) {
					t.Fatalf("missing status-aware block/amend/continue sequence: %v", commands)
				}
				reason := writeAmendInput(t, "reason.txt", "Interrupted slice needs operator observations recorded in its contract.")
				goal := writeAmendInput(t, "goal.txt", "Diagnose the observed blank screen after 30 seconds.")
				for i, command := range commands {
					if (field == "goal" && i == 2) || (field == "tasks" && i == 1) {
						continue // The rendered amendments are alternatives.
					}
					for j, arg := range command {
						switch arg {
						case "<reason-file>", "<blocker-reason-file>":
							command[j] = reason
						case "<goal-file>":
							command[j] = goal
						case "<task containing actual facts>":
							command[j] = "Use the observed blank screen after 30 seconds."
						}
					}
					if i == 3 && drift {
						liveHead = "changed"
					}
					err = app.Run(context.Background(), append([]string{"--plans-dir", fixture.root}, command...))
					if i == 3 {
						break
					}
					if err != nil {
						t.Fatalf("rendered command %v: %v", command, err)
					}
					after = loadApprovalEvidence(t, fixture)
					slice := &after.Slices.Slices[0]
					original := &before.Slices.Slices[0]
					if slice.Status != plan.StatusBlocked || slice.BlockerNote != "Interrupted slice needs operator observations recorded in its contract." || slice.ExecutionRoot != original.ExecutionRoot || !reflect.DeepEqual(slice.ExecutionStart, original.ExecutionStart) || !reflect.DeepEqual(slice.Approval, original.Approval) || !reflect.DeepEqual(slice.CommitIntent, original.CommitIntent) {
						t.Fatalf("remediation changed boundary or approval: %+v", slice)
					}
				}
				if drift {
					if err == nil || !strings.Contains(err.Error(), "live HEAD advanced beyond the original execution boundary") || calls != 0 {
						t.Fatalf("boundary drift bypassed: calls=%d err=%v", calls, err)
					}
				} else if calls != 1 || err == nil || !strings.Contains(err.Error(), "test provider admission reached") {
					t.Fatalf("ordinary admission not reached: calls=%d err=%v", calls, err)
				}
				after = loadApprovalEvidence(t, fixture)
				if drift && after.Slices.Slices[0].Status != plan.StatusBlocked {
					t.Fatal("unsafe continue cleared blocked state")
				}
				found := false
				for _, event := range after.Events {
					if event.Type == plan.EventTypeSliceAmended && event.SliceID == "001-a" && reflect.DeepEqual(event.AmendedFields, []string{field}) {
						found = true
					}
				}
				if !found || plan.CheckSliceApprovalEvidence(&after.Slices.Slices[0]) != nil {
					t.Fatal("missing real contract amendment")
				}
			})
		}
	}
}

func TestRunApprovalEvidenceNonqualifyingAmendments(t *testing.T) {
	for _, flag := range []string{"reason-only", "--allow-file", "--add-manual-check"} {
		t.Run(flag, func(t *testing.T) {
			fixture := approvalEvidenceFixture(t, true, true)
			calls := 0
			app := approvalEvidenceApp(&calls, io.Discard)
			args := []string{"--plans-dir", fixture.root, "edit", "amend", fixture.id, "001-a", "--reason-file", writeAmendInput(t, "reason.txt", "The test screen stayed blank for 30 seconds.")}
			if flag != "reason-only" {
				args = append(args, flag, "facts.md")
			}
			err := app.Run(context.Background(), args)
			if flag == "reason-only" {
				if err == nil || !strings.Contains(err.Error(), "at least one contract change") {
					t.Fatalf("reason-only error: %v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			requireApprovalEvidenceRefusal(t, runApprovalEvidence(app, fixture, true))
			if calls != 0 {
				t.Fatalf("provider calls = %d", calls)
			}
		})
	}
}

func TestRunApprovalEvidenceAmendmentRetainsApprovalGate(t *testing.T) {
	fixture := approvalEvidenceFixture(t, false, false)
	calls := 0
	app := approvalEvidenceApp(&calls, io.Discard)
	if err := app.Run(context.Background(), []string{"--plans-dir", fixture.root, "edit", "amend", fixture.id, "001-a", "--reason-file", writeAmendInput(t, "reason.txt", "Record facts."), "--add-task", "Use the observed blank screen after 30 seconds."}); err != nil {
		t.Fatal(err)
	}
	err := runApprovalEvidence(app, fixture, false)
	if err == nil || !strings.Contains(err.Error(), "tao approve --slice 001-a "+fixture.id) || calls != 0 {
		t.Fatalf("approval gate lost: calls=%d err=%v", calls, err)
	}
}

func TestRunApprovalEvidenceDeclaredInputUsesExecutionWorktree(t *testing.T) {
	fixture := approvalEvidenceFixture(t, false, true)
	record, err := plan.NewFileRepository(fixture.root).ResolvePlanRecord(context.Background(), fixture.id)
	if err != nil {
		t.Fatal(err)
	}
	// A file outside the selected execution root does not satisfy the declaration.
	otherRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(otherRoot, "facts.md"), []byte("observations"), 0o600); err != nil {
		t.Fatal(err)
	}
	record.Detail().State.Repo.Root = t.TempDir()
	record.Detail().Slices.Slices[0].RequiredInputs = []plan.RequiredInput{{Path: "facts.md", Kind: plan.RequiredInputFile, Reason: "operator observations"}}
	record.Detail().Slices.Slices[0].Verification.Commands = []string{"true"}
	if err := record.PersistArtifacts(); err != nil {
		t.Fatal(err)
	}
	calls := 0
	var out bytes.Buffer
	err = runApprovalEvidence(approvalEvidenceApp(&calls, &out), fixture, false)
	var evidence *plan.ApprovalEvidenceError
	if err == nil || errors.As(err, &evidence) || !strings.Contains(out.String(), "facts.md") || !strings.Contains(err.Error(), "verification preflight") || calls != 0 {
		t.Fatalf("missing input admission: calls=%d err=%v output=%s", calls, err, &out)
	}
	if loadApprovalEvidence(t, fixture).Slices.Slices[0].Status != plan.StatusPending {
		t.Fatal("missing declared input started slice")
	}
	// Existence satisfies readiness, even without contents; it is not attestation.
	if err := os.WriteFile(filepath.Join(record.Detail().State.Repo.Root, "facts.md"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	err = runApprovalEvidence(approvalEvidenceApp(&calls, io.Discard), fixture, false)
	if calls != 1 || err == nil || !strings.Contains(err.Error(), "test provider admission reached") {
		t.Fatalf("declared input did not restore ordinary admission: calls=%d err=%v", calls, err)
	}
}

func TestRunApprovalEvidenceCommandsAreShellSafe(t *testing.T) {
	fixture := approvalEvidenceFixture(t, false, false)
	record, err := plan.NewFileRepository(fixture.root).ResolvePlanRecord(context.Background(), fixture.id)
	if err != nil {
		t.Fatal(err)
	}
	const sliceID = "001 operator's $(touch NEVER); & task"
	slice := &record.Detail().Slices.Slices[0]
	slice.ID = sliceID
	slice.Status = plan.StatusInProgress
	slice.Approval.Reason = "Approval must include factual observations\nfrom production\x1b[31m\r" + strings.Repeat(" noisy", 100)
	record.Detail().State.Plan.PendingSlices = []string{sliceID}
	if err := record.PersistArtifacts(); err != nil {
		t.Fatal(err)
	}
	planPath := filepath.Join(fixture.root, "operator's plan $(touch NEVER); & path")
	if err := os.Rename(fixture.dir, planPath); err != nil {
		t.Fatal(err)
	}
	repo := plan.NewFileRepository(fixture.root)
	evidence := plan.CheckSliceApprovalEvidence(slice)
	if evidence == nil {
		t.Fatal("missing classifier error")
	}
	err = decorateRunCannotStartError(context.Background(), repo, planPath, fmt.Errorf("%w: %w", run.ErrCannotStart, evidence))
	if strings.ContainsAny(err.Error(), "\x1b\r") || strings.Count(err.Error(), "noisy") > 100 {
		t.Fatalf("unbounded or unsafe requirement: %q", err)
	}
	var commands []string
	for _, line := range strings.Split(err.Error(), "\n") {
		if strings.HasPrefix(line, "  tao ") {
			commands = append(commands, strings.TrimSpace(line))
		}
	}
	if len(commands) != 5 {
		t.Fatalf("want block, goal/task alternatives, outstanding approval, and continue commands: %v", commands)
	}
	want := [][]string{
		{"slice-blocked", "--plan-dir", planPath, "--slice-id", sliceID, "--reason-file", "<blocker-reason-file>"},
		{"edit", "amend", planPath, sliceID, "--reason-file", "<reason-file>", "--goal-file", "<goal-file>"},
		{"edit", "amend", planPath, sliceID, "--reason-file", "<reason-file>", "--add-task", "<task containing actual facts>"},
		{"approve", "--slice", sliceID, planPath},
		{"run", "--continue", planPath},
	}
	for i, command := range commands {
		// Execute only a shell function that captures argv, never the real Tao.
		cmd := exec.CommandContext(context.Background(), "sh", "-c", "tao() { printf '%s\\000' \"$@\"; }; "+command) //nolint:gosec // G204: test-controlled guidance exercises shell quoting; Tao is an argv-capturing function.
		cmd.Dir = t.TempDir()
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("unsafe command %q: %v: %s", command, err, output)
		}
		got := strings.Split(strings.TrimSuffix(string(output), "\x00"), "\x00")
		if !reflect.DeepEqual(got, want[i]) {
			t.Fatalf("command argv = %#v, want %#v", got, want[i])
		}
		if _, err := os.Stat(filepath.Join(cmd.Dir, "NEVER")); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("shell metacharacters executed")
		}
	}
}
