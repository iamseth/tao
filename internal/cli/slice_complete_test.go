package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/iamseth/tao/internal/agentinput"
	"github.com/iamseth/tao/internal/commandrunner"
	"github.com/iamseth/tao/internal/plan"
)

func newVerifiedCLICompletion(t *testing.T, policy string, commands ...string) (*plan.PlanRecord, []string) {
	t.Helper()
	repo := newCLICommitRepo(t)
	root := filepath.Join(t.TempDir(), "worktree")
	runCLICommitGit(t, repo, "worktree", "add", "-b", "tao/complete", root)
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	fixture := newRunPlanFixture(t, plan.StatusInProgress, []string{"001-a"}, nil, "001-a", plan.StatusInProgress)
	record, err := plan.NewFileRepository("").ResolvePlanRecord(context.Background(), fixture.dir)
	if err != nil {
		t.Fatal(err)
	}
	d := record.Detail()
	head := strings.TrimSpace(runCLICommitGit(t, root, "rev-parse", "HEAD"))
	d.State.Repo.Root = repo
	d.State.Plan.LastRunCommitPolicy = policy
	d.State.Plan.CurrentSlice = new("001-a")
	d.State.Workspace = &plan.Workspace{Strategy: plan.WorkspaceStrategyWorktree, Root: root, Path: root, Branch: "tao/complete", HeadSHA: head, LifecycleStatus: plan.WorkspaceStatusReady}
	slice := &d.Slices.Slices[0]
	slice.Timing.StartedAt = new(time.Now().UTC().Add(-time.Minute))
	slice.ExecutionRoot = root
	slice.ExecutionStart = &plan.SliceExecutionStart{Branch: "tao/complete", Head: head, CommitPolicy: policy, WorkspaceStrategy: plan.WorkspaceStrategyWorktree}
	slice.Verification = plan.Verification{Commands: commands}
	if err := record.PersistArtifacts(); err != nil {
		t.Fatal(err)
	}
	inputs := t.TempDir()
	writeCLICommitFile(t, inputs, "notes", "implemented slice completion")
	writeCLICommitFile(t, inputs, "proposal", `{"type":"feat","scope":"cli","summary":"observe slice gates","what":"Execute declared gates.","why":"Do not trust claims."}`)
	args := []string{"slice-complete", "--plan-dir", fixture.dir, "--slice-id", "001-a", "--notes-file", filepath.Join(inputs, "notes")}
	if policy == "slice" {
		args = append(args, "--commit-proposal-file", filepath.Join(inputs, "proposal"))
	}
	return record, args
}

