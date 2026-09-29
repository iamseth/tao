package run

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/iamseth/tao/internal/commandrunner"
	"github.com/iamseth/tao/internal/durableintent/crashfixture"
	"github.com/iamseth/tao/internal/plan"
)

func TestCompleteVerifiedCrashMatrix(t *testing.T) {
	for _, outcome := range []string{plan.SliceCompletionCommitted, plan.SliceCompletionNoChanges, plan.SliceCompletionManualUncommitted} {
		points := []string{"snapshot", "intent", "settlement"}
		if outcome == plan.SliceCompletionCommitted {
			points = append(points, "stage", "commit")
		}
		for _, point := range points {
			t.Run(outcome+"/"+point, func(t *testing.T) {
				policy := CommitPolicySlice
				if outcome == plan.SliceCompletionManualUncommitted {
					policy = CommitPolicyNone
				}
				request := verifiedCompletionFixture(t, policy, "true")
				root := request.Record.Detail().Slices.Slices[0].ExecutionRoot
				if outcome != plan.SliceCompletionNoChanges {
					if err := os.WriteFile(filepath.Join(root, "change.txt"), []byte("verified bytes"), 0o600); err != nil {
						t.Fatal(err)
					}
				}
				gates, injected := 0, false
				fault := errors.New("injected crash boundary")
				service := SliceCompletionService{CommandRunner: func(ctx context.Context, cwd, name string, args []string, stdout, stderr io.Writer) error {
					if name == "sh" {
						gates++
						// A retry has different output and timing; neither may conflict
						// with a pre-intent observation or replace a frozen intent.
						if gates > 1 {
							time.Sleep(3 * time.Millisecond)
						}
						_, err := io.WriteString(stdout, strings.Repeat("observed ", gates))
						return err
					}
					if !injected && name == "git" {
						data, err := os.ReadFile(filepath.Join(request.Record.Dir(), "slices.json"))
						if err != nil {
							return err
						}
						var artifact plan.SlicesFile
						if err := json.Unmarshal(data, &artifact); err != nil {
							return err
						}
						slice := artifact.Slices[0]
						op := ""
						if len(args) > 2 {
							op = args[2]
						}
						if point == "snapshot" && slice.VerificationAttempt != nil || point == "intent" && slice.CommitIntent != nil {
							injected = true
							return fault
						}
						if point == "stage" && op == "add" || point == "commit" && op == "commit" {
							injected = true
							if err := commandrunner.DefaultLocal(ctx, cwd, name, args, stdout, stderr); err != nil {
								return err
							}
							return fault
						}
						if point == "settlement" && slice.CommitIntent != nil {
							injected = true
							// Refuse the completion journal write, after durable intent.
							if err := os.Mkdir(filepath.Join(request.Record.Dir(), ".mutation.json"), 0o700); err != nil {
								return err
							}
						}
					}
					return commandrunner.DefaultLocal(ctx, cwd, name, args, stdout, stderr)
				}}
				if err := service.CompleteVerified(context.Background(), request); err == nil || !injected {
					t.Fatalf("fault not exercised: injected=%v err=%v", injected, err)
				}
				if point == "settlement" {
					if err := os.Remove(filepath.Join(request.Record.Dir(), ".mutation.json")); err != nil {
						t.Fatal(err)
					}
				}
				before := reloadVerifiedCompletion(t, request).Detail().Slices.Slices[0]
				if before.VerificationAttempt == nil || before.Completion != nil || (before.CommitIntent == nil) != (point == "snapshot") {
					t.Fatalf("incorrect crash boundary: %+v", before)
				}
				// Historical-shaped files are still acceptable advisory input. They
				// never overwrite observed results, even when they claim failure.
				claimsFile := filepath.Join(t.TempDir(), "legacy-results.json")
				payload, _ := json.Marshal([]VerificationClaim{{Command: "true", CWD: root, Result: "failed", Details: "claim-secret"}})
				if err := os.WriteFile(claimsFile, payload, 0o600); err != nil {
					t.Fatal(err)
				}
				var err error
				request.VerificationClaims, err = LoadVerificationClaims(claimsFile)
				if err != nil {
					t.Fatal(err)
				}
				request.VerificationResults = []plan.VerificationRun{{Command: "forged", Result: "failed", DurationMilliseconds: new(int64(999))}}
				if err := service.CompleteVerified(context.Background(), request); err != nil {
					t.Fatal(err)
				}
				after := reloadVerifiedCompletion(t, request).Detail().Slices.Slices[0]
				if after.Completion == nil || after.Completion.Outcome != outcome {
					t.Fatalf("settlement = %+v", after.Completion)
				}
				if point == "snapshot" {
					if gates != 2 || before.VerificationAttempt.AttemptID == after.CommitIntent.Verification.AttemptID || before.VerificationAttempt.Runs[0].OutputDigest == after.VerificationResults[0].OutputDigest {
						t.Fatal("pre-intent retry did not observe fresh evidence")
					}
				} else if gates != 1 || !reflect.DeepEqual(before.CommitIntent, after.CommitIntent) {
					t.Fatal("post-intent retry changed frozen evidence")
				}
				if !reflect.DeepEqual(after.VerificationResults, after.CommitIntent.Verification.Runs) {
					t.Fatal("claims became completion evidence")
				}
				if point == "settlement" {
					// Reconstruct the crash boundary where completion artifacts are
					// installed but the owned event has not yet been appended.
					detail := reloadVerifiedCompletion(t, request).Detail()
					var pendingEvents strings.Builder
					encoder := json.NewEncoder(&pendingEvents)
					for _, event := range detail.Events {
						if event.Type == plan.EventTypeSliceCompleted && event.SliceID == request.SliceID {
							continue
						}
						if err := encoder.Encode(event); err != nil {
							t.Fatal(err)
						}
					}
					if err := os.WriteFile(filepath.Join(request.Record.Dir(), "events.jsonl"), []byte(pendingEvents.String()), 0o600); err != nil {
						t.Fatal(err)
					}
				}
				request.VerificationClaims = nil
				request.CommitProposal = nil
				if err := service.CompleteVerified(context.Background(), request); err != nil {
					t.Fatal(err)
				}
				final := reloadVerifiedCompletion(t, request).Detail()
				if countPlanEvents(final.Events, plan.EventTypeSliceCompleted) != 1 || !reflect.DeepEqual(after, final.Slices.Slices[0]) {
					t.Fatal("settlement was not idempotent")
				}
			})
		}
	}
}

