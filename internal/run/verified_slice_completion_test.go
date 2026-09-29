package run

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/iamseth/tao/internal/commandrunner"
	"github.com/iamseth/tao/internal/plan"
)

func verifiedCompletionFixture(t *testing.T, policy CommitPolicy, commands ...string) SliceCompletionRequest {
	t.Helper()
	repo := initSliceCompletionRepo(t)
	if err := os.WriteFile(filepath.Join(repo, "binary.dat"), []byte{0, 0, 0}, 0o600); err != nil {
		t.Fatal(err)
	}
	runCommitTestGitCommand(t, repo, "add", "binary.dat")
	runCommitTestGitCommand(t, repo, "commit", "-m", "binary baseline")
	root := filepath.Join(t.TempDir(), "worktree")
	runCommitTestGitCommand(t, repo, "worktree", "add", "-b", "tao/verified", root)
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	detail, _ := sliceCompletionRecord(t, root, policy, &sliceCompletionStore{})
	detail.Dir = t.TempDir()
	detail.State.Repo.Root = repo
	detail.State.Workspace = &plan.Workspace{Strategy: plan.WorkspaceStrategyWorktree, Root: root, Path: root, Branch: "tao/verified", HeadSHA: detail.Slices.Slices[0].ExecutionStart.Head, LifecycleStatus: plan.WorkspaceStatusReady}
	detail.Slices.Slices[0].ExecutionStart.CommitPolicy = policy.String()
	detail.Slices.Slices[0].ExecutionStart.WorkspaceStrategy = plan.WorkspaceStrategyWorktree
	detail.Slices.Slices[0].Verification = plan.Verification{Commands: commands}
	record, err := plan.NewPlanRecord(detail.Dir, detail)
	if err != nil {
		t.Fatal(err)
	}
	if err := record.PersistArtifacts(); err != nil {
		t.Fatal(err)
	}
	return SliceCompletionRequest{Record: record, SliceID: "001-a", Notes: "verified completion", CommitProposal: sliceCompletionProposal(), Now: time.Now().UTC()}
}

func reloadVerifiedCompletion(t *testing.T, request SliceCompletionRequest) *plan.PlanRecord {
	t.Helper()
	record, err := plan.NewFileRepository("").ResolvePlanRecord(context.Background(), request.Record.Dir())
	if err != nil {
		t.Fatal(err)
	}
	return record
}

func TestHistoricalSettlementRefusesNewTransaction(t *testing.T) {
	request := verifiedCompletionFixture(t, CommitPolicySlice, "exit 1")
	if err := (SliceCompletionService{}).settleHistoricalCompletion(context.Background(), request); err == nil || !strings.Contains(err.Error(), "recorded historical intent") {
		t.Fatalf("claim-only historical bypass: %v", err)
	}
}

func TestCompleteRejectsClaimOnlySuccess(t *testing.T) {
	request := verifiedCompletionFixture(t, CommitPolicySlice, "exit 1")
	request.VerificationResults = []plan.VerificationRun{{Command: "exit 1", Result: "passed"}}
	request.VerificationClaims = []VerificationClaim{{Command: "exit 1", CWD: ".", Result: "passed"}}
	if err := (SliceCompletionService{}).Complete(context.Background(), request); err == nil {
		t.Fatal("accepted claim-only success despite a failing declared gate")
	}
	slice := reloadVerifiedCompletion(t, request).Detail().Slices.Slices[0]
	if slice.CommitIntent != nil || slice.VerificationAttempt == nil {
		t.Fatal("expected observed failure without intent")
	}
}

func TestCompleteVerifiedRejectsProposalBeforeGates(t *testing.T) {
	request := verifiedCompletionFixture(t, CommitPolicySlice, "true")
	request.CommitProposal.Summary = ""
	calls := 0
	service := SliceCompletionService{CommandRunner: func(ctx context.Context, cwd, name string, args []string, stdout, stderr io.Writer) error {
		if name == "sh" {
			calls++
		}
		return commandrunner.DefaultLocal(ctx, cwd, name, args, stdout, stderr)
	}}
	if err := service.Complete(context.Background(), request); err == nil {
		t.Fatal("accepted invalid proposal")
	}
	if calls != 0 {
		t.Fatalf("ran %d gates", calls)
	}
	slice := reloadVerifiedCompletion(t, request).Detail().Slices.Slices[0]
	if slice.CommitIntent != nil || slice.VerificationAttempt != nil {
		t.Fatalf("persisted evidence/intent: %+v", slice)
	}
}