func reloadCLICompletion(t *testing.T, record *plan.PlanRecord) *plan.PlanDetail {
	t.Helper()
	d, err := plan.NewFileRepository("").ResolvePlan(context.Background(), record.Dir())
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestSliceCompleteObservedGatesAndOptionalClaims(t *testing.T) {
	for _, mode := range []string{"no edits", "commit", "manual", "claims"} {
		t.Run(mode, func(t *testing.T) {
			policy := "slice"
			if mode == "manual" {
				policy = "none"
			}
			record, args := newVerifiedCLICompletion(t, policy, "printf observed")
			root := record.Detail().Slices.Slices[0].ExecutionRoot
			if mode == "commit" || mode == "manual" {
				writeCLICommitFile(t, root, "work.go", "package work\n")
			}
			if mode == "claims" {
				claims := filepath.Join(t.TempDir(), "claims.json")
				writeCLICommitFile(t, filepath.Dir(claims), "claims.json", `[{"command":"printf observed","cwd":".","result":"failed","details":"claim-secret"}]`)
				args = append(args, "--verification-results-file", claims)
			}
			var out bytes.Buffer
			if err := (App{Out: &out, Err: &out}).Run(context.Background(), args); err != nil {
				t.Fatal(err)
			}
			d := reloadCLICompletion(t, record)
			s := d.Slices.Slices[0]
			if s.Status != plan.StatusCompleted || s.CommitIntent == nil || s.CommitIntent.Verification == nil {
				t.Fatalf("incomplete: %+v", s)
			}
			if len(s.VerificationResults) != 1 || s.VerificationResults[0].Source != plan.VerificationSourceTao || !strings.Contains(s.VerificationResults[0].Details, "stdout (tail):\nobserved") {
				t.Fatalf("not observed: %+v", s.VerificationResults)
			}
			if strings.Contains(out.String(), "claim-secret") || !strings.Contains(out.String(), "Gate 1: passed") {
				t.Fatal(out.String())
			}
			if _, err := os.Stat(args[6]); !os.IsNotExist(err) {
				t.Fatalf("notes retained: %v", err)
			}
			if mode == "manual" && s.Completion.Outcome != plan.SliceCompletionManualUncommitted {
				t.Fatal(s.Completion)
			}
			if mode == "claims" {
				found := false
				for _, e := range d.Events {
					if e.Type == plan.EventTypeVerificationClaimMismatch {
						found = true
					}
				}
				if !found {
					t.Fatal("missing mismatch diagnostic")
				}
			}
		})
	}
}

func TestSliceCompleteRejectsInputsBeforeGates(t *testing.T) {
	for _, tc := range []struct{ name, claims, want string }{
		{"forged source", `[{"command":"true","cwd":".","result":"passed","source":"tao"}]`, "unknown field"},
		{"missing result", `[{"command":"true","cwd":"."}]`, "requires bounded"},
		{"null", `null`, "must be an array"},
		{"oversized notes", `[]`, "notes file exceeds"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			record, args := newVerifiedCLICompletion(t, "slice", "true")
			if tc.name == "oversized notes" {
				if err := os.WriteFile(args[6], []byte(strings.Repeat("n", int(agentinput.MaxFileBytes)+1)), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			claims := filepath.Join(t.TempDir(), "claims")
			writeCLICommitFile(t, filepath.Dir(claims), "claims", tc.claims)
			args = append(args, "--verification-results-file", claims)
			var out bytes.Buffer
			err := (App{Out: &out, Err: &out}).Run(context.Background(), args)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want %s", err, tc.want)
			}
			if _, err := os.Stat(args[6]); err != nil {
				t.Fatal("lost repair inputs")
			}
			if s := reloadCLICompletion(t, record).Slices.Slices[0]; s.CommitIntent != nil || s.VerificationAttempt != nil {
				t.Fatal("advanced rejected input")
			}
		})
	}
}

func TestSliceCompleteFailureRepairAndRecovery(t *testing.T) {
	record, args := newVerifiedCLICompletion(t, "slice", "test -f repaired.go")
	root := record.Detail().Slices.Slices[0].ExecutionRoot
	var out bytes.Buffer
	app := App{Out: &out, Err: &out}
	err := app.Run(context.Background(), args)
	for _, want := range []string{"No intent recorded", "Plan-Owned Files", "--gate-command", "--failing-path", "--invalid-command", "--corrected-command"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("missing %q: %v", want, err)
		}
	}
	if s := reloadCLICompletion(t, record).Slices.Slices[0]; s.CommitIntent != nil || s.VerificationAttempt == nil {
		t.Fatal("failure authorized intent")
	}
	writeCLICommitFile(t, root, "repaired.go", "package repaired\n")
	// Keep identical inputs for post-intent recovery after successful cleanup.
	notes, proposal := readText(t, args[6]), readText(t, args[8])
	if err := app.Run(context.Background(), args); err != nil {
		t.Fatal(err)
	}
	writeCLICommitFile(t, filepath.Dir(args[6]), filepath.Base(args[6]), notes)
	writeCLICommitFile(t, filepath.Dir(args[8]), filepath.Base(args[8]), proposal)
	head := runCLICommitGit(t, root, "rev-parse", "HEAD")
	app.CommandRunner = func(ctx context.Context, cwd, name string, argv []string, stdout, stderr io.Writer) error {
		if name == "sh" {
			t.Fatal("recovery reran gates")
		}
		return commandrunner.DefaultLocal(ctx, cwd, name, argv, stdout, stderr)
	}
	if err := app.Run(context.Background(), args); err != nil {
		t.Fatal(err)
	}
	if got := runCLICommitGit(t, root, "rev-parse", "HEAD"); got != head {
		t.Fatal("recovery committed twice")
	}
}

func TestSliceCompleteMechanicalCorrection(t *testing.T) {
	record, args := newVerifiedCLICompletion(t, "none", "go test pkg/example_test.go")
	root := record.Detail().Slices.Slices[0].ExecutionRoot
	writeCLICommitFile(t, root, "pkg/example_test.go", "package example\n")
	record.Detail().Slices.Slices[0].Verification.Steps = []plan.VerificationStep{{Command: "go test pkg/example_test.go", CWD: "pkg"}}
	if err := record.PersistArtifacts(); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	calls := 0
	app := App{Out: &out, Err: &out, CommandRunner: func(ctx context.Context, cwd, name string, argv []string, stdout, stderr io.Writer) error {
		if name != "sh" {
			return commandrunner.DefaultLocal(ctx, cwd, name, argv, stdout, stderr)
		}
		calls++
		if calls == 1 {
			_, _ = io.WriteString(stderr, "No test files found")
			return errors.New("invalid command")
		}
		if argv[1] != "go test example_test.go" {
			t.Fatalf("unsafe correction %v", argv)
		}
		return nil
	}}
	if err := app.Run(context.Background(), args); err != nil {
		t.Fatal(err)
	}
	if calls != 2 || !strings.Contains(out.String(), "corrected from:") {
		t.Fatal(out.String())
	}
}