func TestCompleteVerifiedRecoveryRefusesExactBoundaryDrift(t *testing.T) {
	for _, drift := range []string{"message", "parent", "worktree", "proposal"} {
		t.Run(drift, func(t *testing.T) {
			request := verifiedCompletionFixture(t, CommitPolicySlice, "true")
			root := request.Record.Detail().Slices.Slices[0].ExecutionRoot
			if err := os.WriteFile(filepath.Join(root, "change.txt"), []byte("verified"), 0o600); err != nil {
				t.Fatal(err)
			}
			service := SliceCompletionService{CommandRunner: func(ctx context.Context, cwd, name string, args []string, stdout, stderr io.Writer) error {
				if name == "git" && len(args) > 2 && args[2] == "add" {
					return errors.New("stop after intent")
				}
				return commandrunner.DefaultLocal(ctx, cwd, name, args, stdout, stderr)
			}}
			if err := service.CompleteVerified(context.Background(), request); err == nil {
				t.Fatal("missing interruption")
			}
			intent := reloadVerifiedCompletion(t, request).Detail().Slices.Slices[0].CommitIntent
			if intent == nil {
				t.Fatal("missing intent")
			}
			switch drift {
			case "message":
				runCommitTestGitCommand(t, root, "add", "change.txt")
				runCommitTestGitCommand(t, root, "commit", "-m", "wrong message")
			case "parent":
				runCommitTestGitCommand(t, root, "commit", "--allow-empty", "-m", "intervening parent")
				runCommitTestGitCommand(t, root, "add", "change.txt")
				runCommitTestGitCommand(t, root, "commit", "-m", intent.Message)
			case "worktree":
				if err := os.WriteFile(filepath.Join(root, "change.txt"), []byte("unverified"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "proposal":
				request.CommitProposal.Summary = "change the intended message"
			}
			head := runCommitTestGitOutput(t, root, "rev-parse", "HEAD")
			service.CommandRunner = func(ctx context.Context, cwd, name string, args []string, stdout, stderr io.Writer) error {
				if name != "git" || len(args) > 2 && (args[2] == "add" || args[2] == "commit") {
					t.Fatalf("recovery ran gate or mutation: %s %v", name, args)
				}
				return commandrunner.DefaultLocal(ctx, cwd, name, args, stdout, stderr)
			}
			if err := service.CompleteVerified(context.Background(), request); err == nil {
				t.Fatal("accepted drift")
			}
			if slice := reloadVerifiedCompletion(t, request).Detail().Slices.Slices[0]; slice.Completion != nil || !reflect.DeepEqual(intent, slice.CommitIntent) {
				t.Fatal("drift advanced or changed intent")
			}
			if runCommitTestGitOutput(t, root, "rev-parse", "HEAD") != head {
				t.Fatal("drift recovery mutated HEAD")
			}
		})
	}
}

func TestRecoverCommitCrashMatrix(t *testing.T) {
	tests := []struct {
		name          string
		build         func(*testing.T, *crashfixture.Fixture) crashfixture.State
		wantRecovered bool
	}{
		{name: "before intent", build: func(_ *testing.T, fixture *crashfixture.Fixture) crashfixture.State {
			return fixture.BeforeIntent()
		}},
		{name: "after intent", build: func(_ *testing.T, fixture *crashfixture.Fixture) crashfixture.State {
			return fixture.AfterIntent()
		}},
		{name: "after git mutation", build: func(t *testing.T, fixture *crashfixture.Fixture) crashfixture.State {
			return fixture.AfterGitMutation(t, crashfixture.SourceTarget)
		}, wantRecovered: true},
		{name: "after settlement", build: func(t *testing.T, fixture *crashfixture.Fixture) crashfixture.State {
			return fixture.AfterSettlement(t, crashfixture.SourceTarget)
		}, wantRecovered: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := crashfixture.New(t)
			state := test.build(t, fixture)
			detail, request, intent := sliceRecoveryCrashInput(t, fixture)
			head := sliceRecoveryCrashHead(t, fixture)

			err := (SliceCompletionService{}).recoverCommit(context.Background(), fixture.SourceGit, request, intent, head)
			if !test.wantRecovered {
				if err == nil || !strings.Contains(err.Error(), "does not match intent starting") {
					t.Fatalf("recover at %s returned %v, want parent mismatch refusal", state.Point, err)
				}
				if detail.Slices.Slices[0].Completion != nil {
					t.Fatalf("recover at %s recorded completion: %#v", state.Point, detail.Slices.Slices[0].Completion)
				}
				return
			}
			if err != nil {
				t.Fatalf("recover at %s: %v", state.Point, err)
			}
			completion := detail.Slices.Slices[0].Completion
			if completion == nil || completion.Outcome != plan.SliceCompletionCommitted || completion.CommitSHA != state.MutationSHA {
				t.Fatalf("recover at %s completion = %#v, want committed head %s", state.Point, completion, state.MutationSHA)
			}
		})
	}
}

func TestRecoverCommitRefusesParentMismatch(t *testing.T) {
	fixture := crashfixture.New(t)
	fixture.AfterGitMutation(t, crashfixture.SourceTarget)
	_, request, intent := sliceRecoveryCrashInput(t, fixture)
	intent.StartingHead = fixture.BaseSHA

	err := (SliceCompletionService{}).recoverCommit(context.Background(), fixture.SourceGit, request, intent, sliceRecoveryCrashHead(t, fixture))
	if err == nil || !strings.Contains(err.Error(), "does not match intent starting") {
		t.Fatalf("parent mismatch error = %v", err)
	}
}

func TestRecoverCommitRefusesMessageMismatch(t *testing.T) {
	fixture := crashfixture.New(t)
	fixture.AfterGitMutation(t, crashfixture.SourceTarget)
	_, request, intent := sliceRecoveryCrashInput(t, fixture)
	intent.Message = "test: a different intended slice commit"

	err := (SliceCompletionService{}).recoverCommit(context.Background(), fixture.SourceGit, request, intent, sliceRecoveryCrashHead(t, fixture))
	if err == nil || !strings.Contains(err.Error(), "HEAD commit does not match recorded intent") {
		t.Fatalf("message mismatch error = %v", err)
	}
}

func TestRecoverCommitRefusesDirtyWorktreeWithCommitCandidates(t *testing.T) {
	fixture := crashfixture.New(t)
	fixture.AfterGitMutation(t, crashfixture.SourceTarget)
	if err := os.WriteFile(filepath.Join(fixture.SourceWorktree, "dirty.txt"), []byte("unsettled work\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, request, intent := sliceRecoveryCrashInput(t, fixture)

	err := (SliceCompletionService{}).recoverCommit(context.Background(), fixture.SourceGit, request, intent, sliceRecoveryCrashHead(t, fixture))
	if err == nil || !strings.Contains(err.Error(), "worktree is not clean") {
		t.Fatalf("dirty worktree error = %v", err)
	}
}

func sliceRecoveryCrashInput(t *testing.T, fixture *crashfixture.Fixture) (*plan.PlanDetail, SliceCompletionRequest, plan.SliceCommitIntent) {
	t.Helper()
	detail, record := sliceCompletionRecord(t, fixture.SourceWorktree, CommitPolicySlice, &sliceCompletionStore{})
	request := SliceCompletionRequest{
		Record: record, SliceID: "001-a", Notes: "recover crash fixture", Now: time.Now().UTC(),
	}
	intent := plan.SliceCommitIntent{
		Policy: CommitPolicySlice.String(), StartingBranch: crashfixture.SourceBranch,
		StartingHead: fixture.SourceSHA, Message: crashfixture.MutationMessage, CreatedAt: request.Now,
	}
	detail.Slices.Slices[0].CommitIntent = &intent
	return detail, request, intent
}

func sliceRecoveryCrashHead(t *testing.T, fixture *crashfixture.Fixture) string {
	t.Helper()
	head, err := fixture.SourceGit.RevParse(context.Background(), "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(head)
}