func TestCompleteVerifiedFailedAttemptDoesNotMutateGit(t *testing.T) {
	for _, failure := range []string{"exit", "unknown", "unsafe correction", "cancelled", "content drift", "binary drift", "index drift", "head drift", "declaration drift", "snapshot write"} {
		t.Run(failure, func(t *testing.T) {
			request := verifiedCompletionFixture(t, CommitPolicySlice, "true")
			root := request.Record.Detail().Slices.Slices[0].ExecutionRoot
			if err := os.WriteFile(filepath.Join(root, "change.go"), []byte("package change\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			if failure == "unsafe correction" {
				request.Record.Detail().Slices.Slices[0].Verification.Commands = []string{"go test pkg/example_test.go && true"}
				if err := request.Record.PersistArtifacts(); err != nil {
					t.Fatal(err)
				}
			}
			if failure == "binary drift" {
				if err := os.WriteFile(filepath.Join(root, "binary.dat"), []byte{0, 1, 0}, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			parent := runCommitTestGitOutput(t, root, "rev-parse", "HEAD")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			calls := 0
			service := SliceCompletionService{CommandRunner: func(ctx context.Context, cwd, name string, args []string, stdout, stderr io.Writer) error {
				if name != "sh" {
					return commandrunner.DefaultLocal(ctx, cwd, name, args, stdout, stderr)
				}
				calls++
				switch failure {
				case "exit":
					return commandrunner.DefaultLocal(ctx, cwd, "sh", []string{"-c", "exit 1"}, stdout, stderr)
				case "unknown":
					return errors.New("unknown launch failure")
				case "unsafe correction":
					_, _ = io.WriteString(stderr, "No test files found\n")
					return errors.New("invalid command")
				case "cancelled":
					cancel()
					return nil
				case "content drift":
					return os.WriteFile(filepath.Join(root, "change.go"), []byte("package drift\n"), 0o600)
				case "binary drift":
					return os.WriteFile(filepath.Join(root, "binary.dat"), []byte{0, 2, 0}, 0o600)
				case "index drift":
					runCommitTestGitCommand(t, root, "add", "change.go")
					return nil
				case "head drift":
					runCommitTestGitCommand(t, root, "commit", "--allow-empty", "-m", "external")
					return nil
				case "declaration drift":
					record := reloadVerifiedCompletion(t, request)
					record.Detail().Slices.Slices[0].Verification.Commands = []string{"echo changed"}
					return record.PersistArtifacts()
				case "snapshot write":
					return os.Mkdir(filepath.Join(request.Record.Dir(), ".mutation.json"), 0o700)
				}
				return nil
			}}
			err := service.Complete(ctx, request)
			if err == nil {
				t.Fatal("accepted unsafe completion")
			}
			if calls != 1 {
				t.Fatalf("gate calls = %d; error %v", calls, err)
			}
			if failure == "snapshot write" {
				_ = os.Remove(filepath.Join(request.Record.Dir(), ".mutation.json"))
			}
			slice := reloadVerifiedCompletion(t, request).Detail().Slices.Slices[0]
			if slice.CommitIntent != nil || slice.Completion != nil || slice.Status != plan.StatusInProgress {
				t.Fatalf("advanced failed slice: %+v", slice)
			}
			if got := runCommitTestGitOutput(t, root, "rev-parse", "HEAD"); got != parent && failure != "head drift" {
				t.Fatal("advanced HEAD")
			}
			if staged := runCommitTestGitOutput(t, root, "diff", "--cached", "--name-only"); strings.TrimSpace(staged) != "" && failure != "index drift" {
				t.Fatalf("staged %s", staged)
			}
			if failure == "exit" || failure == "unknown" || failure == "cancelled" {
				if slice.VerificationAttempt == nil {
					t.Fatal("lost failed attempt")
				}
			}
		})
	}
}

func TestCompleteVerifiedSuccessAndRecovery(t *testing.T) {
	for _, outcome := range []string{plan.SliceCompletionCommitted, plan.SliceCompletionNoChanges, plan.SliceCompletionManualUncommitted} {
		t.Run(outcome, func(t *testing.T) {
			policy := CommitPolicySlice
			if outcome == plan.SliceCompletionManualUncommitted {
				policy = CommitPolicyNone
			}
			request := verifiedCompletionFixture(t, policy, "true", "true")
			root := request.Record.Detail().Slices.Slices[0].ExecutionRoot
			if outcome != plan.SliceCompletionNoChanges {
				if err := os.WriteFile(filepath.Join(root, "change.go"), []byte("package change\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			calls := 0
			service := SliceCompletionService{CommandRunner: func(ctx context.Context, cwd, name string, args []string, stdout, stderr io.Writer) error {
				if name == "sh" {
					calls++
				}
				return commandrunner.DefaultLocal(ctx, cwd, name, args, stdout, stderr)
			}}
			if err := service.Complete(context.Background(), request); err != nil {
				t.Fatal(err)
			}
			slice := reloadVerifiedCompletion(t, request).Detail().Slices.Slices[0]
			if slice.Completion == nil || slice.Completion.Outcome != outcome || slice.CommitIntent.Verification == nil {
				t.Fatalf("completion = %+v", slice)
			}
			if calls != 2 {
				t.Fatalf("gate count = %d", calls)
			}
			if policy == CommitPolicySlice && (!strings.Contains(slice.CommitIntent.Message, "Tao-Verify-Exit: 1: 0") || !strings.Contains(slice.CommitIntent.Message, "Tao-Verify-Exit: 2: 0")) {
				t.Fatalf("missing ordered trailers: %s", slice.CommitIntent.Message)
			}
			// Claims and the mutable latest attempt must not become recovery authority.
			request.VerificationResults = []plan.VerificationRun{{Command: "forged", Result: "passed"}}
			request.VerificationClaims = []VerificationClaim{{Command: "true", CWD: root, Result: "failed"}}
			request.CommitProposal = nil
			t.Setenv(sliceCompletionOwnerEnv, "dead-session")
			if err := service.Complete(context.Background(), request); err != nil {
				t.Fatalf("recover: %v", err)
			}
			if calls != 2 {
				t.Fatal("reran verification during recovery")
			}
		})
	}
}

func TestCompleteVerifiedInterruptedIntentRecovery(t *testing.T) {
	for _, point := range []string{"before stage", "after stage", "after commit"} {
		t.Run(point, func(t *testing.T) {
			request := verifiedCompletionFixture(t, CommitPolicySlice, "true")
			root := request.Record.Detail().Slices.Slices[0].ExecutionRoot
			if err := os.WriteFile(filepath.Join(root, "change.go"), []byte("package change\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			gates := 0
			interrupted := false
			service := SliceCompletionService{CommandRunner: func(ctx context.Context, cwd, name string, args []string, stdout, stderr io.Writer) error {
				if name == "sh" {
					gates++
				}
				operation := ""
				if name == "git" && len(args) > 2 {
					operation = args[2]
				}
				if !interrupted && ((point != "after commit" && operation == "add") || (point == "after commit" && operation == "commit")) {
					interrupted = true
					if point != "before stage" {
						if err := commandrunner.DefaultLocal(ctx, cwd, name, args, stdout, stderr); err != nil {
							return err
						}
					}
					return errors.New("interrupted transaction")
				}
				return commandrunner.DefaultLocal(ctx, cwd, name, args, stdout, stderr)
			}}
			if err := service.Complete(context.Background(), request); err == nil {
				t.Fatal("expected interruption")
			}
			record := reloadVerifiedCompletion(t, request)
			intent := record.Detail().Slices.Slices[0].CommitIntent
			if intent == nil || intent.Verification == nil {
				t.Fatal("no frozen intent")
			}
			exactMessage, exactHash := intent.Message, intent.Hash
			// Latest diagnostic evidence may disappear without changing recovery authority.
			rewriteVerifiedSliceFixture(t, request, func(slice *plan.Slice) { slice.VerificationAttempt = nil })
			if reloadVerifiedCompletion(t, request).Detail().Slices.Slices[0].VerificationAttempt != nil {
				t.Fatal("fixture retained latest attempt")
			}
			request.CommitProposal = nil
			request.VerificationResults = []plan.VerificationRun{{Result: "forged"}}
			t.Setenv(sliceCompletionOwnerEnv, "expired-owner")
			if err := service.Complete(context.Background(), request); err != nil {
				t.Fatalf("recover: %v", err)
			}
			if gates != 1 {
				t.Fatalf("reran gates: %d", gates)
			}
			settled := reloadVerifiedCompletion(t, request).Detail().Slices.Slices[0]
			if settled.CommitIntent.Message != exactMessage || settled.CommitIntent.Hash != exactHash || settled.Completion == nil {
				t.Fatalf("changed frozen intent: %+v", settled)
			}
			if message := strings.TrimRight(runCommitTestGitOutput(t, root, "log", "-1", "--format=%B"), "\n"); message != exactMessage {
				t.Fatal("commit message differs from intent")
			}
			request.Notes = "different notes"
			if err := service.Complete(context.Background(), request); err == nil {
				t.Fatal("accepted conflicting notes")
			}
		})
	}
}

func TestCompleteVerifiedNoneInterruptedBoundaryRecovery(t *testing.T) {
	for _, drift := range []string{"unchanged", "branch", "head"} {
		t.Run(drift, func(t *testing.T) {
			request := verifiedCompletionFixture(t, CommitPolicyNone, "true")
			root := request.Record.Detail().Slices.Slices[0].ExecutionRoot
			gates := 0
			interrupt := true
			service := SliceCompletionService{CommandRunner: func(ctx context.Context, cwd, name string, args []string, stdout, stderr io.Writer) error {
				if name == "sh" {
					gates++
				}
				if interrupt && name == "git" && reloadVerifiedCompletion(t, request).Detail().Slices.Slices[0].CommitIntent != nil {
					return errors.New("interrupted after intent")
				}
				return commandrunner.DefaultLocal(ctx, cwd, name, args, stdout, stderr)
			}}
			if err := service.Complete(context.Background(), request); err == nil {
				t.Fatal("expected interruption")
			}
			before := reloadVerifiedCompletion(t, request).Detail().Slices.Slices[0]
			if before.CommitIntent == nil || before.CommitIntent.Verification == nil || before.Completion != nil {
				t.Fatalf("expected incomplete verified intent: %+v", before)
			}
			switch drift {
			case "branch":
				runCommitTestGitCommand(t, root, "checkout", "-b", "tao/other")
			case "head":
				if err := os.WriteFile(filepath.Join(root, "new.txt"), []byte("unverified tree\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				runCommitTestGitCommand(t, root, "add", "new.txt")
				runCommitTestGitCommand(t, root, "commit", "-m", "external change")
			}
			if status := runCommitTestGitOutput(t, root, "status", "--porcelain"); strings.TrimSpace(status) != "" {
				t.Fatalf("expected clean worktree: %s", status)
			}
			interrupt = false
			err := service.Complete(context.Background(), request)
			after := reloadVerifiedCompletion(t, request).Detail()
			slice := after.Slices.Slices[0]
			if gates != 1 {
				t.Fatalf("reran gates: %d", gates)
			}
			if drift == "unchanged" {
				if err != nil || slice.Completion == nil || slice.Completion.Outcome != plan.SliceCompletionManualUncommitted {
					t.Fatalf("unchanged boundary did not recover: %v; %+v", err, slice)
				}
			} else {
				if err == nil || !strings.Contains(err.Error(), "changed") {
					t.Fatalf("expected boundary drift refusal: %v", err)
				}
				if slice.Completion != nil || slice.Status != plan.StatusInProgress || len(slice.VerificationResults) != 0 {
					t.Fatalf("settled drifted intent: %+v", slice)
				}
				for _, event := range after.Events {
					if event.Type == plan.EventTypeSliceCompleted {
						t.Fatal("published completion for drifted intent")
					}
				}
			}
		})
	}
}

func TestCompleteVerifiedRecoversBothHistoricalHashesVerbatim(t *testing.T) {
	for _, legacy := range []bool{true, false} {
		t.Run(map[bool]string{true: "legacy", false: "message-bound"}[legacy], func(t *testing.T) {
			request := verifiedCompletionFixture(t, CommitPolicySlice, "must never run")
			root := request.Record.Detail().Slices.Slices[0].ExecutionRoot
			if err := os.WriteFile(filepath.Join(root, "old.go"), []byte("package old\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			message := "old unvalidated message"
			if !legacy {
				var err error
				message, err = formatSliceCommitMessage("plan-a", request.SliceID, *request.CommitProposal)
				if err != nil {
					t.Fatal(err)
				}
			}
			request.VerificationResults = []plan.VerificationRun{{Command: "historical command", CWD: root, Result: "passed", Details: "historical prose"}}
			hash, err := sliceCompletionHash("plan-a", request.SliceID, "slice", request.Notes, request.VerificationResults, message)
			if legacy {
				hash, err = legacySliceCompletionHash("plan-a", request.SliceID, "slice", request.Notes, request.VerificationResults)
			}
			if err != nil {
				t.Fatal(err)
			}
			start := request.Record.Detail().Slices.Slices[0].ExecutionStart
			if err := request.Record.RecordSliceCommitIntent(request.SliceID, plan.SliceCommitIntent{Hash: hash, Policy: "slice", StartingBranch: start.Branch, StartingHead: start.Head, Message: message, CreatedAt: request.Now}); err != nil {
				t.Fatal(err)
			}
			request.CommitProposal = nil
			t.Setenv(sliceCompletionOwnerEnv, "ended")
			service := SliceCompletionService{CommandRunner: func(ctx context.Context, cwd, name string, args []string, stdout, stderr io.Writer) error {
				if name != "git" {
					t.Fatalf("unexpected command/provider fallback: %s", name)
				}
				return commandrunner.DefaultLocal(ctx, cwd, name, args, stdout, stderr)
			}}
			wrong := request
			wrong.VerificationResults = nil
			if err := service.Complete(context.Background(), wrong); err == nil {
				t.Fatal("historical recovery dropped required inputs")
			}
			if err := service.Complete(context.Background(), request); err != nil {
				t.Fatal(err)
			}
			if got := strings.TrimRight(runCommitTestGitOutput(t, root, "log", "-1", "--format=%B"), "\n"); got != message {
				t.Fatalf("rewrote historical message: %q", got)
			}
		})
	}
}

func TestCompleteVerifiedRetryAndDiagnostics(t *testing.T) {
	request := verifiedCompletionFixture(t, CommitPolicySlice, "first", "go test pkg/example_test.go", "last")
	root := request.Record.Detail().Slices.Slices[0].ExecutionRoot
	if err := os.Mkdir(filepath.Join(root, "pkg"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "pkg", "example_test.go"), []byte("package example\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	request.Record.Detail().Slices.Slices[0].Verification.Steps = []plan.VerificationStep{{Command: "go test pkg/example_test.go", CWD: "pkg"}}
	if err := request.Record.PersistArtifacts(); err != nil {
		t.Fatal(err)
	}
	request.VerificationClaims = []VerificationClaim{{Command: "go test pkg/example_test.go", CWD: filepath.Join(root, "pkg"), Result: "passed", Details: "must not become evidence"}}
	failLast := true
	var commands []string
	service := SliceCompletionService{CommandRunner: func(ctx context.Context, cwd, name string, args []string, stdout, stderr io.Writer) error {
		if name != "sh" {
			return commandrunner.DefaultLocal(ctx, cwd, name, args, stdout, stderr)
		}
		commands = append(commands, args[1])
		if args[1] == "go test pkg/example_test.go" {
			_, _ = io.WriteString(stderr, "No test files found")
			return commandrunner.DefaultLocal(ctx, cwd, "sh", []string{"-c", "exit 1"}, stdout, stderr)
		}
		if args[1] == "last" && failLast {
			return errors.New("failed last gate")
		}
		return nil
	}}
	if err := service.Complete(context.Background(), request); err == nil {
		t.Fatal("accepted failed last gate")
	}
	record := reloadVerifiedCompletion(t, request)
	snapshot := record.Detail().Slices.Slices[0].VerificationAttempt
	if snapshot == nil || len(snapshot.Runs) != 4 || snapshot.Runs[2].OriginalCommand == "" {
		t.Fatalf("snapshot = %+v", snapshot)
	}
	request.Record = record
	if err := recordVerificationDiagnostics(request, *snapshot); err != nil {
		t.Fatal(err)
	}
	if err := recordVerificationDiagnostics(request, *snapshot); err != nil {
		t.Fatal(err)
	}
	events := reloadVerifiedCompletion(t, request).Detail().Events
	for _, kind := range []string{plan.EventTypeVerificationCommandInvalid, plan.EventTypeVerificationClaimMismatch} {
		found := 0
		for _, event := range events {
			if event.Type == kind {
				found++
				if event.VerificationAttemptID != snapshot.AttemptID || event.SliceID != request.SliceID || event.PlanID != "plan-a" {
					t.Fatalf("missing diagnostic identity: %+v", event)
				}
				if kind == plan.EventTypeVerificationCommandInvalid && (event.Command != "go test pkg/example_test.go" || event.CorrectedCommand != "go test example_test.go" || event.Result != "passed" || event.ExitCode == nil || *event.ExitCode != 1) {
					t.Fatalf("successful correction lost original failure: %+v", event)
				}
			}
		}
		if found != 1 {
			t.Fatalf("%s count = %d", kind, found)
		}
	}
	failLast = false
	if err := service.Complete(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	settled := reloadVerifiedCompletion(t, request).Detail()
	if len(commands) != 8 || commands[0] != "first" || commands[4] != "first" {
		t.Fatalf("retry skipped gates: %v", commands)
	}
	if settled.Slices.Slices[0].CommitIntent.Verification.AttemptID == snapshot.AttemptID {
		t.Fatal("conflated distinct attempts")
	}
	if countPlanEvents(settled.Events, plan.EventTypeVerificationCommandInvalid) != 2 || countPlanEvents(settled.Events, plan.EventTypeVerificationClaimMismatch) != 2 {
		t.Fatal("lost distinct diagnostics")
	}
	message := settled.Slices.Slices[0].CommitIntent.Message
	if !strings.Contains(message, "Tao-Verify-Exit: 2: 1") || !strings.Contains(message, "Tao-Verify-Exit: 3: 0") {
		t.Fatalf("lost correction trailers: %s", message)
	}
}

func TestCompleteVerifiedOptionalClaimsNeverAuthorizeOrLeak(t *testing.T) {
	for _, mode := range []string{"absent", "matched", "mismatched", "unmatched"} {
		t.Run(mode, func(t *testing.T) {
			request := verifiedCompletionFixture(t, CommitPolicyNone, "true")
			root := request.Record.Detail().Slices.Slices[0].ExecutionRoot
			if mode != "absent" {
				claim := VerificationClaim{Command: "true", CWD: root, Result: "passed", Details: strings.Repeat("claim-secret", 1000)}
				if mode == "mismatched" {
					claim.Result = "failed"
				}
				if mode == "unmatched" {
					claim.Command = "never executed"
				}
				request.VerificationClaims = []VerificationClaim{claim}
			}
			service := SliceCompletionService{CommandRunner: func(ctx context.Context, cwd, name string, args []string, stdout, stderr io.Writer) error {
				if name == "sh" {
					_, err := io.WriteString(stdout, "password=output-secret")
					return err
				}
				return commandrunner.DefaultLocal(ctx, cwd, name, args, stdout, stderr)
			}}
			if err := service.Complete(context.Background(), request); err != nil {
				t.Fatal(err)
			}
			detail := reloadVerifiedCompletion(t, request).Detail()
			results := detail.Slices.Slices[0].VerificationResults
			if len(results) != 1 || results[0].Source != plan.VerificationSourceTao || results[0].Result != "passed" || !strings.Contains(results[0].Details, "output-secret") {
				t.Fatalf("lost local observed evidence: %+v", results)
			}
			mismatches := 0
			for _, event := range detail.Events {
				if event.Type != plan.EventTypeVerificationClaimMismatch {
					continue
				}
				mismatches++
				if event.Command != "true" || event.Result != "passed" || event.ClaimedResult != "failed" || event.VerificationAttemptID != detail.Slices.Slices[0].VerificationAttempt.AttemptID {
					t.Fatalf("incorrect bounded mismatch: %+v", event)
				}
				encoded, _ := json.Marshal(event)
				if strings.Contains(string(encoded), "secret") || len(encoded) > 1024 {
					t.Fatalf("unbounded/private mismatch event: %s", encoded)
				}
			}
			want := 0
			if mode == "mismatched" {
				want = 1
			}
			if mismatches != want {
				t.Fatalf("mismatches = %d, want %d", mismatches, want)
			}
		})
	}
}

func TestCompleteVerifiedReloadedAdmissionAndLock(t *testing.T) {
	for _, reason := range []string{"approval", "dependency", "policy", "selected", "boundary", "branch", "lock"} {
		t.Run(reason, func(t *testing.T) {
			request := verifiedCompletionFixture(t, CommitPolicySlice, "true")
			record := reloadVerifiedCompletion(t, request)
			detail := record.Detail()
			switch reason {
			case "approval":
				detail.Slices.Slices[0].Approval = &plan.Approval{Required: true}
			case "dependency":
				detail.Slices.Slices[0].DependsOn = []string{"missing"}
			case "policy":
				detail.State.Plan.LastRunCommitPolicy = "plan"
			case "selected":
				detail.State.Plan.CurrentSlice = new("another")
			case "boundary":
				detail.Slices.Slices[0].ExecutionStart = nil
			case "branch":
				runCommitTestGitCommand(t, detail.Slices.Slices[0].ExecutionRoot, "checkout", "-b", "changed")
			case "lock":
				release, err := acquireSliceCompletionLock(record.Dir())
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = release() }()
			}
			if err := record.PersistArtifacts(); err != nil {
				t.Fatal(err)
			}
			if reason == "boundary" {
				rewriteVerifiedSliceFixture(t, request, func(slice *plan.Slice) { slice.ExecutionStart = nil })
			}
			service := SliceCompletionService{CommandRunner: func(ctx context.Context, cwd, name string, args []string, stdout, stderr io.Writer) error {
				if name != "git" {
					t.Fatalf("gate/provider ran before admission: %s", name)
				}
				return commandrunner.DefaultLocal(ctx, cwd, name, args, stdout, stderr)
			}}
			if err := service.Complete(context.Background(), request); err == nil {
				t.Fatal("accepted unsafe admission")
			}
			if slice := reloadVerifiedCompletion(t, request).Detail().Slices.Slices[0]; slice.CommitIntent != nil || slice.VerificationAttempt != nil {
				t.Fatal("persisted before admission")
			}
		})
	}
}

func rewriteVerifiedSliceFixture(t *testing.T, request SliceCompletionRequest, change func(*plan.Slice)) {
	t.Helper()
	record := reloadVerifiedCompletion(t, request)
	artifact := record.Detail().Slices
	change(&artifact.Slices[0])
	payload, err := json.Marshal(artifact)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(record.Dir(), "slices.json"), payload, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestCompleteVerifiedOwnerEndsDuringGate(t *testing.T) {
	request := verifiedCompletionFixture(t, CommitPolicySlice, "true")
	_, closeOwner, err := startSliceCompletionLifetime(context.Background(), request.Record.Dir(), request.SliceID)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = closeOwner() }()
	owner, err := readCompletionOwner(request.Record.Dir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(sliceCompletionOwnerEnv, owner.Token)
	service := SliceCompletionService{CommandRunner: func(ctx context.Context, cwd, name string, args []string, stdout, stderr io.Writer) error {
		if name == "sh" {
			return closeOwner()
		}
		return commandrunner.DefaultLocal(ctx, cwd, name, args, stdout, stderr)
	}}
	if err := service.Complete(context.Background(), request); err == nil {
		t.Fatal("completed after owner exit")
	}
	if slice := reloadVerifiedCompletion(t, request).Detail().Slices.Slices[0]; slice.CommitIntent != nil || slice.VerificationAttempt == nil {
		t.Fatalf("owner death snapshot/intent = %+v", slice)
	}
}

func TestCompleteVerifiedIgnoresBuildArtifacts(t *testing.T) {
	request := verifiedCompletionFixture(t, CommitPolicyNone, "true")
	root := request.Record.Detail().Slices.Slices[0].ExecutionRoot
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte("build-output\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	service := SliceCompletionService{CommandRunner: func(ctx context.Context, cwd, name string, args []string, stdout, stderr io.Writer) error {
		if name == "sh" {
			return os.WriteFile(filepath.Join(root, "build-output"), []byte("ignored artifact"), 0o600)
		}
		return commandrunner.DefaultLocal(ctx, cwd, name, args, stdout, stderr)
	}}
	if err := service.Complete(context.Background(), request); err != nil {
		t.Fatal(err)
	}
}