func TestSliceCompleteCommandSignalCancelsGate(t *testing.T) {
	record, args := newVerifiedCLICompletion(t, "none", "sleep 30")
	var cancel context.CancelFunc
	old := newCommandSignalContext
	newCommandSignalContext = func(ctx context.Context) (context.Context, context.CancelFunc) {
		var bound context.Context
		bound, cancel = context.WithCancel(ctx)
		return bound, cancel
	}
	t.Cleanup(func() { newCommandSignalContext = old })
	var out bytes.Buffer
	app := App{Out: &out, Err: &out, CommandRunner: func(ctx context.Context, cwd, name string, argv []string, stdout, stderr io.Writer) error {
		if name != "sh" {
			return commandrunner.DefaultLocal(ctx, cwd, name, argv, stdout, stderr)
		}
		cancel()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
			t.Fatal("gate ignored signal")
			return nil
		}
	}}
	if err := app.Run(context.Background(), args); !errors.Is(err, context.Canceled) {
		t.Fatalf("signal: %v", err)
	}
	if s := reloadCLICompletion(t, record).Slices.Slices[0]; s.CommitIntent != nil || s.VerificationAttempt == nil {
		t.Fatal("signal lost diagnostic or created intent")
	}
	if _, err := os.Stat(args[6]); err != nil {
		t.Fatal("signal removed inputs")
	}
}

func TestSliceCompleteHistoricalInputsRecoverVerbatim(t *testing.T) {
	record, args := newVerifiedCLICompletion(t, "slice", "exit 99")
	d := record.Detail()
	s := &d.Slices.Slices[0]
	message := "historical message\n\nVerification: old claim"
	results := []plan.VerificationRun{{Command: "old gate", CWD: s.ExecutionRoot, Result: "passed", Details: "legacy"}}
	payload, err := json.Marshal(struct {
		PlanID  string                 `json:"plan_id"`
		SliceID string                 `json:"slice_id"`
		Policy  string                 `json:"policy"`
		Notes   string                 `json:"notes"`
		Results []plan.VerificationRun `json:"results"`
	}{d.State.Plan.ID, "001-a", "slice", readText(t, args[6]), results})
	if err != nil {
		t.Fatal(err)
	}
	s.CommitIntent = &plan.SliceCommitIntent{Hash: fmt.Sprintf("%x", sha256.Sum256(payload)), Policy: "slice", StartingBranch: s.ExecutionStart.Branch, StartingHead: s.ExecutionStart.Head, Message: message, CreatedAt: time.Now().UTC()}
	if err := record.PersistArtifacts(); err != nil {
		t.Fatal(err)
	}
	args = args[:7] // Historical intent, not a fresh proposal.
	var out bytes.Buffer
	app := App{Out: &out, Err: &out}
	if err := app.Run(context.Background(), args); err == nil || !strings.Contains(err.Error(), "original --verification-results-file") {
		t.Fatalf("missing historical input: %v", err)
	}
	file := filepath.Join(t.TempDir(), "results")
	data, err := json.Marshal(results)
	if err != nil {
		t.Fatal(err)
	}
	writeCLICommitFile(t, filepath.Dir(file), "results", string(data))
	args = append(args, "--verification-results-file", file)
	writeCLICommitFile(t, s.ExecutionRoot, "historical.go", "package historical\n")
	if err := app.Run(context.Background(), args); err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(runCLICommitGit(t, s.ExecutionRoot, "log", "-1", "--format=%B")); got != message {
		t.Fatalf("historical message changed: %q", got)
	}
	if slice := reloadCLICompletion(t, record).Slices.Slices[0]; slice.VerificationAttempt != nil || slice.Status != plan.StatusCompleted {
		t.Fatal("historical recovery reran failing gates")
	}
}

func TestSliceCompleteHelpAndCompletion(t *testing.T) {
	var out bytes.Buffer
	app := App{Out: &out, Err: &out}
	if err := app.Run(context.Background(), []string{"slice-complete", "--help"}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"[--verification-results-file FILE]", "ten-minute", "unchanged remaining agent-session", "not sandboxed", "Final repository verification is unchanged"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("help missing %q", want)
		}
	}
	out.Reset()
	if err := app.Run(context.Background(), []string{"completion", "zsh"}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"slice-complete:Verify declared gates and complete a slice", "--verification-results-file[optional JSON advisory claims"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("completion missing %q", want)
		}
	}
}
