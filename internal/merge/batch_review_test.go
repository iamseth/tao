package merge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	commitpkg "github.com/iamseth/tao/internal/commit"
	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/internal/reviewcontract"
	"github.com/iamseth/tao/internal/runtimeconfig"
)

type batchReviewAgentFunc func(context.Context, string, string) (string, error)

func (f batchReviewAgentFunc) Resolve(ctx context.Context, request BatchAgentSessionRequest) (BatchAgentSessionResult, error) {
	output, err := f(ctx, request.IntegrationRoot, request.Prompt)
	return BatchAgentSessionResult{Output: output}, err
}

type batchReviewTestStore struct {
	recordingBatchTransitionStore
	dir          string
	artifactFail bool
}

func (s *batchReviewTestStore) WriteAggregateReview(_ string, attempt int, output string) (string, error) {
	if s.artifactFail {
		return "", errors.New("artifact failed")
	}
	name := filepath.Join(s.dir, fmt.Sprintf("aggregate-review-%03d.md", attempt))
	return filepath.Base(name), os.WriteFile(name, []byte(output), 0o600)
}

func TestBatchSubmoduleFailureStopsBeforeAggregateGate(t *testing.T) {
	for _, failure := range []error{errors.New("authentication denied"), context.Canceled} {
		t.Run(failure.Error(), func(t *testing.T) {
			fixture, state, root := batchReviewFixture(t)
			if err := os.WriteFile(filepath.Join(root, ".gitmodules"), nil, 0o600); err != nil {
				t.Fatal(err)
			}
			service := NewService(fixture.repoRoot, nil)
			preparations, gates, agents := 0, 0, 0
			service.Runner = func(_ context.Context, cwd, name string, args []string, _, stderr io.Writer) error {
				if name == "git" && strings.Join(args, " ") == "submodule update --init --recursive" {
					preparations++
					if cwd != root {
						t.Fatalf("preparation cwd = %s", cwd)
					}
					_, _ = io.WriteString(stderr, failure.Error())
					return failure
				}
				gates++
				return nil
			}
			reviewer := BatchAggregateReviewer{Service: service, Store: &batchReviewTestStore{dir: t.TempDir()}, Agent: batchReviewAgentFunc(func(context.Context, string, string) (string, error) {
				agents++
				return reviewJSON("approve", "green", ""), nil
			})}
			got, err := reviewer.Review(context.Background(), state, root, BatchReviewOptions{VerifyCommand: "true"})
			if err == nil || !strings.Contains(err.Error(), "submodule") || preparations != 1 || gates != 0 || agents != 0 || got.State.Attempts.AggregateRework != 0 || got.State.VerificationAttribution != nil || got.State.Verification != nil || got.State.BlockKind != BatchBlockKindResumable {
				t.Fatalf("prerequisite failure reached consumers: preparations=%d gates=%d agents=%d state=%+v err=%v", preparations, gates, agents, got.State, err)
			}
		})
	}
}

func TestBatchUnpinnedSubmodulesDoNotAuthorizeGatesOrRepair(t *testing.T) {
	for _, status := range []string{"-012345 child\n", " 012345 child\n+678901 child/nested\n"} {
		t.Run(status[:1], func(t *testing.T) {
			fixture, state, root := batchReviewFixture(t)
			if err := os.WriteFile(filepath.Join(root, ".gitmodules"), nil, 0o600); err != nil {
				t.Fatal(err)
			}
			service := NewService(fixture.repoRoot, nil)
			service.Runner = func(_ context.Context, _, name string, args []string, stdout, _ io.Writer) error {
				if name == "git" && strings.Join(args, " ") == "submodule update --init --recursive" {
					return nil
				}
				if name == "git" && strings.Join(args, " ") == "submodule status --recursive" {
					_, _ = io.WriteString(stdout, status)
					return nil
				}
				t.Fatal("gate ran without pinned recursive prerequisites")
				return nil
			}
			reviewer := BatchAggregateReviewer{Service: service, Store: &batchReviewTestStore{dir: t.TempDir()}, Agent: batchReviewAgentFunc(func(context.Context, string, string) (string, error) {
				t.Fatal("review/repair agent ran without pinned recursive prerequisites")
				return "", nil
			})}
			got, err := reviewer.Review(context.Background(), state, root, BatchReviewOptions{VerifyCommand: "false"})
			if err == nil || !strings.Contains(err.Error(), "recorded gitlink") || got.State.Attempts.AggregateRework != 0 || got.State.VerificationAttribution != nil || got.State.Verification != nil || got.State.BlockKind != BatchBlockKindResumable {
				t.Fatalf("prerequisite failure authorized verification or repair: state=%+v err=%v", got.State, err)
			}
		})
	}
}

func TestBatchSubmodulesPrepareAcrossReworkAndResume(t *testing.T) {
	fixture, state, root := batchReviewFixture(t)
	if err := os.WriteFile(filepath.Join(root, ".gitmodules"), []byte("# initial\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runRealGit(t, root, "add", ".gitmodules")
	runRealGit(t, root, "commit", "-m", "modules")
	state.IntegrationHead = realGitOutput(t, root, "rev-parse", "HEAD")
	service := NewService(fixture.repoRoot, nil)
	var sequence []string
	service.Runner = func(_ context.Context, _, name string, args []string, _, _ io.Writer) error {
		if name == "git" {
			if strings.Join(args, " ") == "submodule status --recursive" {
				return nil // The fixture has module metadata but no gitlinks.
			}
			if strings.Join(args, " ") != "submodule update --init --recursive" {
				t.Fatalf("unexpected command: %v", args)
			}
			data, err := os.ReadFile(filepath.Join(root, ".gitmodules")) //nolint:gosec // test-owned fixture
			if err != nil {
				return err
			}
			sequence = append(sequence, "prepare:"+strings.TrimSpace(string(data)))
		} else {
			sequence = append(sequence, "gate")
		}
		return nil
	}
	// Resume a recorded rework intent: prepare before the editing session,
	// then prepare the new tree again before its gate and aggregate review.
	state.Review = &BatchReview{Status: "reworking", BaseSHA: state.DefaultStartSHA, HeadSHA: state.IntegrationHead}
	state.Attempts.AggregateRework = 1
	agent := batchSessionAgentFunc(func(_ context.Context, request BatchAgentSessionRequest) (BatchAgentSessionResult, error) {
		if request.Operation == BatchAgentOperationAggregateRework {
			sequence = append(sequence, "rework")
			return BatchAgentSessionResult{Output: batchResolutionJSON("update module metadata")}, os.WriteFile(filepath.Join(root, ".gitmodules"), []byte("# changed\n"), 0o600)
		}
		sequence = append(sequence, "review")
		return BatchAgentSessionResult{Output: reviewJSON("approve", "green", "")}, nil
	})
	got, err := (BatchAggregateReviewer{Service: service, Store: &batchReviewTestStore{dir: t.TempDir()}, Agent: agent}).Review(context.Background(), state, root, BatchReviewOptions{VerifyCommand: "true"})
	want := []string{"prepare:# initial", "rework", "prepare:# changed", "gate", "review"}
	if err != nil || got.State.Status != BatchStatusReadyToLand || !slices.Equal(sequence, want) {
		t.Fatalf("sequence=%v state=%+v err=%v", sequence, got.State, err)
	}
}

func TestBatchSubmoduleBisectionPreparationFailureRestoresWithoutAttribution(t *testing.T) {
	fixture, state, root := batchAttributionFixture(t)
	service := NewService(fixture.repoRoot, nil)
	// Use a Git wrapper to place the test marker after each restore. Production
	// preparation still runs through its real filesystem detection boundary.
	git := &submoduleProbeGit{GitClient: service.NewGit(root), root: root}
	service.NewGit = func(string) GitClient { return git }
	if err := os.WriteFile(filepath.Join(root, ".gitmodules"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	var sequence []string
	gates, agents := 0, 0
	service.Runner = func(_ context.Context, _, name string, args []string, _, _ io.Writer) error {
		if name == "git" && strings.Join(args, " ") == "submodule update --init --recursive" {
			head := realGitOutput(t, root, "rev-parse", "HEAD")
			sequence = append(sequence, head)
			if head != state.IntegrationHead {
				return errors.New("credential unavailable at probe")
			}
			return nil
		}
		if name == "git" && strings.Join(args, " ") == "submodule status --recursive" {
			return nil // The fixture has module metadata but no gitlinks.
		}
		gates++
		return errors.New("aggregate code failure")
	}
	got, err := (BatchAggregateReviewer{Service: service, Store: &batchReviewTestStore{dir: t.TempDir()}, Agent: batchReviewAgentFunc(func(context.Context, string, string) (string, error) { agents++; return "", nil })}).Review(context.Background(), state, root, BatchReviewOptions{VerifyCommand: "false"})
	a := got.State.VerificationAttribution
	if err == nil || !strings.Contains(err.Error(), "submodule") || gates != 1 || agents != 0 || got.State.Attempts.AggregateRework != 0 || a == nil || a.Status != batchAttributionBisecting || a.PlanID != "" || a.GateRuns != 0 || a.ParkedSHA != "" || len(sequence) != 3 || sequence[0] != state.IntegrationHead || sequence[2] != state.IntegrationHead {
		t.Fatalf("sequence=%v gates=%d agents=%d state=%+v attribution=%+v err=%v", sequence, gates, agents, got.State, a, err)
	}
	assertRef(t, root, "HEAD", state.IntegrationHead)
}

type submoduleProbeGit struct {
	GitClient
	root string
}

func (g *submoduleProbeGit) CleanUntracked(ctx context.Context) error {
	if err := g.GitClient.CleanUntracked(ctx); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(g.root, ".gitmodules"), nil, 0o600)
}

func TestBatchReviewPromptUsesConfiguredDiffStat(t *testing.T) {
	t.Parallel()
	state := BatchState{ID: "batch-1", DefaultStartSHA: "base", IntegrationHead: "head"}
	git := &fakeGitClient{diffStat: " feature.go | 4 +++-"}

	prompt, err := (BatchAggregateReviewer{}).renderPrompt(context.Background(), git, state, "go test ./...", "ok")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(prompt, git.diffStat) {
		t.Fatalf("aggregate review prompt omitted configured diff stat:\n%s", prompt)
	}
	if !slices.Contains(git.calls, "diff-stat base..head") {
		t.Fatalf("diff stat call missing: %#v", git.calls)
	}
}

func TestBatchReviewDiffStatFailureBlocksResumably(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	state := BatchState{ID: "batch-1", Status: BatchStatusReviewing, RepoRoot: root, DefaultStartSHA: "base", IntegrationHead: "head"}
	git := &fakeGitClient{
		root:        root,
		revParse:    map[string]string{"HEAD": "head"},
		diffStatErr: errors.New("diff stat unavailable"),
	}
	runner := func(_ context.Context, _ string, _ string, _ []string, _ io.Writer, _ io.Writer) error { return nil }
	reviewer := BatchAggregateReviewer{
		Store:   &batchReviewTestStore{dir: t.TempDir()},
		Service: Service{Git: git, Runner: runner},
		Agent: batchReviewAgentFunc(func(context.Context, string, string) (string, error) {
			return reviewJSON("approve", "green", ""), nil
		}),
	}

	got, err := reviewer.Review(context.Background(), state, root, BatchReviewOptions{VerifyCommand: "true"})
	if err == nil || !strings.Contains(err.Error(), "render aggregate review diff stat for base..head: diff stat unavailable") {
		t.Fatalf("expected contextual diff stat failure, got state=%+v err=%v", got.State, err)
	}
	if got.State.Status != BatchStatusBlocked || got.State.BlockKind != BatchBlockKindResumable {
		t.Fatalf("diff stat failure did not block resumably: %+v", got.State)
	}
	if !slices.Contains(git.calls, "diff-stat base..head") {
		t.Fatalf("diff stat call missing: %#v", git.calls)
	}
}

func TestFinishAggregateReworkStagesDotBeforePreparedCommit(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "reworked.txt"), []byte("fixed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	message, err := aggregateProposedResolutionCommitMessage(plan.ReviewCommitMessage{
		Subject: "fix(batch): resolve aggregate findings",
		Body:    "What:\nResolve the aggregate findings.\n\nWhy:\nKeep the combined batch correct.",
	}, "batch-1", 1)
	if err != nil {
		t.Fatal(err)
	}
	state := BatchState{
		ID: "batch-1", IntegrationHead: "parent", Attempts: BatchAttempts{AggregateRework: 1},
		Review: &BatchReview{Status: "applying", CommitMessage: message, ResolutionPaths: []string{"reworked.txt"}},
	}
	git := &fakeGitClient{
		root:          root,
		status:        "?? reworked.txt\n",
		stagedChanges: true,
		revParse:      map[string]string{"HEAD": "committed-head"},
	}

	got, err := (BatchAggregateReviewer{Store: &batchReviewTestStore{dir: t.TempDir()}}).finishAggregateRework(context.Background(), git, state, root)
	if err != nil {
		t.Fatal(err)
	}
	if got.IntegrationHead != "committed-head" {
		t.Fatalf("integration head = %q, want committed-head", got.IntegrationHead)
	}
	addIndex := slices.Index(git.calls, "add .")
	want := []string{"add .", "has-staged-changes", "commit " + message, "rev-parse HEAD"}
	if addIndex < 0 || !slices.Equal(git.calls[addIndex:], want) {
		t.Fatalf("Add(.) was not followed by the prepared commit sequence: %#v", git.calls)
	}
}

func TestFinishAggregateReworkRefusesEmptyStaging(t *testing.T) {
	t.Parallel()
	fixture, state, root := batchReviewFixture(t)
	if err := os.WriteFile(filepath.Join(root, "reworked.txt"), []byte("fixed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	message, err := aggregateProposedResolutionCommitMessage(plan.ReviewCommitMessage{
		Subject: "fix(batch): resolve aggregate findings",
		Body:    "What:\nResolve aggregate findings.\n\nWhy:\nKeep the combined batch correct.",
	}, state.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	fingerprint, err := aggregateReworkContentFingerprint(root, []string{"reworked.txt"})
	if err != nil {
		t.Fatal(err)
	}
	state.Attempts.AggregateRework = 1
	state.Review = &BatchReview{Status: "applying", CommitMessage: message, ResolutionPaths: []string{"reworked.txt"}, ResolutionFingerprint: fingerprint}
	client, err := NewService(fixture.repoRoot, nil).gitClientForRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	git := &batchPreparedCommitTestGit{GitClient: client, skipAdd: true}
	got, err := (BatchAggregateReviewer{}).finishAggregateRework(context.Background(), git, state, root)
	if !errors.Is(err, commitpkg.ErrNoStagedChanges) || !strings.Contains(err.Error(), "commit aggregate rework") {
		t.Fatalf("expected contextual empty-staging refusal, got %v", err)
	}
	if git.commitCalled || got.Review.Status != "applying" || got.IntegrationHead != state.IntegrationHead {
		t.Fatalf("empty staging committed or settled intent: called=%t state=%+v", git.commitCalled, got)
	}
	assertRef(t, root, "HEAD", state.IntegrationHead)
}

func TestBatchReviewApprovePersistsExactEvidenceAndLeavesDefaultAndSourcesAlone(t *testing.T) {
	t.Parallel()
	fixture, state, root := batchReviewFixture(t)
	sourceReview := filepath.Join(t.TempDir(), "review.md")
	if err := os.WriteFile(sourceReview, []byte("source review"), 0o600); err != nil {
		t.Fatal(err)
	}
	state.Candidates[0].PlanDir = filepath.Dir(sourceReview)
	store := &batchReviewTestStore{dir: t.TempDir()}
	agent := batchSessionAgentFunc(func(_ context.Context, request BatchAgentSessionRequest) (BatchAgentSessionResult, error) {
		if request.BatchID != state.ID || request.Operation != BatchAgentOperationAggregateReview || request.Attempt != 1 || request.CandidatePlanID != "" || request.IntegrationRoot != root {
			t.Fatalf("unexpected aggregate review attribution: %#v", request)
		}
		if !strings.Contains(request.Prompt, state.DefaultStartSHA) || !strings.Contains(request.Prompt, state.IntegrationHead) || !strings.Contains(request.Prompt, state.Candidates[0].SourceTip) {
			t.Fatalf("aggregate packet omitted exact evidence:\n%s", request.Prompt)
		}
		return BatchAgentSessionResult{Output: reviewJSON("approve", "green", "")}, nil
	})
	got, err := (BatchAggregateReviewer{Store: store, Service: NewService(fixture.repoRoot, nil), Agent: agent}).Review(context.Background(), state, root, BatchReviewOptions{VerifyCommand: "true"})
	if err != nil {
		t.Fatal(err)
	}
	if got.State.Status != BatchStatusReadyToLand || got.State.Review == nil || got.State.Review.Verdict != "approve" || got.State.Review.Artifact == "" || !got.State.Verification.Passed {
		t.Fatalf("unexpected approved state: %+v", got.State)
	}
	if data, err := os.ReadFile(sourceReview); err != nil || string(data) != "source review" { //nolint:gosec // test path is rooted in t.TempDir.
		t.Fatalf("source review mutated: %q %v", data, err)
	}
	assertRef(t, fixture.repoRoot, fixture.defaultBranch, state.DefaultStartSHA)
}

func TestBatchReviewAttributesReviewAndReworkAttemptsFromDurableCounters(t *testing.T) {
	t.Parallel()
	fixture, state, root := batchReviewFixture(t)
	var requests []BatchAgentSessionRequest
	agent := batchSessionAgentFunc(func(_ context.Context, request BatchAgentSessionRequest) (BatchAgentSessionResult, error) {
		requests = append(requests, request)
		switch len(requests) {
		case 1:
			return BatchAgentSessionResult{Output: reviewJSON("changes_requested", "fix aggregate", "finding")}, nil
		case 2:
			return BatchAgentSessionResult{Output: batchResolutionJSON("fixed aggregate")}, os.WriteFile(filepath.Join(request.IntegrationRoot, "attributed-rework.txt"), []byte("fixed\n"), 0o600)
		default:
			return BatchAgentSessionResult{Output: reviewJSON("approve", "green", "")}, nil
		}
	})
	got, err := (BatchAggregateReviewer{Store: &batchReviewTestStore{dir: t.TempDir()}, Service: NewService(fixture.repoRoot, nil), Agent: agent}).Review(context.Background(), state, root, BatchReviewOptions{VerifyCommand: "true", MaxAttempts: 1})
	if err != nil || got.State.Status != BatchStatusReadyToLand {
		t.Fatalf("attributed review failed: state=%s err=%v", got.State.Status, err)
	}
	wantOperations := []BatchAgentOperation{BatchAgentOperationAggregateReview, BatchAgentOperationAggregateRework, BatchAgentOperationAggregateReview}
	wantAttempts := []int{1, 1, 2}
	if len(requests) != len(wantOperations) {
		t.Fatalf("requests = %#v", requests)
	}
	for i, request := range requests {
		if request.BatchID != state.ID || request.Operation != wantOperations[i] || request.Attempt != wantAttempts[i] || request.IntegrationRoot != root || request.CandidatePlanID != "" {
			t.Fatalf("request %d attribution = %#v", i, request)
		}
	}
}

func TestBatchReviewRejectsCandidateSourceRefChangesByEveryAgentSession(t *testing.T) {
	t.Parallel()
	addProtectedCandidate := func(t *testing.T, fixture realGitWorktree, state *BatchState) string {
		t.Helper()
		branch := "tao/protected-candidate"
		runRealGit(t, fixture.repoRoot, "branch", branch, state.DefaultStartSHA)
		state.Candidates = append(state.Candidates, BatchCandidate{PlanID: "plan-b", Branch: branch, SourceTip: state.DefaultStartSHA})
		return branch
	}

	t.Run("aggregate review", func(t *testing.T) {
		fixture, state, root := batchReviewFixture(t)
		branch := addProtectedCandidate(t, fixture, &state)
		got, err := (BatchAggregateReviewer{
			Store: &batchReviewTestStore{dir: t.TempDir()}, Service: NewService(fixture.repoRoot, nil),
			Agent: batchReviewAgentFunc(func(_ context.Context, root, _ string) (string, error) {
				runRealGit(t, root, "update-ref", "-d", "refs/heads/"+branch)
				return reviewJSON("approve", "green", ""), nil
			}),
		}).Review(context.Background(), state, root, BatchReviewOptions{VerifyCommand: "true"})
		if err == nil || got.State.Status != BatchStatusBlocked || !strings.Contains(err.Error(), "protected refs") {
			t.Fatalf("expected aggregate review source ref change to block, got state=%#v err=%v", got.State, err)
		}
		assertRef(t, fixture.repoRoot, fixture.defaultBranch, state.DefaultStartSHA)
		assertRef(t, fixture.repoRoot, state.Candidates[0].Branch, state.Candidates[0].SourceTip)
		assertRef(t, fixture.repoRoot, branch, state.DefaultStartSHA)
	})

	t.Run("aggregate rework", func(t *testing.T) {
		fixture, state, root := batchReviewFixture(t)
		branch := addProtectedCandidate(t, fixture, &state)
		calls := 0
		got, err := (BatchAggregateReviewer{
			Store: &batchReviewTestStore{dir: t.TempDir()}, Service: NewService(fixture.repoRoot, nil),
			Agent: batchReviewAgentFunc(func(_ context.Context, root, _ string) (string, error) {
				calls++
				if calls == 1 {
					return reviewJSON("changes_requested", "fix", "finding"), nil
				}
				runRealGit(t, root, "update-ref", "-d", "refs/heads/"+branch)
				return batchResolutionJSON("changed"), os.WriteFile(filepath.Join(root, "reworked.txt"), []byte("fixed\n"), 0o600)
			}),
		}).Review(context.Background(), state, root, BatchReviewOptions{VerifyCommand: "true", MaxAttempts: 1})
		if err == nil || got.State.Status != BatchStatusBlocked || !strings.Contains(err.Error(), "protected Git refs") {
			t.Fatalf("expected aggregate rework source ref change to block, got state=%#v err=%v", got.State, err)
		}
		assertRef(t, fixture.repoRoot, fixture.defaultBranch, state.DefaultStartSHA)
		assertRef(t, fixture.repoRoot, state.Candidates[0].Branch, state.Candidates[0].SourceTip)
		assertRef(t, fixture.repoRoot, branch, state.DefaultStartSHA)
	})
}

func TestBatchReviewRejectedAgentsRestoreCleanResumableWorkspace(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		agent func(*testing.T) BatchResolutionAgent
		want  string
	}{
		{
			name: "aggregate review edits",
			agent: func(t *testing.T) BatchResolutionAgent {
				return batchReviewAgentFunc(func(_ context.Context, root, _ string) (string, error) {
					if err := os.WriteFile(filepath.Join(root, "unexpected.txt"), []byte("review edit\n"), 0o600); err != nil {
						t.Fatal(err)
					}
					return reviewJSON("approve", "green", ""), nil
				})
			},
			want: "modified the integration workspace",
		},
		{
			name: "aggregate rework error with edits",
			agent: func(t *testing.T) BatchResolutionAgent {
				calls := 0
				return batchReviewAgentFunc(func(_ context.Context, root, _ string) (string, error) {
					calls++
					if calls == 1 {
						return reviewJSON("changes_requested", "fix", "finding"), nil
					}
					if err := os.WriteFile(filepath.Join(root, "unexpected.txt"), []byte("rework edit\n"), 0o600); err != nil {
						t.Fatal(err)
					}
					return "partial rework", errors.New("agent failed")
				})
			},
			want: "aggregate rework agent failed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixture := newRealGitWorktree(t)
			state := batchWorkspaceState(t, fixture)
			state.ID = "batch-review-rejected"
			owner, err := NewBatchWorkspace(fixture.repoRoot, filepath.Join(t.TempDir(), "merge-batches"), nil)
			if err != nil {
				t.Fatal(err)
			}
			integration, err := owner.Start(context.Background(), state)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(integration.Path, "combined.txt"), []byte("combined\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			runRealGit(t, integration.Path, "add", ".")
			runRealGit(t, integration.Path, "commit", "-m", "feat: combined")
			head := strings.TrimSpace(realGitOutput(t, integration.Path, "rev-parse", "HEAD"))
			state.Status = BatchStatusReviewing
			state.IntegrationHead = head
			state.Integrations = []BatchIntegration{{PlanID: "plan-a", SourceHead: state.Candidates[0].SourceTip, IntegrationBaseSHA: state.DefaultStartSHA, IntegrationSHA: head, Status: batchIntegrationApplied}}

			store := &batchReviewTestStore{dir: t.TempDir()}
			got, err := (BatchAggregateReviewer{Store: store, Service: NewService(fixture.repoRoot, nil), Agent: tt.agent(t)}).Review(context.Background(), state, integration.Path, BatchReviewOptions{VerifyCommand: "true", MaxAttempts: 1})
			if err == nil || !strings.Contains(err.Error(), tt.want) || got.State.Status != BatchStatusBlocked {
				t.Fatalf("expected rejected agent to block: state=%+v err=%v", got.State, err)
			}
			status, statusErr := realGitOutputAllowFailure(t, integration.Path, "status", "--porcelain")
			if statusErr != nil {
				t.Fatal(statusErr)
			}
			if status != "" {
				t.Fatalf("rejected agent left integration workspace dirty: %q", status)
			}
			if current := strings.TrimSpace(realGitOutput(t, integration.Path, "rev-parse", "HEAD")); current != head {
				t.Fatalf("rejected agent left integration at %s, want %s", current, head)
			}
			if err := owner.ValidateResume(context.Background(), got.State); err != nil {
				t.Fatalf("rejected agent left batch unresumable: %v", err)
			}

			resumed, ok := ResumeBlockedBatch(got.State)
			if !ok {
				t.Fatalf("blocked aggregate review was not resumable: %+v", got.State)
			}
			resumedResult, resumeErr := (BatchAggregateReviewer{
				Store: store, Service: NewService(fixture.repoRoot, nil),
				Agent: batchReviewAgentFunc(func(_ context.Context, root, prompt string) (string, error) {
					if strings.Contains(prompt, "Candidate: aggregate-review") {
						return batchResolutionJSON("fixed"), os.WriteFile(filepath.Join(root, "reworked.txt"), []byte("fixed\n"), 0o600)
					}
					return reviewJSON("approve", "green", ""), nil
				}),
			}).Review(context.Background(), resumed, integration.Path, BatchReviewOptions{VerifyCommand: "true", MaxAttempts: 1})
			if resumeErr != nil || resumedResult.State.Status != BatchStatusReadyToLand {
				t.Fatalf("clean blocked review did not resume: state=%+v err=%v", resumedResult.State, resumeErr)
			}
		})
	}
}

func TestBatchReviewMalformedOrCommentStopsSafely(t *testing.T) {
	t.Parallel()
	for _, output := range []string{"malformed output", reviewJSON("comment", "risk", "")} {
		t.Run(output[:4], func(t *testing.T) {
			fixture, state, root := batchReviewFixture(t)
			store := &batchReviewTestStore{dir: t.TempDir()}
			got, err := (BatchAggregateReviewer{Store: store, Service: NewService(fixture.repoRoot, nil), Agent: batchReviewAgentFunc(func(context.Context, string, string) (string, error) { return output, nil })}).Review(context.Background(), state, root, BatchReviewOptions{VerifyCommand: "true"})
			if err == nil || got.State.Status != BatchStatusBlocked || got.State.Review.Verdict != "comment" {
				t.Fatalf("expected safe comment stop, got %+v, %v", got.State, err)
			}
			assertRef(t, fixture.repoRoot, fixture.defaultBranch, state.DefaultStartSHA)
		})
	}
}

func TestBatchReviewMalformedAggregateProposalRestoresWithoutIntentOrStaging(t *testing.T) {
	t.Parallel()
	valid := plan.ReviewCommitMessage{
		Subject: "fix(batch): resolve aggregate findings",
		Body:    "What:\nResolve the aggregate review findings.\n\nWhy:\nKeep the combined batch correct.",
	}
	tests := []struct {
		name   string
		output string
	}{
		{name: "not json", output: "fixed aggregate issue"},
		{name: "missing proposal", output: `{"summary":"fixed","commit_message":{}}`},
		{name: "reserved trailer", output: batchResolutionJSONWithProposal("fixed", plan.ReviewCommitMessage{Subject: valid.Subject, Body: valid.Body + "\n\nTao-Merge-Batch: forged"})},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixture, state, root := batchReviewFixture(t)
			store := &batchReviewTestStore{dir: t.TempDir()}
			calls := 0
			agent := batchReviewAgentFunc(func(_ context.Context, root, _ string) (string, error) {
				calls++
				if calls == 1 {
					return reviewJSON("changes_requested", "fix", "finding"), nil
				}
				return tt.output, os.WriteFile(filepath.Join(root, "malformed-rework.txt"), []byte("fixed\n"), 0o600)
			})
			got, err := (BatchAggregateReviewer{Store: store, Service: NewService(fixture.repoRoot, nil), Agent: agent}).Review(context.Background(), state, root, BatchReviewOptions{VerifyCommand: "true", MaxAttempts: 1})
			if err == nil || !strings.Contains(err.Error(), "aggregate rework agent returned malformed output") || got.State.Status != BatchStatusBlocked || got.State.BlockKind != BatchBlockKindResumable {
				t.Fatalf("malformed proposal did not resumably block: state=%+v err=%v", got.State, err)
			}
			if got.State.Review == nil || got.State.Review.Status == "applying" || got.State.Review.CommitMessage != "" || len(got.State.Review.ResolutionPaths) != 0 {
				t.Fatalf("malformed proposal created commit intent: %+v", got.State.Review)
			}
			status, statusErr := realGitOutputAllowFailure(t, root, "status", "--porcelain")
			if statusErr != nil {
				t.Fatal(statusErr)
			}
			if status != "" {
				t.Fatalf("malformed proposal left workspace dirty or staged: %q", status)
			}
			for _, persisted := range store.states {
				if persisted.Review != nil && persisted.Review.Status == "applying" {
					t.Fatalf("malformed proposal persisted applying intent: %+v", persisted.Review)
				}
			}
		})
	}
}

func TestBatchReviewReworkPersistsInterruptionStateAndCreatesResolutionCommit(t *testing.T) {
	t.Parallel()
	fixture, state, root := batchReviewFixture(t)
	store := &batchReviewTestStore{dir: t.TempDir()}
	calls := 0
	agent := batchReviewAgentFunc(func(_ context.Context, root, prompt string) (string, error) {
		calls++
		switch calls {
		case 1:
			return reviewJSON("changes_requested", "fix it", "first"), nil
		case 2:
			if !strings.Contains(prompt, "first") {
				t.Fatal("findings missing from rework packet")
			}
			if err := os.WriteFile(filepath.Join(root, "reworked.txt"), []byte("fixed\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			return batchResolutionJSON("fixed aggregate issue"), nil
		default:
			return reviewJSON("approve", "now green", ""), nil
		}
	})
	got, err := (BatchAggregateReviewer{Store: store, Service: NewService(fixture.repoRoot, nil), Agent: agent}).Review(context.Background(), state, root, BatchReviewOptions{VerifyCommand: "true", MaxAttempts: 2})
	if err != nil {
		t.Fatal(err)
	}
	if got.State.Status != BatchStatusReadyToLand || got.State.Attempts.AggregateRework != 1 || len(got.State.Review.ResolutionSHAs) != 1 {
		t.Fatalf("unexpected rework state: %+v", got.State)
	}
	var pending *BatchState
	for i := range store.states {
		candidate := &store.states[i]
		if candidate.Review != nil && candidate.Review.Status == "pending" {
			pending = candidate
			break
		}
	}
	if pending == nil || pending.Verification != nil || pending.Review.HeadSHA != pending.IntegrationHead || pending.Review.Verdict != "" || pending.Review.CompletedAt != "" {
		t.Fatalf("rework did not persist pending evidence for the new head: %#v", pending)
	}
	if drifts := validatePersistedProgress(*pending); len(drifts) != 0 {
		t.Fatalf("interrupted post-rework state cannot resume: %+v", drifts)
	}
	message := strings.TrimSpace(realGitOutput(t, root, "log", "-1", "--format=%B"))
	expected, messageErr := aggregateProposedResolutionCommitMessage(plan.ReviewCommitMessage{
		Subject: "fix(batch): resolve candidate integration",
		Body:    "What:\nResolve the candidate changes in the integration worktree.\n\nWhy:\nPreserve the candidate intent in the combined batch.",
	}, state.ID, 1)
	if messageErr != nil {
		t.Fatal(messageErr)
	}
	if message != expected {
		t.Fatalf("resolution commit message = %q, want exact proposal intent %q", message, expected)
	}
	var applying *BatchReview
	for i := range store.states {
		if review := store.states[i].Review; review != nil && review.Status == "applying" {
			applying = review
			break
		}
	}
	if applying == nil || applying.CommitMessage != expected || !slices.Equal(applying.ResolutionPaths, []string{"reworked.txt"}) || applying.ResolutionFingerprint == "" {
		t.Fatalf("exact aggregate intent was not durable before commit: %+v", applying)
	}
	assertRef(t, fixture.repoRoot, fixture.defaultBranch, state.DefaultStartSHA)
}

func TestAggregateReworkContentFingerprintFramesRegularFileDigest(t *testing.T) {
	t.Parallel()
	paths := []string{"first.txt", "next.txt"}
	var nextFileEncoding strings.Builder
	nextFileEncoding.WriteByte(0)
	writeAggregateFingerprintField(&nextFileEncoding, paths[1])
	writeAggregateFingerprintField(&nextFileEncoding, "regular")
	writeAggregateFingerprintField(&nextFileEncoding, "false")
	separator := nextFileEncoding.String()

	leftRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(leftRoot, paths[0]), []byte("prefix"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(leftRoot, paths[1]), []byte(separator+"suffix"), 0o600); err != nil {
		t.Fatal(err)
	}

	rightRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(rightRoot, paths[0]), []byte("prefix"+separator), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rightRoot, paths[1]), []byte("suffix"), 0o600); err != nil {
		t.Fatal(err)
	}

	leftFingerprint, err := aggregateReworkContentFingerprint(leftRoot, paths)
	if err != nil {
		t.Fatal(err)
	}
	rightFingerprint, err := aggregateReworkContentFingerprint(rightRoot, paths)
	if err != nil {
		t.Fatal(err)
	}
	if leftFingerprint == rightFingerprint {
		t.Fatal("different multi-file contents collided after shifting the next file's encoded metadata")
	}
}

func TestBatchReviewBlocksWhenAggregateReworkContentDriftsAfterIntent(t *testing.T) {
	t.Parallel()
	fixture, state, root := batchReviewFixture(t)
	store := &batchReviewTestStore{dir: t.TempDir()}
	persistedFingerprint := ""
	store.afterTransition = func(persisted BatchState) error {
		if persisted.Review == nil || persisted.Review.Status != "applying" {
			return nil
		}
		persistedFingerprint = persisted.Review.ResolutionFingerprint
		store.afterTransition = nil
		return os.WriteFile(filepath.Join(root, "reworked.txt"), []byte("changed after intent\n"), 0o600)
	}
	calls := 0
	agent := batchReviewAgentFunc(func(_ context.Context, root, _ string) (string, error) {
		calls++
		if calls == 1 {
			return reviewJSON("changes_requested", "fix", "first"), nil
		}
		return batchResolutionJSON("fixed"), os.WriteFile(filepath.Join(root, "reworked.txt"), []byte("proposed\n"), 0o600)
	})

	got, err := (BatchAggregateReviewer{Store: store, Service: NewService(fixture.repoRoot, nil), Agent: agent}).Review(context.Background(), state, root, BatchReviewOptions{VerifyCommand: "true"})
	if err == nil || !strings.Contains(err.Error(), "content drifted from durable intent") || got.State.Status != BatchStatusBlocked {
		t.Fatalf("same-path content drift did not block completion: state=%+v err=%v", got.State, err)
	}
	if persistedFingerprint == "" {
		t.Fatal("applying intent omitted the complete content fingerprint")
	}
	if calls != 2 || got.State.Review == nil || got.State.Review.Status != "reworking" || got.State.Review.CommitMessage != "" || len(got.State.Review.ResolutionPaths) != 0 || got.State.Review.ResolutionFingerprint != "" {
		t.Fatalf("drifted intent was not reset for a fresh proposal: calls=%d review=%+v", calls, got.State.Review)
	}
	status, statusErr := realGitOutputAllowFailure(t, root, "status", "--porcelain")
	if statusErr != nil {
		t.Fatal(statusErr)
	}
	if status != "" {
		t.Fatalf("drifted aggregate edits were not restored: %q", status)
	}
	if head := strings.TrimSpace(realGitOutput(t, root, "rev-parse", "HEAD")); head != state.IntegrationHead {
		t.Fatalf("drifted content was committed under stale intent: head=%s want=%s", head, state.IntegrationHead)
	}
}

func TestBatchReviewRecoveryReproposesSamePathAfterAggregateContentDrift(t *testing.T) {
	t.Parallel()
	fixture, state, root := batchReviewFixture(t)
	state.Attempts.AggregateRework = 1
	oldProposal := plan.ReviewCommitMessage{
		Subject: "fix(batch): apply stale aggregate fix",
		Body:    "What:\nApply the original aggregate fix.\n\nWhy:\nResolve the reviewed finding.",
	}
	oldMessage, err := aggregateProposedResolutionCommitMessage(oldProposal, state.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	path := "reworked.txt"
	if err := os.WriteFile(filepath.Join(root, path), []byte("original proposal\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fingerprint, err := aggregateReworkContentFingerprint(root, []string{path})
	if err != nil {
		t.Fatal(err)
	}
	state.Review = &BatchReview{
		Status: "applying", Verdict: plan.ReviewVerdictChangesRequested, Summary: "fix",
		Findings: reviewcontract.Parse(reviewJSON("changes_requested", "fix", "first"), reviewcontract.CommitProposalOptional).Findings,
		BaseSHA:  state.DefaultStartSHA, HeadSHA: state.IntegrationHead, Attempts: 1,
		CommitMessage: oldMessage, ResolutionPaths: []string{path}, ResolutionFingerprint: fingerprint,
	}
	if err := os.WriteFile(filepath.Join(root, path), []byte("modified after interruption\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	freshProposal := plan.ReviewCommitMessage{
		Subject: "fix(batch): apply fresh aggregate fix",
		Body:    "What:\nApply the replacement aggregate fix.\n\nWhy:\nPrevent stale commit intent reuse.",
	}
	calls := 0
	agent := batchReviewAgentFunc(func(_ context.Context, agentRoot, prompt string) (string, error) {
		calls++
		if calls == 1 {
			if !strings.Contains(prompt, "Candidate: aggregate-review") {
				t.Fatalf("recovery did not request a fresh aggregate proposal:\n%s", prompt)
			}
			status, statusErr := realGitOutputAllowFailure(t, agentRoot, "status", "--porcelain")
			if statusErr != nil {
				t.Fatal(statusErr)
			}
			if status != "" {
				t.Fatalf("recovery did not restore drifted edits before reproposal: %q", status)
			}
			return batchResolutionJSONWithProposal("fresh fix", freshProposal), os.WriteFile(filepath.Join(agentRoot, path), []byte("fresh proposal\n"), 0o600)
		}
		return reviewJSON("approve", "green", ""), nil
	})
	got, err := (BatchAggregateReviewer{Store: &batchReviewTestStore{dir: t.TempDir()}, Service: NewService(fixture.repoRoot, nil), Agent: agent}).Review(context.Background(), state, root, BatchReviewOptions{VerifyCommand: "true"})
	if err != nil || got.State.Status != BatchStatusReadyToLand || calls != 2 {
		t.Fatalf("drift recovery did not finish with a fresh proposal: calls=%d state=%+v err=%v", calls, got.State, err)
	}
	message := strings.TrimSpace(realGitOutput(t, root, "log", "-1", "--format=%B"))
	if !strings.HasPrefix(message, freshProposal.Subject+"\n\n") || strings.Contains(message, oldProposal.Subject) {
		t.Fatalf("recovery reused stale proposal instead of fresh proposal: %q", message)
	}
	content, readErr := os.ReadFile(filepath.Join(root, path)) //nolint:gosec // test path is rooted in t.TempDir.
	if readErr != nil || string(content) != "fresh proposal\n" {
		t.Fatalf("fresh aggregate content was not committed: content=%q err=%v", content, readErr)
	}
}

func TestBatchReviewAggregateReworkConflictMarkerConfinement(t *testing.T) {
	t.Parallel()
	markerContent := []byte("before\n" + strings.Repeat("<", 7) + " HEAD\nours\n=======\ntheirs\n" + strings.Repeat(">", 7) + " branch\nafter\n")

	t.Run("changed file blocks", func(t *testing.T) {
		fixture, state, root := batchReviewFixture(t)
		originalHead := state.IntegrationHead
		calls := 0
		agent := batchReviewAgentFunc(func(_ context.Context, root, _ string) (string, error) {
			calls++
			switch calls {
			case 1:
				return reviewJSON("changes_requested", "fix conflict", "remove marker"), nil
			case 2:
				return batchResolutionJSON("attempted conflict fix"), os.WriteFile(filepath.Join(root, "marker-rework.txt"), markerContent, 0o600)
			default:
				t.Fatalf("unexpected agent call %d", calls)
				return "", nil
			}
		})

		got, err := (BatchAggregateReviewer{
			Store: &batchReviewTestStore{dir: t.TempDir()}, Service: NewService(fixture.repoRoot, nil), Agent: agent,
		}).Review(context.Background(), state, root, BatchReviewOptions{VerifyCommand: "true", MaxAttempts: 1})
		if err == nil || got.State.Status != BatchStatusBlocked || got.State.BlockKind != BatchBlockKindResumable {
			t.Fatalf("conflict marker did not create a resumable block: state=%+v err=%v", got.State, err)
		}
		for _, want := range []string{"aggregate rework agent left conflict markers", "marker-rework.txt"} {
			if !strings.Contains(got.State.BlockedReason, want) {
				t.Fatalf("block reason %q does not name %q", got.State.BlockedReason, want)
			}
		}
		status, statusErr := realGitOutputAllowFailure(t, root, "status", "--porcelain")
		if statusErr != nil {
			t.Fatal(statusErr)
		}
		if status != "" {
			t.Fatalf("blocked marker edit was not restored: %q", status)
		}
		if head := strings.TrimSpace(realGitOutput(t, root, "rev-parse", "HEAD")); head != originalHead {
			t.Fatalf("blocked marker edit moved HEAD to %s, want %s", head, originalHead)
		}
	})

	t.Run("nested quoted untracked directory blocks", func(t *testing.T) {
		fixture, state, root := batchReviewFixture(t)
		originalHead := state.IntegrationHead
		markerPath := filepath.Join("new marker dir", "deeper", "marker file.txt")
		calls := 0
		agent := batchReviewAgentFunc(func(_ context.Context, root, _ string) (string, error) {
			calls++
			switch calls {
			case 1:
				return reviewJSON("changes_requested", "fix nested conflict", "remove marker"), nil
			case 2:
				if err := os.MkdirAll(filepath.Join(root, filepath.Dir(markerPath)), 0o700); err != nil {
					return "", err
				}
				return batchResolutionJSON("attempted nested conflict fix"), os.WriteFile(filepath.Join(root, markerPath), markerContent, 0o600)
			default:
				return reviewJSON("approve", "marker was missed", ""), nil
			}
		})

		got, err := (BatchAggregateReviewer{
			Store: &batchReviewTestStore{dir: t.TempDir()}, Service: NewService(fixture.repoRoot, nil), Agent: agent,
		}).Review(context.Background(), state, root, BatchReviewOptions{VerifyCommand: "true", MaxAttempts: 1})
		if err == nil || got.State.Status != BatchStatusBlocked || got.State.BlockKind != BatchBlockKindResumable || calls != 2 {
			t.Fatalf("nested conflict marker did not create a resumable block: calls=%d state=%+v err=%v", calls, got.State, err)
		}
		for _, want := range []string{"aggregate rework agent left conflict markers", filepath.ToSlash(markerPath)} {
			if !strings.Contains(got.State.BlockedReason, want) {
				t.Fatalf("block reason %q does not name %q", got.State.BlockedReason, want)
			}
		}
		status, statusErr := realGitOutputAllowFailure(t, root, "status", "--porcelain")
		if statusErr != nil {
			t.Fatal(statusErr)
		}
		if status != "" {
			t.Fatalf("blocked nested marker edit was not restored: %q", status)
		}
		if head := strings.TrimSpace(realGitOutput(t, root, "rev-parse", "HEAD")); head != originalHead {
			t.Fatalf("blocked nested marker edit moved HEAD to %s, want %s", head, originalHead)
		}
	})

	t.Run("unchanged file does not block", func(t *testing.T) {
		fixture, state, root := batchReviewFixture(t)
		markerPath := filepath.Join(root, "unchanged-marker.txt")
		if err := os.WriteFile(markerPath, markerContent, 0o600); err != nil {
			t.Fatal(err)
		}
		runRealGit(t, root, "add", "unchanged-marker.txt")
		runRealGit(t, root, "commit", "-m", "test: add unchanged marker fixture")
		fixtureHead := strings.TrimSpace(realGitOutput(t, root, "rev-parse", "HEAD"))
		state.IntegrationHead = fixtureHead
		state.Candidates[0].SourceTip = fixtureHead
		state.Candidates[0].ReviewHead = fixtureHead
		state.Integrations[0].SourceHead = fixtureHead
		state.Integrations[0].IntegrationSHA = fixtureHead

		calls := 0
		agent := batchReviewAgentFunc(func(_ context.Context, root, _ string) (string, error) {
			calls++
			switch calls {
			case 1:
				return reviewJSON("changes_requested", "fix aggregate issue", "finding"), nil
			case 2:
				return batchResolutionJSON("fixed aggregate issue"), os.WriteFile(filepath.Join(root, "safe-rework.txt"), []byte("fixed\n"), 0o600)
			default:
				return reviewJSON("approve", "green", ""), nil
			}
		})

		got, err := (BatchAggregateReviewer{
			Store: &batchReviewTestStore{dir: t.TempDir()}, Service: NewService(fixture.repoRoot, nil), Agent: agent,
		}).Review(context.Background(), state, root, BatchReviewOptions{VerifyCommand: "true", MaxAttempts: 1})
		if err != nil || got.State.Status != BatchStatusReadyToLand || calls != 3 {
			t.Fatalf("unchanged marker blocked safe rework: calls=%d state=%+v err=%v", calls, got.State, err)
		}
		if content, readErr := os.ReadFile(markerPath); readErr != nil || string(content) != string(markerContent) { //nolint:gosec // test path is rooted in t.TempDir.
			t.Fatalf("unchanged marker fixture was modified: %q err=%v", content, readErr)
		}
	})
}

func TestBatchReviewResumeDoesNotCountInterruptedMetadataAsAnotherConvergenceRound(t *testing.T) {
	t.Parallel()
	fixture, state, root := batchEjectTestFixture(t)
	artifactDir := t.TempDir()
	interruptedStore := &batchReviewTestStore{dir: artifactDir}
	interruptedStore.failAt = 4
	reviewCalls := 0
	reworkCalls := 0
	agent := batchReviewAgentFunc(func(_ context.Context, root, prompt string) (string, error) {
		if strings.Contains(prompt, "Candidate: aggregate-review") {
			reworkCalls++
			return batchResolutionJSON("fixed interrupted finding"), os.WriteFile(filepath.Join(root, "interrupted-review-fix.txt"), []byte("fixed\n"), 0o600)
		}
		reviewCalls++
		if reviewCalls == 3 {
			return reviewJSON("approve", "green after rework", ""), nil
		}
		output := reviewJSON("changes_requested", "fix plan a", fmt.Sprintf("round %d", reviewCalls))
		return strings.Replace(output, `"file":"combined.txt"`, `"file":"plan-a.txt"`, 1), nil
	})
	reviewer := BatchAggregateReviewer{Store: interruptedStore, Service: NewService(fixture.repoRoot, nil), Agent: agent}
	_, err := reviewer.Review(context.Background(), state, root, BatchReviewOptions{VerifyCommand: "true", MaxAttempts: 3, AutoEject: true})
	if err == nil || !strings.Contains(err.Error(), "persist aggregate rework intent") || len(interruptedStore.states) != 3 {
		t.Fatalf("expected interruption after review metadata, got states=%d err=%v", len(interruptedStore.states), err)
	}
	interrupted := interruptedStore.states[len(interruptedStore.states)-1]
	if interrupted.Review == nil || interrupted.Review.Verdict != plan.ReviewVerdictChangesRequested || len(interrupted.Attempts.ReviewHistory) != 1 || interrupted.Attempts.ReviewHistory[0].HeadSHA != interrupted.IntegrationHead {
		t.Fatalf("interruption did not retain exact-head review metadata: %+v", interrupted)
	}
	// The recording store retains pointers that the failed post-persist mutation
	// aliases. A disk reload retains the preceding completed status.
	persistedReview := *interrupted.Review
	persistedReview.Status = "completed"
	interrupted.Review = &persistedReview

	resumeStore := &batchReviewTestStore{dir: artifactDir}
	reviewer.Store = resumeStore
	got, err := reviewer.Review(context.Background(), interrupted, root, BatchReviewOptions{VerifyCommand: "true", MaxAttempts: 3, AutoEject: true})
	if err != nil {
		t.Fatal(err)
	}
	if got.State.Status != BatchStatusReadyToLand || got.State.Ejection != nil || reviewCalls != 3 || reworkCalls != 1 || got.State.Attempts.AggregateRework != 1 {
		t.Fatalf("resume treated repeated head as convergence: reviews=%d reworks=%d state=%+v", reviewCalls, reworkCalls, got.State)
	}
	var retriedMetadata *BatchState
	for i := range resumeStore.states {
		candidate := &resumeStore.states[i]
		if candidate.Review != nil && candidate.Review.Artifact == "aggregate-review-002.md" {
			retriedMetadata = candidate
			break
		}
	}
	if retriedMetadata == nil || len(retriedMetadata.Attempts.ReviewHistory) != 1 || retriedMetadata.Attempts.ReviewHistory[0].HeadSHA != interrupted.IntegrationHead {
		t.Fatalf("retried metadata did not replace the interrupted head: %#v", retriedMetadata)
	}
}

func TestBatchReviewResumeAfterEquivalentFingerprintMetadataRemainsStalled(t *testing.T) {
	t.Parallel()
	fixture, state, root := batchEjectTestFixture(t)
	store := &batchReviewTestStore{dir: t.TempDir()}
	interruption := errors.New("interrupted after equivalent review metadata")
	reviewCalls := 0
	reworkCalls := 0
	agent := batchReviewAgentFunc(func(_ context.Context, root, prompt string) (string, error) {
		if strings.Contains(prompt, "Candidate: aggregate-review") {
			reworkCalls++
			return batchResolutionJSON("attempted fix"), os.WriteFile(filepath.Join(root, "equivalent-fingerprint-fix.txt"), []byte("fixed\n"), 0o600)
		}
		reviewCalls++
		output := reviewJSON("changes_requested", "same finding remains", "equivalent finding")
		return strings.Replace(output, `"file":"combined.txt"`, `"file":"plan-a.txt"`, 1), nil
	})
	store.afterTransition = func(persisted BatchState) error {
		if persisted.Review != nil && persisted.Review.Status == "completed" && latestDistinctReviewFingerprintsEquivalent(persisted.Attempts.ReviewHistory) {
			store.afterTransition = nil
			return interruption
		}
		return nil
	}
	reviewer := BatchAggregateReviewer{Store: store, Service: NewService(fixture.repoRoot, nil), Agent: agent}
	_, err := reviewer.Review(context.Background(), state, root, BatchReviewOptions{VerifyCommand: "true", MaxAttempts: 3, AutoEject: true})
	if !errors.Is(err, interruption) || reviewCalls != 2 || reworkCalls != 1 {
		t.Fatalf("expected interruption after second equivalent review: reviews=%d reworks=%d err=%v", reviewCalls, reworkCalls, err)
	}
	interrupted := store.states[len(store.states)-1]
	if interrupted.Status != BatchStatusReviewing || interrupted.Ejection != nil || len(interrupted.Attempts.ReviewHistory) != 2 {
		t.Fatalf("equivalent metadata was not durably recorded before interruption: %+v", interrupted)
	}

	got, err := reviewer.Review(context.Background(), interrupted, root, BatchReviewOptions{VerifyCommand: "true", MaxAttempts: 3, AutoEject: true})
	if err == nil || !strings.Contains(err.Error(), "aggregate review stalled on equivalent findings") {
		t.Fatalf("resume did not preserve equivalent-fingerprint stall: state=%+v err=%v", got.State, err)
	}
	if got.State.Status != BatchStatusBlocked || got.State.Ejection != nil || reviewCalls != 3 || reworkCalls != 1 {
		t.Fatalf("resume ejected or reworked after equivalent-fingerprint stall: reviews=%d reworks=%d state=%+v", reviewCalls, reworkCalls, got.State)
	}
}

func TestBatchReviewResumesAggregateReworkAfterCommitTransitionFailure(t *testing.T) {
	t.Parallel()
	fixture, state, root := batchReviewFixture(t)
	store := &batchReviewTestStore{dir: t.TempDir()}
	store.failAt = 6
	calls := 0
	agent := batchReviewAgentFunc(func(_ context.Context, root, _ string) (string, error) {
		calls++
		if calls == 1 {
			return reviewJSON("changes_requested", "fix", "first"), nil
		}
		if calls == 2 {
			return batchResolutionJSON("fixed"), os.WriteFile(filepath.Join(root, "reworked.txt"), []byte("fixed\n"), 0o600)
		}
		return reviewJSON("approve", "green", ""), nil
	})
	reviewer := BatchAggregateReviewer{Store: store, Service: NewService(fixture.repoRoot, nil), Agent: agent}
	_, err := reviewer.Review(context.Background(), state, root, BatchReviewOptions{VerifyCommand: "true"})
	if err == nil || !strings.Contains(err.Error(), "persist aggregate rework commit") {
		t.Fatalf("expected post-commit transition failure, got %v", err)
	}
	intent := store.states[len(store.states)-1]
	if intent.Review == nil || intent.Review.Status != "applying" || intent.IntegrationHead == strings.TrimSpace(realGitOutput(t, root, "rev-parse", "HEAD")) {
		t.Fatalf("missing durable aggregate commit intent: %+v", intent)
	}

	resumeStore := &batchReviewTestStore{dir: t.TempDir()}
	reviewer.Store = resumeStore
	got, err := reviewer.Review(context.Background(), intent, root, BatchReviewOptions{VerifyCommand: "true"})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 3 || got.State.Status != BatchStatusReadyToLand || len(got.State.Review.ResolutionSHAs) != 1 {
		t.Fatalf("resume did not settle before review: calls=%d state=%+v", calls, got.State)
	}
	if first := resumeStore.states[0]; first.Review == nil || first.Review.Status != "pending" || first.IntegrationHead != strings.TrimSpace(realGitOutput(t, root, "rev-parse", "HEAD")) {
		t.Fatalf("first resume transition did not settle commit: %+v", first)
	}
}

func TestBatchReviewRecoversPersistedAggregateCommitIntent(t *testing.T) {
	t.Parallel()
	fixture, state, root := batchReviewFixture(t)
	parent := state.IntegrationHead
	state.Attempts.AggregateRework = 1
	state.Review = &BatchReview{Status: "applying", BaseSHA: state.DefaultStartSHA, HeadSHA: parent, Attempts: 1}
	if err := os.WriteFile(filepath.Join(root, "interrupted.txt"), []byte("fixed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runRealGit(t, root, "add", ".")
	runRealGit(t, root, "commit", "-m", aggregateResolutionCommitMessage(state.ID, 1))
	committedHead := strings.TrimSpace(realGitOutput(t, root, "rev-parse", "HEAD"))

	store := &batchReviewTestStore{dir: t.TempDir()}
	got, err := (BatchAggregateReviewer{
		Store: store, Service: NewService(fixture.repoRoot, nil),
		Agent: batchReviewAgentFunc(func(context.Context, string, string) (string, error) {
			return reviewJSON("approve", "green", ""), nil
		}),
	}).Review(context.Background(), state, root, BatchReviewOptions{VerifyCommand: "true"})
	if err != nil {
		t.Fatal(err)
	}
	if got.State.Status != BatchStatusReadyToLand || got.State.IntegrationHead != committedHead || len(got.State.Review.ResolutionSHAs) != 1 {
		t.Fatalf("interrupted commit was not recovered: %+v", got.State)
	}
	if first := store.states[0]; first.Review.Status != "pending" || first.IntegrationHead != committedHead || first.Verification != nil {
		t.Fatalf("recovery was not settled before verification: %+v", first)
	}
}

func TestBatchReviewRecoversInterruptedAggregateReworkBeforeRetry(t *testing.T) {
	t.Parallel()
	for _, status := range []string{"reworking", "applying"} {
		t.Run(status, func(t *testing.T) {
			_, state, root := batchReviewFixture(t)
			state.Attempts.AggregateRework = 1
			state.Review = &BatchReview{
				Status: status, Verdict: plan.ReviewVerdictChangesRequested, Summary: "fix",
				Findings: reviewcontract.Parse(reviewJSON("changes_requested", "fix", "interrupted"), reviewcontract.CommitProposalOptional).Findings,
				BaseSHA:  state.DefaultStartSHA, HeadSHA: state.IntegrationHead, Attempts: 1,
			}
			state.Verification = &BatchVerification{Command: "true", HeadSHA: state.IntegrationHead, Passed: true, Output: "passed", CompletedAt: time.Now().UTC().Format(time.RFC3339Nano)}
			if err := os.WriteFile(filepath.Join(root, "partial.txt"), []byte("partial\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			runRealGit(t, root, "add", ".")

			calls := 0
			agent := batchReviewAgentFunc(func(_ context.Context, agentRoot, prompt string) (string, error) {
				calls++
				if calls == 1 {
					got, statusErr := realGitOutputAllowFailure(t, agentRoot, "status", "--porcelain")
					if statusErr != nil {
						t.Fatal(statusErr)
					}
					if got != "" {
						t.Fatalf("interrupted edits were not removed before retry: %q", got)
					}
					if got := strings.TrimSpace(realGitOutput(t, agentRoot, "rev-parse", "HEAD")); got != state.IntegrationHead {
						t.Fatalf("retry head = %s, want recorded %s", got, state.IntegrationHead)
					}
					if !strings.Contains(prompt, "Candidate: aggregate-review") {
						t.Fatalf("resume reran review instead of interrupted rework:\n%s", prompt)
					}
					return batchResolutionJSON("fixed"), os.WriteFile(filepath.Join(agentRoot, "fixed.txt"), []byte("fixed\n"), 0o600)
				}
				return reviewJSON("approve", "green", ""), nil
			})
			got, err := (BatchAggregateReviewer{Store: &batchReviewTestStore{dir: t.TempDir()}, Service: NewService(state.RepoRoot, nil), Agent: agent}).Review(context.Background(), state, root, BatchReviewOptions{VerifyCommand: "true", MaxAttempts: 2})
			if err != nil || got.State.Status != BatchStatusReadyToLand || calls != 2 {
				t.Fatalf("interrupted %s rework did not recover: calls=%d state=%+v err=%v", status, calls, got.State, err)
			}
		})
	}
}

func TestBatchReviewRecoversAggregateReworkCommitFailure(t *testing.T) {
	t.Parallel()
	_, state, root := batchReviewFixture(t)
	hooks := t.TempDir()
	hook := filepath.Join(hooks, "pre-commit")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\nexit 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(hook, 0o700); err != nil { //nolint:gosec // G302: test hook must be executable.
		t.Fatal(err)
	}
	runRealGit(t, root, "config", "core.hooksPath", hooks)
	calls := 0
	agent := batchReviewAgentFunc(func(_ context.Context, agentRoot, _ string) (string, error) {
		calls++
		if calls == 1 {
			return reviewJSON("changes_requested", "fix", "first"), nil
		}
		if calls == 2 {
			return batchResolutionJSON("first fix"), os.WriteFile(filepath.Join(agentRoot, "failed.txt"), []byte("failed\n"), 0o600)
		}
		if calls == 3 {
			got, statusErr := realGitOutputAllowFailure(t, agentRoot, "status", "--porcelain")
			if statusErr != nil {
				t.Fatal(statusErr)
			}
			if got != "" {
				t.Fatalf("recovered commit left workspace dirty before review: %q", got)
			}
			return reviewJSON("approve", "green", ""), nil
		}
		t.Fatalf("unexpected follow-up agent call %d", calls)
		return "", nil
	})
	store := &batchReviewTestStore{dir: t.TempDir()}
	reviewer := BatchAggregateReviewer{Store: store, Service: NewService(state.RepoRoot, nil), Agent: agent}
	blocked, err := reviewer.Review(context.Background(), state, root, BatchReviewOptions{VerifyCommand: "true", MaxAttempts: 2})
	if err == nil || !strings.Contains(err.Error(), "commit aggregate rework") || blocked.State.Review.Status != "applying" {
		t.Fatalf("expected durable failed commit intent, got state=%+v err=%v", blocked.State, err)
	}
	runRealGit(t, root, "config", "--unset", "core.hooksPath")
	resumed, ok := ResumeBlockedBatch(blocked.State)
	if !ok {
		t.Fatal("commit failure was not resumable")
	}
	got, err := reviewer.Review(context.Background(), resumed, root, BatchReviewOptions{VerifyCommand: "true", MaxAttempts: 2})
	if err != nil || got.State.Status != BatchStatusReadyToLand || calls != 3 || len(got.State.Review.ResolutionSHAs) != 1 {
		t.Fatalf("failed commit did not recover without another rework call: calls=%d state=%+v err=%v", calls, got.State, err)
	}
}

func TestAggregateReviewConvergenceSequences(t *testing.T) {
	t.Parallel()
	finding := func(files ...string) []plan.ReviewFinding {
		result := make([]plan.ReviewFinding, len(files))
		for i, file := range files {
			result[i] = plan.ReviewFinding{File: file, Message: "finding"}
		}
		return result
	}

	t.Run("recurring file", func(t *testing.T) {
		history, got := updateAggregateReviewHistory(nil, "head-1", finding("internal/workspace/cleanup.go"), 2)
		if got.NotConverging {
			t.Fatalf("first round unexpectedly tripped: %+v", got)
		}
		_, got = updateAggregateReviewHistory(history, "head-2", finding("internal/workspace/cleanup.go", "other.go"), 2)
		if !got.NotConverging || len(got.Files) != 1 || got.Files[0] != "internal/workspace/cleanup.go" {
			t.Fatalf("recurring file result = %+v", got)
		}
	})

	t.Run("non-decreasing count", func(t *testing.T) {
		history, _ := updateAggregateReviewHistory(nil, "head-1", finding("first.go"), 2)
		_, got := updateAggregateReviewHistory(history, "head-2", finding("second.go"), 2)
		if !got.NotConverging || !got.AllFindingsHaveFiles || !slices.Equal(got.Files, []string{"first.go", "second.go"}) {
			t.Fatalf("non-decreasing result = %+v", got)
		}
	})

	t.Run("non-decreasing count with missing file", func(t *testing.T) {
		history, _ := updateAggregateReviewHistory(nil, "head-1", finding(""), 2)
		_, got := updateAggregateReviewHistory(history, "head-2", finding("plan-b.go"), 2)
		if !got.NotConverging || got.AllFindingsHaveFiles || !slices.Equal(got.Files, []string{"plan-b.go"}) {
			t.Fatalf("missing-file result = %+v", got)
		}
	})

	t.Run("repeated head replaces interrupted round", func(t *testing.T) {
		history, _ := updateAggregateReviewHistory(nil, "head-1", finding("first.go"), 2)
		firstFingerprint := history[0].Fingerprint
		history, got := updateAggregateReviewHistory(history, "head-1", finding("second.go"), 2)
		if got.NotConverging || len(history) != 1 || history[0].HeadSHA != "head-1" || history[0].Fingerprint == "" || history[0].Fingerprint == firstFingerprint || !slices.Equal(history[0].FindingFiles, []string{"second.go"}) {
			t.Fatalf("repeated-head result = history=%+v convergence=%+v", history, got)
		}
	})

	t.Run("equivalent fingerprints survive repeated latest head", func(t *testing.T) {
		history, _ := updateAggregateReviewHistory(nil, "head-1", finding("same.go"), 3)
		history, _ = updateAggregateReviewHistory(history, "head-2", finding("same.go"), 3)
		history, _ = updateAggregateReviewHistory(history, "head-2", finding("same.go"), 3)
		if !latestDistinctReviewFingerprintsEquivalent(history) || len(history) != 2 {
			t.Fatalf("equivalent distinct heads were not retained: %+v", history)
		}
	})

	t.Run("shrinking findings", func(t *testing.T) {
		history, got := updateAggregateReviewHistory(nil, "head-1", finding("a.go", "b.go", "c.go"), 3)
		if got.NotConverging {
			t.Fatalf("first shrinking round tripped: %+v", got)
		}
		history, got = updateAggregateReviewHistory(history, "head-2", finding("d.go", "e.go"), 3)
		if got.NotConverging {
			t.Fatalf("second shrinking round tripped: %+v", got)
		}
		history, got = updateAggregateReviewHistory(history, "head-3", finding("f.go"), 3)
		if got.NotConverging || len(history) != 3 {
			t.Fatalf("shrinking sequence result = history=%+v convergence=%+v", history, got)
		}
	})
}

type aggregateReviewFilesGit struct {
	GitClient
	files map[string][]string
}

func (g aggregateReviewFilesGit) ChangedFiles(_ context.Context, revspec string) ([]string, error) {
	return append([]string(nil), g.files[revspec]...), nil
}

func TestAttributeAggregateReviewFilesRequiresExactlyOneCandidate(t *testing.T) {
	t.Parallel()
	candidates := []BatchCandidate{
		{PlanID: "plan-a", ReviewBase: "base-a", SourceTip: "tip-a"},
		{PlanID: "plan-b", ReviewBase: "base-b", SourceTip: "tip-b"},
	}
	git := aggregateReviewFilesGit{files: map[string][]string{
		"base-a..tip-a": {"internal/workspace/cleanup.go"},
		"base-b..tip-b": {"README.md"},
	}}
	if got := attributeAggregateReviewFiles(context.Background(), git, candidates, []string{"internal/workspace/cleanup.go"}); got != "plan-a" {
		t.Fatalf("unique attribution = %q, want plan-a", got)
	}
	if got := aggregateReviewNonConvergenceReason([]string{"internal/workspace/cleanup.go"}, "plan-a"); got != "aggregate review not converging on internal/workspace/cleanup.go (plan plan-a)" {
		t.Fatalf("attributed reason = %q", got)
	}

	git.files["base-b..tip-b"] = []string{"internal/workspace/cleanup.go"}
	if got := attributeAggregateReviewFiles(context.Background(), git, candidates, []string{"internal/workspace/cleanup.go"}); got != "" {
		t.Fatalf("ambiguous attribution = %q, want empty", got)
	}
}

func TestBatchReviewAutoEjectReworksReducedSetVerificationAndApproves(t *testing.T) {
	t.Parallel()
	fixture, state, root := batchEjectTestFixture(t)
	store := &batchReviewTestStore{dir: t.TempDir()}
	ejectedRef := "refs/heads/" + fixture.planBranch
	reviewCalls := 0
	resolutionCalls := 0
	var reviewOutputs []string
	agent := batchReviewAgentFunc(func(_ context.Context, root, prompt string) (string, error) {
		if strings.Contains(prompt, "Candidate: aggregate-review") {
			return batchResolutionJSON("attempted fix"), os.WriteFile(filepath.Join(root, "review-fix.txt"), []byte("fix\n"), 0o600)
		}
		if strings.Contains(prompt, "Candidate: plan-b") {
			// Verification-sourced aggregate rework names the attributed candidate.
			resolutionCalls++
			// Ejected refs need not survive reduced-set aggregate rework.
			runRealGit(t, root, "update-ref", "-d", ejectedRef)
			return batchResolutionJSON("fixed reduced-set verification"), os.WriteFile(filepath.Join(root, "reduced-fixed.txt"), []byte("fixed\n"), 0o600)
		}
		reviewCalls++
		if reviewCalls == 3 {
			runRealGit(t, root, "update-ref", ejectedRef, state.DefaultStartSHA)
			output := reviewJSON("approve", "reduced batch is green", "")
			reviewOutputs = append(reviewOutputs, output)
			return output, nil
		}
		output := reviewJSON("changes_requested", "plan a keeps failing", "round "+strconv.Itoa(reviewCalls))
		output = strings.Replace(output, `"file":"combined.txt"`, `"file":"plan-a.txt"`, 1)
		if reviewCalls == 2 {
			output = strings.Replace(output, `"severity":"major"`, `"severity":"critical"`, 1)
		}
		reviewOutputs = append(reviewOutputs, output)
		return output, nil
	})

	verifyCommand := "test ! -f plan-b.txt || test -f plan-a.txt || test -f reduced-fixed.txt"
	service := NewService(fixture.repoRoot, nil)
	reviewer := BatchAggregateReviewer{Store: store, Service: service, Agent: agent}
	got, err := reviewer.Review(context.Background(), state, root, BatchReviewOptions{VerifyCommand: verifyCommand, MaxAttempts: 3, AutoEject: true})
	if err != nil {
		t.Fatal(err)
	}
	if got.State.Status != BatchStatusReadyToLand || got.ReenterPhases || got.State.Ejection == nil || got.State.Ejection.PlanID != "plan-a" || got.State.Ejection.Status != batchEjectionCompleted {
		t.Fatalf("auto-eject review state = %+v", got.State)
	}
	if got.State.Candidates[0].Deferred == nil || !strings.Contains(got.State.Candidates[0].Deferred.Reason, "not converging") {
		t.Fatalf("ejected candidate was not attributed: %+v", got.State.Candidates[0])
	}
	if resolutionCalls != 1 {
		t.Fatalf("reduced-set resolution calls = %d, want 1", resolutionCalls)
	}
	assertRef(t, fixture.repoRoot, fixture.planBranch, state.DefaultStartSHA)
	if len(got.State.Integrations) != 1 || got.State.Integrations[0].PlanID != "plan-b" || len(got.State.Review.ResolutionSHAs) != 1 || got.State.Attempts.AggregateRework != 1 || got.State.VerificationAttribution == nil || got.State.VerificationAttribution.PlanID != "plan-b" || got.State.Review.HeadSHA != got.State.IntegrationHead || !got.State.Verification.Passed {
		t.Fatalf("reduced set lacks resolved exact final evidence: %+v", got.State)
	}
	if got.State.AggregateReviewSequence != 3 || got.State.Review.Attempts != 1 || got.State.Review.Artifact != "aggregate-review-003.md" {
		t.Fatalf("review sequences after ejection = artifact %d, attempts %+v, review %+v", got.State.AggregateReviewSequence, got.State.Attempts, got.State.Review)
	}
	for i, want := range reviewOutputs {
		name := fmt.Sprintf("aggregate-review-%03d.md", i+1)
		data, readErr := os.ReadFile(filepath.Join(store.dir, name)) //nolint:gosec // test path is rooted in t.TempDir.
		if readErr != nil || string(data) != want {
			t.Fatalf("%s changed after reduced-set review: got %q, err %v; want %q", name, data, readErr, want)
		}
	}
}

func TestBatchReviewAutoEjectBlocksAttributedNonConvergenceForOnlyCandidate(t *testing.T) {
	t.Parallel()
	fixture, state, root := batchReviewFixture(t)
	store := &batchReviewTestStore{dir: t.TempDir()}
	reviewCalls := 0
	agent := batchReviewAgentFunc(func(_ context.Context, root, prompt string) (string, error) {
		if strings.Contains(prompt, "Candidate: aggregate-review") {
			return batchResolutionJSON("attempted fix"), os.WriteFile(filepath.Join(root, "review-fix.txt"), []byte("fix\n"), 0o600)
		}
		reviewCalls++
		output := reviewJSON("changes_requested", "only candidate keeps failing", "round "+strconv.Itoa(reviewCalls))
		if reviewCalls == 2 {
			output = strings.Replace(output, `"severity":"major"`, `"severity":"critical"`, 1)
		}
		return output, nil
	})

	got, err := (BatchAggregateReviewer{Store: store, Service: NewService(fixture.repoRoot, nil), Agent: agent}).Review(context.Background(), state, root, BatchReviewOptions{VerifyCommand: "true", MaxAttempts: 3, AutoEject: true})
	if err == nil || !strings.Contains(err.Error(), "aggregate review not converging") {
		t.Fatalf("expected attributed non-convergence block, got state=%+v err=%v", got.State, err)
	}
	if got.State.NonConvergence == nil || got.State.NonConvergence.PlanID != "plan-a" || got.State.Ejection != nil {
		t.Fatalf("only candidate should be attributed without ejection: %+v", got.State)
	}
	if got.State.Status != BatchStatusBlocked || got.State.BlockedReason != got.State.NonConvergence.Reason || got.State.ResumeStatus != "" {
		t.Fatalf("non-convergence block was not persisted as terminal: %+v", got.State)
	}
	if got.State.Candidates[0].Deferred != nil || !slices.Equal(got.State.ChosenOrder, []string{"plan-a"}) || len(got.State.Integrations) != 1 {
		t.Fatalf("only-candidate batch was mutated for ejection: %+v", got.State)
	}
}

func TestBatchReviewAutoEjectBlocksAttributedNonConvergenceAfterCompletedEjection(t *testing.T) {
	t.Parallel()
	fixture, state, root := batchEjectTestFixture(t)
	store := &batchReviewTestStore{dir: t.TempDir()}
	planCBranch := "tao/plan-c"
	planCRoot := filepath.Join(filepath.Dir(fixture.repoRoot), "plan-c")
	runRealGit(t, fixture.repoRoot, "worktree", "add", "-b", planCBranch, planCRoot, state.DefaultStartSHA)
	if err := os.WriteFile(filepath.Join(planCRoot, "plan-c.txt"), []byte("c\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runRealGit(t, planCRoot, "add", ".")
	runRealGit(t, planCRoot, "commit", "-m", "feat: plan c")
	planCHead := realGitOutput(t, fixture.repoRoot, "rev-parse", planCBranch)
	state.Candidates = append(state.Candidates, BatchCandidate{
		PlanID: "plan-c", PlanTitle: "Plan C", RepoRoot: fixture.repoRoot, Branch: planCBranch, SourceTip: planCHead,
		ReviewBase: state.DefaultStartSHA, ReviewHead: planCHead, DefaultBranch: fixture.defaultBranch, DefaultStartSHA: state.DefaultStartSHA,
		CommitMessage: testBatchCommitMessage("plan-c", planCHead),
	})
	state.ChosenOrder = append(state.ChosenOrder, "plan-c")
	integrated, err := (BatchIntegrator{Store: store, Service: NewService(fixture.repoRoot, nil)}).Integrate(context.Background(), state, root, BatchIntegrateOptions{VerifyCommand: "true"})
	if err != nil {
		t.Fatal(err)
	}
	ejected, err := (BatchIntegrator{Store: store, Service: NewService(fixture.repoRoot, nil)}).Eject(context.Background(), integrated.State, root, BatchEjectOptions{
		PlanID: "plan-a", Reason: "aggregate review not converging on plan-a.txt (plan plan-a)", VerifyCommand: "true",
	})
	if err != nil {
		t.Fatal(err)
	}
	state = ejected.State

	reviewCalls := 0
	agent := batchReviewAgentFunc(func(_ context.Context, root, prompt string) (string, error) {
		if strings.Contains(prompt, "Candidate: aggregate-review") {
			return batchResolutionJSON("attempted fix"), os.WriteFile(filepath.Join(root, "review-fix.txt"), []byte("fix\n"), 0o600)
		}
		reviewCalls++
		output := reviewJSON("changes_requested", "plan b keeps failing", "round "+strconv.Itoa(reviewCalls))
		output = strings.Replace(output, `"file":"combined.txt"`, `"file":"plan-b.txt"`, 1)
		if reviewCalls == 2 {
			output = strings.Replace(output, `"severity":"major"`, `"severity":"critical"`, 1)
		}
		return output, nil
	})

	got, err := (BatchAggregateReviewer{Store: store, Service: NewService(fixture.repoRoot, nil), Agent: agent}).Review(context.Background(), state, root, BatchReviewOptions{VerifyCommand: "true", MaxAttempts: 3, AutoEject: true})
	if err == nil || !strings.Contains(err.Error(), "aggregate review not converging") {
		t.Fatalf("expected post-ejection non-convergence block, got state=%+v err=%v", got.State, err)
	}
	if got.State.Status != BatchStatusBlocked || got.State.ResumeStatus != "" || got.State.NonConvergence == nil || got.State.NonConvergence.PlanID != "plan-b" {
		t.Fatalf("post-ejection non-convergence was not persisted as terminal: %+v", got.State)
	}
	if got.State.Ejection == nil || got.State.Ejection.PlanID != "plan-a" || got.State.Ejection.Status != batchEjectionCompleted {
		t.Fatalf("completed ejection was replaced: %+v", got.State.Ejection)
	}
	if got.State.Candidates[1].Deferred != nil || !slices.Equal(got.State.ChosenOrder, []string{"plan-b", "plan-c"}) {
		t.Fatalf("second candidate was ejected after completed ejection: %+v", got.State)
	}
}

func TestBatchReviewResumesPendingEjectBeforeReviewing(t *testing.T) {
	t.Parallel()
	fixture, state, root := batchEjectTestFixture(t)
	reason := "aggregate review not converging on plan-a.txt (plan plan-a)"
	interruptedStore := &recordingBatchTransitionStore{failAt: 2}
	_, err := (BatchIntegrator{Store: interruptedStore, Service: NewService(fixture.repoRoot, nil)}).Eject(context.Background(), state, root, BatchEjectOptions{PlanID: "plan-a", Reason: reason, VerifyCommand: "true"})
	if err == nil || len(interruptedStore.states) != 1 {
		t.Fatalf("expected eject interruption after durable intent, got states=%d err=%v", len(interruptedStore.states), err)
	}

	got, err := (BatchAggregateReviewer{
		Store:   &batchReviewTestStore{dir: t.TempDir()},
		Service: NewService(fixture.repoRoot, nil),
		Agent: batchReviewAgentFunc(func(context.Context, string, string) (string, error) {
			return reviewJSON("approve", "reduced batch approved after resume", ""), nil
		}),
	}).Review(context.Background(), interruptedStore.states[0], root, BatchReviewOptions{VerifyCommand: "true", AutoEject: true})
	if err != nil {
		t.Fatal(err)
	}
	if got.State.Status != BatchStatusReadyToLand || got.State.Ejection == nil || got.State.Ejection.Status != batchEjectionCompleted || len(got.State.Integrations) != 1 || got.State.Integrations[0].PlanID != "plan-b" {
		t.Fatalf("pending eject did not resume before review: %+v", got.State)
	}
}

func TestBatchReviewAutoEjectDoesNotEjectNonAttributableNonConvergence(t *testing.T) {
	t.Parallel()
	fixture, state, root := batchEjectTestFixture(t)
	store := &batchReviewTestStore{dir: t.TempDir()}
	reviewCalls := 0
	agent := batchReviewAgentFunc(func(_ context.Context, root, prompt string) (string, error) {
		if strings.Contains(prompt, "Candidate: aggregate-review") {
			return batchResolutionJSON("attempted fix"), os.WriteFile(filepath.Join(root, "review-fix.txt"), []byte("fix\n"), 0o600)
		}
		reviewCalls++
		output := reviewJSON("changes_requested", "unattributed", "round "+strconv.Itoa(reviewCalls))
		output = strings.Replace(output, `"file":"combined.txt"`, `"file":"unowned.txt"`, 1)
		if reviewCalls == 2 {
			output = strings.Replace(output, `"severity":"major"`, `"severity":"critical"`, 1)
		}
		return output, nil
	})

	got, err := (BatchAggregateReviewer{Store: store, Service: NewService(fixture.repoRoot, nil), Agent: agent}).Review(context.Background(), state, root, BatchReviewOptions{VerifyCommand: "true", MaxAttempts: 3, AutoEject: true})
	if err == nil || !strings.Contains(err.Error(), "aggregate review not converging") {
		t.Fatalf("expected non-attributable block, got state=%+v err=%v", got.State, err)
	}
	if got.State.Status != BatchStatusBlocked || got.State.Ejection != nil || got.State.NonConvergence == nil || got.State.NonConvergence.PlanID != "" {
		t.Fatalf("non-attributable batch was ejected: %+v", got.State)
	}
}

func TestBatchReviewAutoEjectDoesNotAttributeCountStallAcrossCandidates(t *testing.T) {
	t.Parallel()
	fixture, state, root := batchEjectTestFixture(t)
	store := &batchReviewTestStore{dir: t.TempDir()}
	reviewCalls := 0
	agent := batchReviewAgentFunc(func(_ context.Context, root, prompt string) (string, error) {
		if strings.Contains(prompt, "Candidate: aggregate-review") {
			return batchResolutionJSON("attempted fix"), os.WriteFile(filepath.Join(root, "review-fix.txt"), []byte("fix\n"), 0o600)
		}
		reviewCalls++
		file := "plan-a.txt"
		if reviewCalls == 2 {
			file = "plan-b.txt"
		}
		output := reviewJSON("changes_requested", "finding moved between plans", "round "+strconv.Itoa(reviewCalls))
		return strings.Replace(output, `"file":"combined.txt"`, `"file":"`+file+`"`, 1), nil
	})

	got, err := (BatchAggregateReviewer{Store: store, Service: NewService(fixture.repoRoot, nil), Agent: agent}).Review(context.Background(), state, root, BatchReviewOptions{VerifyCommand: "true", MaxAttempts: 3, AutoEject: true})
	if err == nil || !strings.Contains(err.Error(), "aggregate review not converging") {
		t.Fatalf("expected count-only non-convergence block, got state=%+v err=%v", got.State, err)
	}
	if got.State.Status != BatchStatusBlocked || got.State.Ejection != nil || got.State.NonConvergence == nil || got.State.NonConvergence.PlanID != "" {
		t.Fatalf("cross-candidate count stall was attributed: %+v", got.State)
	}
	if !slices.Equal(got.State.NonConvergence.Files, []string{"plan-a.txt", "plan-b.txt"}) {
		t.Fatalf("non-convergence files = %v, want complete window", got.State.NonConvergence.Files)
	}
}

func TestBatchReviewAutoEjectDoesNotAttributeCountStallWithMissingFile(t *testing.T) {
	t.Parallel()
	fixture, state, root := batchEjectTestFixture(t)
	store := &batchReviewTestStore{dir: t.TempDir()}
	reviewCalls := 0
	agent := batchReviewAgentFunc(func(_ context.Context, root, prompt string) (string, error) {
		if strings.Contains(prompt, "Candidate: aggregate-review") {
			return batchResolutionJSON("attempted fix"), os.WriteFile(filepath.Join(root, "review-fix.txt"), []byte("fix\n"), 0o600)
		}
		reviewCalls++
		file := ""
		if reviewCalls == 2 {
			file = "plan-b.txt"
		}
		output := reviewJSON("changes_requested", "one finding lacks a file", "round "+strconv.Itoa(reviewCalls))
		return strings.Replace(output, `"file":"combined.txt"`, `"file":"`+file+`"`, 1), nil
	})

	got, err := (BatchAggregateReviewer{Store: store, Service: NewService(fixture.repoRoot, nil), Agent: agent}).Review(context.Background(), state, root, BatchReviewOptions{VerifyCommand: "true", MaxAttempts: 3, AutoEject: true})
	if err == nil || !strings.Contains(err.Error(), "aggregate review not converging") {
		t.Fatalf("expected count-only non-convergence block, got state=%+v err=%v", got.State, err)
	}
	if got.State.Status != BatchStatusBlocked || got.State.Ejection != nil || got.State.NonConvergence == nil || got.State.NonConvergence.PlanID != "" {
		t.Fatalf("missing-file convergence window was attributed: %+v", got.State)
	}
	if !slices.Equal(got.State.NonConvergence.Files, []string{"plan-b.txt"}) {
		t.Fatalf("non-convergence files = %v, want only non-empty file", got.State.NonConvergence.Files)
	}
}

func TestBatchReviewSnapshotLimits(t *testing.T) {
	for _, tt := range []struct {
		name, captured, run string
		explicit, want      int
		wantError           bool
	}{
		{name: "default", want: 5},
		{name: "run ignored", run: "9", want: 5},
		{name: "malformed run ignored", run: "bad", want: 5},
		{name: "run disabled", run: "0", captured: "3", want: 3},
		{name: "negative merge", captured: "-1", wantError: true},
		{name: "captured", captured: "3", want: 3},
		{name: "zero", captured: "0", want: 0},
		{name: "explicit", captured: "3", explicit: 2, want: 2},
		{name: "invalid", captured: "bad", wantError: true},
		{name: "explicit short circuit", captured: "bad", explicit: 2, want: 2},
	} {
		t.Run(tt.name, func(t *testing.T) {
			service := Service{RuntimeEnv: mergeSnapshotWith(map[string]string{runtimeconfig.EnvMergeReviewMaxAttempts: tt.captured, runtimeconfig.EnvMaxReworkAttempts: tt.run})}
			t.Setenv(runtimeconfig.EnvMergeReviewMaxAttempts, "99")
			t.Setenv(runtimeconfig.EnvMaxReworkAttempts, "99")
			got, err := service.batchReviewMaxAttempts(tt.explicit)
			if (err != nil) != tt.wantError || got != tt.want {
				t.Fatalf("attempts = %d, %v; want %d, error=%t", got, err, tt.want, tt.wantError)
			}
		})
	}
	t.Setenv(runtimeconfig.EnvMaxReworkAttempts, "invalid")
	t.Setenv(runtimeconfig.EnvMergeReviewMaxAttempts, "invalid")
	t.Setenv(runtimeconfig.EnvAggregateReviewConvergenceWindow, "invalid")
	if got, err := (Service{}).batchReviewMaxAttempts(0); err != nil || got != defaultBatchReviewMaxAttempts {
		t.Fatalf("nil snapshot attempts = %d, %v", got, err)
	}
	service := Service{RuntimeEnv: mergeSnapshotWith(map[string]string{runtimeconfig.EnvAggregateReviewConvergenceWindow: "4"})}
	if got, err := service.batchReviewConvergenceWindow(); err != nil || got != 4 {
		t.Fatalf("captured convergence = %d, %v", got, err)
	}
	if got, err := (Service{}).batchReviewConvergenceWindow(); err != nil || got != runtimeconfig.DefaultAggregateReviewConvergenceWindow {
		t.Fatalf("nil snapshot convergence = %d, %v", got, err)
	}
}

func TestBatchReviewConvergenceWindowEnvironment(t *testing.T) {
	t.Setenv(runtimeconfig.EnvAggregateReviewConvergenceWindow, "4")
	if got, err := (Service{RuntimeEnv: mergeTestRuntimeEnv()}).batchReviewConvergenceWindow(); err != nil || got != 4 {
		t.Fatalf("batchReviewConvergenceWindow() = %d, %v", got, err)
	}
	t.Setenv(runtimeconfig.EnvAggregateReviewConvergenceWindow, "1")
	if _, err := (Service{RuntimeEnv: mergeTestRuntimeEnv()}).batchReviewConvergenceWindow(); err == nil {
		t.Fatal("expected unsafe convergence window to fail")
	}
}

func TestBatchReviewEquivalentFindingsAndCapExhaustionStop(t *testing.T) {
	tests := []struct {
		name, second      string
		changeFile        bool
		max               int
		want              string
		convergenceWindow string
	}{
		{name: "equivalent", second: "first", max: 2, want: "equivalent"},
		{name: "cap", second: "different", changeFile: true, max: 1, want: "cap exhausted", convergenceWindow: "3"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.convergenceWindow != "" {
				t.Setenv(runtimeconfig.EnvAggregateReviewConvergenceWindow, tt.convergenceWindow)
			}
			fixture, state, root := batchReviewFixture(t)
			store := &batchReviewTestStore{dir: t.TempDir()}
			calls := 0
			agent := batchReviewAgentFunc(func(_ context.Context, root, _ string) (string, error) {
				calls++
				if calls == 2 {
					if err := os.WriteFile(filepath.Join(root, "attempt.txt"), []byte("edit"), 0o600); err != nil {
						t.Fatal(err)
					}
					return batchResolutionJSON("edited"), nil
				}
				message := "first"
				if calls > 2 {
					message = tt.second
				}
				review := reviewJSON("changes_requested", "changes", message)
				if calls > 2 && tt.changeFile {
					review = strings.Replace(review, `"file":"combined.txt"`, `"file":"different.txt"`, 1)
				}
				return review, nil
			})
			service := NewService(fixture.repoRoot, nil)
			service.RuntimeEnv = mergeTestRuntimeEnv()
			got, err := (BatchAggregateReviewer{Store: store, Service: service, Agent: agent}).Review(context.Background(), state, root, BatchReviewOptions{VerifyCommand: "true", MaxAttempts: tt.max})
			if err == nil || !strings.Contains(err.Error(), tt.want) || got.State.Status != BatchStatusBlocked || got.State.Attempts.AggregateRework != 1 {
				t.Fatalf("unexpected bounded stop: %+v %v", got.State, err)
			}
		})
	}
}

func TestBatchReviewNonConvergencePrecedesAttemptCapAndAutoEjects(t *testing.T) {
	t.Parallel()
	fixture, state, root := batchEjectTestFixture(t)
	store := &batchReviewTestStore{dir: t.TempDir()}
	reviewCalls := 0
	agent := batchReviewAgentFunc(func(_ context.Context, root, prompt string) (string, error) {
		if strings.Contains(prompt, "Candidate: aggregate-review") {
			return batchResolutionJSON("attempted fix"), os.WriteFile(filepath.Join(root, "review-fix.txt"), []byte("fix\n"), 0o600)
		}
		reviewCalls++
		if reviewCalls == 3 {
			return reviewJSON("approve", "reduced batch approved", ""), nil
		}
		output := reviewJSON("changes_requested", "plan a keeps failing", "round "+strconv.Itoa(reviewCalls))
		output = strings.Replace(output, `"file":"combined.txt"`, `"file":"plan-a.txt"`, 1)
		if reviewCalls == 2 {
			output = strings.Replace(output, `"severity":"major"`, `"severity":"critical"`, 1)
		}
		return output, nil
	})

	got, err := (BatchAggregateReviewer{Store: store, Service: NewService(fixture.repoRoot, nil), Agent: agent}).Review(context.Background(), state, root, BatchReviewOptions{VerifyCommand: "true", MaxAttempts: 1, AutoEject: true})
	if err != nil {
		t.Fatal(err)
	}
	if got.State.Status != BatchStatusReadyToLand || got.State.Ejection == nil || got.State.Ejection.PlanID != "plan-a" || got.State.Ejection.Status != batchEjectionCompleted {
		t.Fatalf("non-convergence at attempt cap did not auto-eject: %+v", got.State)
	}
	if reviewCalls != 3 || got.State.Candidates[0].Deferred == nil || !strings.Contains(got.State.Candidates[0].Deferred.Reason, "aggregate review not converging") {
		t.Fatalf("non-convergence did not win over attempt cap: calls=%d state=%+v", reviewCalls, got.State)
	}
}

func TestBatchReviewVerificationFailureAfterReworkAndReviewTimeout(t *testing.T) {
	t.Parallel()
	t.Run("verification after rework", func(t *testing.T) {
		fixture, state, root := batchReviewFixture(t)
		store := &batchReviewTestStore{dir: t.TempDir()}
		calls := 0
		agent := batchReviewAgentFunc(func(_ context.Context, root, _ string) (string, error) {
			calls++
			if calls == 1 {
				return reviewJSON("changes_requested", "fix", "first"), nil
			}
			if err := os.WriteFile(filepath.Join(root, "fail"), []byte("x"), 0o600); err != nil {
				t.Fatal(err)
			}
			return batchResolutionJSON("changed"), nil
		})
		reviewer := BatchAggregateReviewer{Store: store, Service: NewService(fixture.repoRoot, nil), Agent: agent}
		got, err := reviewer.Review(context.Background(), state, root, BatchReviewOptions{VerifyCommand: "test ! -f fail"})
		if err == nil || !strings.HasPrefix(err.Error(), "aggregate verification failed: ") || got.State.Status != BatchStatusBlocked || got.State.Verification.Passed {
			t.Fatalf("expected verification stop: %+v %v", got.State, err)
		}
		if got.State.VerificationAttribution != nil || got.State.Review.ReworkSource != "" {
			t.Fatal("review-sourced rework must not bisect or become verification-sourced")
		}
		if got.State.Verification.HeadSHA != got.State.IntegrationHead || got.State.Review.Status != "pending" || got.State.Review.HeadSHA != got.State.IntegrationHead {
			t.Fatalf("verification failure retained stale post-rework evidence: %+v", got.State)
		}
		if drifts := validatePersistedProgress(got.State); len(drifts) != 0 {
			t.Fatalf("post-rework verification failure cannot resume: %+v", drifts)
		}
		resumed, ok := ResumeBlockedBatch(got.State)
		if !ok {
			t.Fatalf("post-rework verification failure was not resumable: %+v", got.State)
		}
		resumedResult, resumeErr := (BatchAggregateReviewer{
			Store: store, Service: NewService(fixture.repoRoot, nil),
			Agent: batchReviewAgentFunc(func(context.Context, string, string) (string, error) {
				return reviewJSON("approve", "fixed", ""), nil
			}),
		}).Review(context.Background(), resumed, root, BatchReviewOptions{VerifyCommand: "true"})
		if resumeErr != nil || resumedResult.State.Status != BatchStatusReadyToLand {
			t.Fatalf("post-rework resume failed: %+v %v", resumedResult.State, resumeErr)
		}
	})
	t.Run("timeout", func(t *testing.T) {
		fixture, state, root := batchReviewFixture(t)
		store := &batchReviewTestStore{dir: t.TempDir()}
		calls := 0
		agent := batchSessionAgentFunc(func(_ context.Context, request BatchAgentSessionRequest) (BatchAgentSessionResult, error) {
			calls++
			if request.Operation != BatchAgentOperationAggregateReview || request.Attempt != 1 || request.BatchID != state.ID {
				t.Fatalf("unexpected timeout attribution: %#v", request)
			}
			return BatchAgentSessionResult{}, context.DeadlineExceeded
		})
		got, err := (BatchAggregateReviewer{Store: store, Service: NewService(fixture.repoRoot, nil), Agent: agent}).Review(context.Background(), state, root, BatchReviewOptions{VerifyCommand: "true", MaxAttempts: 3})
		if err == nil || got.State.Status != BatchStatusBlocked || got.State.Review.Status != "error" || calls != 1 {
			t.Fatalf("expected one-call timeout stop: calls=%d state=%+v err=%v", calls, got.State, err)
		}
	})
}

func TestBatchReviewArtifactPersistenceFailureStopsBeforeRework(t *testing.T) {
	t.Parallel()
	fixture, state, root := batchReviewFixture(t)
	store := &batchReviewTestStore{dir: t.TempDir(), artifactFail: true}
	got, err := (BatchAggregateReviewer{Store: store, Service: NewService(fixture.repoRoot, nil), Agent: batchReviewAgentFunc(func(context.Context, string, string) (string, error) {
		return reviewJSON("changes_requested", "fix", "first"), nil
	})}).Review(context.Background(), state, root, BatchReviewOptions{VerifyCommand: "true"})
	if err == nil || got.State.Status != BatchStatusBlocked || got.State.Attempts.AggregateRework != 0 {
		t.Fatalf("expected persistence stop: %+v %v", got.State, err)
	}
}

func TestBatchReviewVerificationAttribution(t *testing.T) {
	t.Setenv("GOLANGCI_LINT_CACHE", filepath.Join(t.TempDir(), "inherited-cache"))
	for _, fix := range []bool{true, false} {
		t.Run(fmt.Sprintf("fix=%t", fix), func(t *testing.T) {
			fixture, state, _ := batchAttributionFixture(t)
			root := filepath.Join(t.TempDir(), "integration")
			runRealGit(t, fixture.repoRoot, "worktree", "add", "-b", "tao/integration/cache", root, state.IntegrationHead)
			store := &batchReviewTestStore{dir: t.TempDir()}
			service := NewService(fixture.repoRoot, nil)
			runner := cacheCheckingMergeRunner(t, root)
			gateRuns, runsBeforeRework, reworks := 0, 0, 0
			service.Runner = func(ctx context.Context, root, name string, args []string, stdout, stderr io.Writer) error {
				gateRuns++
				return runner(ctx, root, name, args, stdout, stderr)
			}
			command := mergeCacheProbe + "if test -f broken.txt; then echo broken-candidate; exit 1; fi"
			encodedCommand, err := json.Marshal(command)
			if err != nil {
				t.Fatal(err)
			}
			agent := batchSessionAgentFunc(func(_ context.Context, request BatchAgentSessionRequest) (BatchAgentSessionResult, error) {
				if request.Operation == BatchAgentOperationAggregateReview {
					return BatchAgentSessionResult{Output: reviewJSON("approve", "green", "")}, nil
				}
				reworks++
				runsBeforeRework = gateRuns
				for _, material := range []string{"plan-b", "Plan Two", state.Integrations[0].IntegrationSHA, state.Integrations[1].IntegrationSHA, string(encodedCommand), "broken-candidate"} {
					if !strings.Contains(request.Prompt, material) {
						t.Fatalf("rework prompt missing %q:\n%s", material, request.Prompt)
					}
				}
				if fix {
					return BatchAgentSessionResult{Output: batchResolutionJSON("remove broken file")}, os.Remove(filepath.Join(root, "broken.txt"))
				}
				return BatchAgentSessionResult{Output: batchResolutionJSON("attempt repair")}, os.WriteFile(filepath.Join(root, "attempt.txt"), []byte("not fixed\n"), 0o600)
			})
			reviewer := BatchAggregateReviewer{Store: store, Service: service, Agent: agent}
			got, err := reviewer.Review(context.Background(), state, root, BatchReviewOptions{VerifyCommand: command, MaxAttempts: 1})
			if fix {
				if err != nil || got.State.Status != BatchStatusReadyToLand {
					t.Fatalf("verification repair failed: state=%+v err=%v", got.State, err)
				}
			} else if err == nil || !strings.Contains(err.Error(), "aggregate verification failed after 1 verification rework attempts; attributed to plan plan-b") || got.State.BlockKind != BatchBlockKindResumable {
				t.Fatalf("expected attributed cap stop: state=%+v err=%v", got.State, err)
			}
			a := got.State.VerificationAttribution
			if a == nil || a.Status != batchAttributionAttributed || a.PlanID != "plan-b" || a.PassingSHA != state.Integrations[0].IntegrationSHA || a.FailingSHA != state.Integrations[1].IntegrationSHA || a.ParkedSHA != "" || !strings.Contains(a.Output, "broken-candidate") {
				t.Fatalf("incorrect attribution: %+v", a)
			}
			// The last prefix is already known to fail; search the remaining interval.
			probes := 0
			for low, high := 0, len(state.Integrations); high-low > 1; probes++ {
				mid := (low + high) / 2
				if mid < 2 {
					low = mid
				} else {
					high = mid
				}
			}
			if reworks != 1 || a.GateRuns != probes || gateRuns != 1+probes+1 || gateRuns-runsBeforeRework != 1 {
				t.Fatalf("gate budget: runs=%d probes=%d attribution=%+v reworks=%d before=%d", gateRuns, probes, a, reworks, runsBeforeRework)
			}
			assertRef(t, root, "HEAD", got.State.IntegrationHead)
			assertRef(t, fixture.repoRoot, fixture.defaultBranch, state.DefaultStartSHA)
			requireNoMergeCache(t, fixture.repoRoot, fixture.worktreePath)
			if status := realGitOutput(t, root, "-c", "core.excludesFile="+os.DevNull, "status", "--porcelain", "--untracked-files=all"); status != "" {
				t.Fatalf("cache dirtied integration worktree: %s", status)
			}
			if fix && (got.State.Review == nil || got.State.Review.HeadSHA != got.State.IntegrationHead) {
				t.Fatalf("approval not bound to exact verified head: %+v", got.State.Review)
			}
			for _, candidate := range state.Candidates {
				assertRef(t, root, candidate.Branch, candidate.SourceTip)
			}
			if !fix {
				resumed, ok := ResumeBlockedBatch(got.State)
				if !ok {
					t.Fatal("cap stop not resumable")
				}
				before := gateRuns
				got, err = reviewer.Review(context.Background(), resumed, root, BatchReviewOptions{VerifyCommand: command, MaxAttempts: 1})
				if err == nil || !strings.Contains(err.Error(), "attributed to plan plan-b") || gateRuns-before != 1 || reworks != 1 {
					t.Fatalf("cap rerun restarted attribution or rework: %+v %v", got.State, err)
				}
			}
		})
	}
}

type attributionRestoreTestGit struct {
	GitClient
	head            string
	failRestore     bool
	restoreAttempts int
}

func (g *attributionRestoreTestGit) ResetHard(ctx context.Context, head string) error {
	if head == g.head {
		g.restoreAttempts++
		if g.failRestore {
			return errors.New("restore interrupted")
		}
	}
	return g.GitClient.ResetHard(ctx, head)
}

func TestBatchReviewVerificationAttributionPersistenceRecovery(t *testing.T) {
	t.Parallel()
	for _, interruptRestore := range []bool{false, true} {
		t.Run(fmt.Sprintf("interrupt-restore=%t", interruptRestore), func(t *testing.T) {
			fixture, state, root := batchAttributionFixture(t)
			service := NewService(fixture.repoRoot, nil)
			client, err := service.gitClientForRoot(root)
			if err != nil {
				t.Fatal(err)
			}
			git := &attributionRestoreTestGit{GitClient: client, head: state.IntegrationHead}
			service.NewGit = func(string) GitClient { return git }
			store := &batchReviewTestStore{dir: t.TempDir()}
			store.afterTransition = func(saved BatchState) error {
				if a := saved.VerificationAttribution; a != nil && a.ParkedSHA != "" {
					// Fail the next write, after the probe has moved HEAD. Also
					// interrupt cleanup in one arm to model a process dying parked.
					store.failAt = len(store.states) + 1
					git.failRestore = interruptRestore
				}
				return nil
			}
			agent := batchSessionAgentFunc(func(_ context.Context, request BatchAgentSessionRequest) (BatchAgentSessionResult, error) {
				if request.Operation == BatchAgentOperationAggregateRework {
					return BatchAgentSessionResult{Output: batchResolutionJSON("repair verification")}, os.Remove(filepath.Join(root, "broken.txt"))
				}
				return BatchAgentSessionResult{Output: reviewJSON("approve", "green", "")}, nil
			})
			reviewer := BatchAggregateReviewer{Store: store, Service: service, Agent: agent}
			options := BatchReviewOptions{VerifyCommand: "test ! -f broken.txt", MaxAttempts: 1}
			_, err = reviewer.Review(context.Background(), state, root, options)
			if err == nil || !strings.Contains(err.Error(), "persist verification probe result") || git.restoreAttempts != 1 {
				t.Fatalf("expected persistence failure and cleanup attempt: %v attempts=%d", err, git.restoreAttempts)
			}
			interrupted := store.states[len(store.states)-1]
			a := interrupted.VerificationAttribution
			if a == nil || a.Status != batchAttributionBisecting || a.ParkedSHA == "" {
				t.Fatalf("lost durable parked intent: %+v", a)
			}
			wantHead := state.IntegrationHead
			if interruptRestore {
				wantHead = a.ParkedSHA
			}
			assertRef(t, root, "HEAD", wantHead)
			git.failRestore = false
			resumeStore := &batchReviewTestStore{dir: t.TempDir()}
			reviewer.Store = resumeStore
			runner := service.commandRunner()
			var heads []string
			reviewer.Service.Runner = func(ctx context.Context, root, name string, args []string, stdout, stderr io.Writer) error {
				heads = append(heads, strings.TrimSpace(realGitOutput(t, root, "rev-parse", "HEAD")))
				return runner(ctx, root, name, args, stdout, stderr)
			}
			got, err := reviewer.Review(context.Background(), interrupted, root, options)
			if err != nil || got.State.Status != BatchStatusReadyToLand || got.State.VerificationAttribution.PlanID != "plan-b" {
				t.Fatalf("attribution recovery failed: %+v %v", got.State, err)
			}
			if len(heads) == 0 || heads[0] != state.IntegrationHead || resumeStore.states[0].VerificationAttribution != nil {
				t.Fatalf("did not restore and clear attribution before re-verifying: heads=%v state=%+v", heads, resumeStore.states[0])
			}
			assertRef(t, root, "HEAD", got.State.IntegrationHead)
		})
	}
}

func TestBatchReviewVerificationAttributionRecoveryDetectsAggregateGate(t *testing.T) {
	unsetMergeVerifyCommandEnv(t)
	for _, prefixCommand := range []string{"make build && make test", ""} {
		t.Run("prefix-command="+prefixCommand, func(t *testing.T) {
			fixture, state, root := batchReviewFixture(t)
			weakMakefile := "build:\n\t@true\ntest:\n\t@true\n"
			if prefixCommand != "" {
				if err := os.WriteFile(filepath.Join(root, "Makefile"), []byte(weakMakefile), 0o600); err != nil {
					t.Fatal(err)
				}
				runRealGit(t, root, "add", "Makefile")
				runRealGit(t, root, "commit", "-m", "build: add weak gate")
			}
			parked := strings.TrimSpace(realGitOutput(t, root, "rev-parse", "HEAD"))
			if err := os.WriteFile(filepath.Join(root, "Makefile"), []byte(weakMakefile+"verify:\n\t@echo aggregate-gate-failed\n\t@false\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			runRealGit(t, root, "add", "Makefile")
			runRealGit(t, root, "commit", "-m", "build: add full gate")
			state.IntegrationHead = strings.TrimSpace(realGitOutput(t, root, "rev-parse", "HEAD"))
			state.Integrations[0].IntegrationSHA = state.IntegrationHead
			state.VerificationAttribution = &BatchVerificationAttribution{Status: batchAttributionBisecting, ParkedSHA: parked}
			runRealGit(t, root, "reset", "--hard", parked)
			if got := (Service{}).resolveMergeVerifyCommandAtRoot(root, Options{}).command; got != prefixCommand {
				t.Fatalf("prefix command = %q, want %q", got, prefixCommand)
			}

			store := &batchReviewTestStore{dir: t.TempDir()}
			service := NewService(fixture.repoRoot, nil)
			runner := service.commandRunner()
			var heads, commands []string
			service.Runner = func(ctx context.Context, cwd, name string, args []string, stdout, stderr io.Writer) error {
				heads = append(heads, strings.TrimSpace(realGitOutput(t, cwd, "rev-parse", "HEAD")))
				commands = append(commands, strings.Join(args, " "))
				return runner(ctx, cwd, name, args, stdout, stderr)
			}
			agentCalls := 0
			got, err := (BatchAggregateReviewer{
				Store: store, Service: service,
				Agent: batchReviewAgentFunc(func(context.Context, string, string) (string, error) {
					agentCalls++
					return reviewJSON("approve", "green", ""), nil
				}),
			}).Review(context.Background(), state, root, BatchReviewOptions{})
			if len(commands) == 0 || commands[0] != "-c make verify" || heads[0] != state.IntegrationHead {
				t.Fatalf("did not detect and run aggregate gate after restoration: commands=%v heads=%v err=%v", commands, heads, err)
			}
			if err == nil || got.State.Status != BatchStatusBlocked || agentCalls != 0 {
				t.Fatalf("failing aggregate gate authorized review: state=%+v agentCalls=%d err=%v", got.State, agentCalls, err)
			}
			if v := got.State.Verification; v == nil || v.Command != "make verify" || v.HeadSHA != state.IntegrationHead || v.Passed || !strings.Contains(v.Output, "aggregate-gate-failed") {
				t.Fatalf("missing full aggregate verification failure: %+v", v)
			}
			if len(store.states) == 0 || store.states[0].VerificationAttribution != nil {
				t.Fatal("interrupted attribution was not cleared before verification")
			}
			assertRef(t, root, "HEAD", state.IntegrationHead)
		})
	}
}

func TestBatchReviewVerificationAttributionBetweenProbeRecovery(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture, state, _ := batchAttributionFixture(t)
	for i := range state.Candidates {
		state.Candidates[i].RepoRoot = state.RepoRoot
		state.Candidates[i].DefaultBranch = state.DefaultBranch
		state.Candidates[i].DefaultStartSHA = state.DefaultStartSHA
	}
	owner, err := NewBatchWorkspace(fixture.repoRoot, filepath.Join(t.TempDir(), "merge-batches"), nil)
	if err != nil {
		t.Fatal(err)
	}
	integration, err := owner.Start(ctx, state)
	if err != nil {
		t.Fatal(err)
	}
	root := integration.Path
	runRealGit(t, root, "reset", "--hard", state.IntegrationHead)
	if err := owner.ValidateResume(ctx, state); err != nil {
		t.Fatal(err)
	}

	store := &batchReviewTestStore{dir: t.TempDir()}
	var interrupted BatchState
	var interruptedHead string
	store.afterTransition = func(saved BatchState) error {
		a := saved.VerificationAttribution
		if a != nil && a.ParkedSHA == state.Integrations[1].IntegrationSHA && a.GateRuns == 1 {
			// Capture a crash immediately after the second probe intent is durable,
			// before its reset. Reconstruct this snapshot below without cleanup.
			interrupted = saved
			interruptedHead = realGitOutput(t, root, "rev-parse", "HEAD")
			return errors.New("interrupted after second probe intent")
		}
		return nil
	}
	service := NewService(fixture.repoRoot, nil)
	agent := batchSessionAgentFunc(func(_ context.Context, request BatchAgentSessionRequest) (BatchAgentSessionResult, error) {
		if request.Operation == BatchAgentOperationAggregateRework {
			return BatchAgentSessionResult{Output: batchResolutionJSON("repair verification")}, os.Remove(filepath.Join(root, "broken.txt"))
		}
		return BatchAgentSessionResult{Output: reviewJSON("approve", "green", "")}, nil
	})
	reviewer := BatchAggregateReviewer{Store: store, Service: service, Agent: agent}
	options := BatchReviewOptions{VerifyCommand: "test ! -f broken.txt", MaxAttempts: 1}
	if _, err := reviewer.Review(ctx, state, root, options); err == nil || !strings.Contains(err.Error(), "interrupted after second probe intent") {
		t.Fatalf("expected second probe interruption, got %v", err)
	}
	if interruptedHead == "" {
		t.Fatal("did not capture the second probe intent")
	}
	runRealGit(t, root, "reset", "--hard", interruptedHead)
	if err := owner.ValidateResume(ctx, interrupted); err != nil {
		t.Fatalf("second probe intent left a legitimate head unresumable: %v", err)
	}

	// Other prefixes, including the superseded probe, are not recovery authority.
	for _, head := range []string{state.DefaultStartSHA, state.Integrations[0].IntegrationSHA} {
		runRealGit(t, root, "reset", "--hard", head)
		var resumeErr *BatchResumeError
		if err := owner.ValidateResume(ctx, interrupted); !errors.As(err, &resumeErr) {
			t.Fatalf("unrelated head %s accepted: %v", head, err)
		}
		if !slices.ContainsFunc(resumeErr.Drifts, func(drift BatchDrift) bool { return drift.Scope == "integration head" }) {
			t.Fatalf("missing integration head drift: %v", resumeErr)
		}
	}
	runRealGit(t, root, "reset", "--hard", interruptedHead)
	resumeStore := &batchReviewTestStore{dir: t.TempDir()}
	reviewer.Store = resumeStore
	runner := service.commandRunner()
	var heads []string
	reviewer.Service.Runner = func(ctx context.Context, root, name string, args []string, stdout, stderr io.Writer) error {
		heads = append(heads, realGitOutput(t, root, "rev-parse", "HEAD"))
		return runner(ctx, root, name, args, stdout, stderr)
	}
	got, err := reviewer.Review(ctx, interrupted, root, options)
	if err != nil || got.State.Status != BatchStatusReadyToLand || got.State.VerificationAttribution.PlanID != "plan-b" {
		t.Fatalf("between-probe recovery failed: %+v %v", got.State, err)
	}
	if len(heads) == 0 || heads[0] != state.IntegrationHead || resumeStore.states[0].VerificationAttribution != nil {
		t.Fatalf("did not restore and clear attribution before re-verifying: heads=%v state=%+v", heads, resumeStore.states[0])
	}
	assertRef(t, root, "HEAD", got.State.IntegrationHead)
	if err := owner.ValidateResume(ctx, got.State); err != nil {
		t.Fatalf("recovered batch failed workspace validation: %v", err)
	}
}

func TestBatchVerificationAttributionRestoresOnCancellationAndRefMutation(t *testing.T) {
	t.Parallel()
	for _, cancelProbe := range []bool{false, true} {
		t.Run(fmt.Sprintf("cancel=%t", cancelProbe), func(t *testing.T) {
			fixture, state, root := batchAttributionFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			service := NewService(fixture.repoRoot, nil)
			git, err := service.gitClientForRoot(root)
			if err != nil {
				t.Fatal(err)
			}
			service.Runner = func(_ context.Context, root, _ string, _ []string, _, _ io.Writer) error {
				if cancelProbe {
					cancel()
					return ctx.Err()
				}
				runRealGit(t, root, "update-ref", "refs/heads/"+fixture.defaultBranch, state.IntegrationHead)
				return errors.New("gate failed")
			}
			store := &batchReviewTestStore{dir: t.TempDir()}
			got, err := (BatchAggregateReviewer{Store: store, Service: service}).attributeVerificationFailure(ctx, git, state, root, "gate", "initial failure")
			if err == nil || (cancelProbe && !errors.Is(err, context.Canceled)) {
				t.Fatalf("expected canceled or unsafe attribution: %v", err)
			}
			if a := got.VerificationAttribution; a == nil || a.Status != batchAttributionBisecting || a.ParkedSHA != "" {
				t.Fatalf("unsafe attribution or uncleared parked head: %+v", a)
			}
			assertRef(t, root, "HEAD", state.IntegrationHead)
			assertRef(t, root, fixture.defaultBranch, state.DefaultStartSHA)
		})
	}
}

func TestBatchVerificationAttributionSkipsResolutionHead(t *testing.T) {
	t.Parallel()
	fixture, state, root := batchAttributionFixture(t)
	runRealGit(t, root, "commit", "--allow-empty", "-m", "fix: resolution")
	state.IntegrationHead = strings.TrimSpace(realGitOutput(t, root, "rev-parse", "HEAD"))
	service := NewService(fixture.repoRoot, nil)
	git, err := service.gitClientForRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	service.Runner = func(context.Context, string, string, []string, io.Writer, io.Writer) error {
		t.Fatal("must not probe a history with resolution commits")
		return nil
	}
	got, err := (BatchAggregateReviewer{Store: &batchReviewTestStore{dir: t.TempDir()}, Service: service}).attributeVerificationFailure(context.Background(), git, state, root, "gate", "failed")
	if err != nil || got.VerificationAttribution.Status != batchAttributionUnattributed || got.VerificationAttribution.ParkedSHA != "" {
		t.Fatalf("expected unattributed resolution head: %+v %v", got, err)
	}
	assertRef(t, root, "HEAD", state.IntegrationHead)
}

func TestBatchReviewCachePreparationFailureBlocksApproval(t *testing.T) {
	t.Parallel()
	fixture, state, root := batchAttributionFixture(t)
	if err := os.WriteFile(filepath.Join(root, ".tao"), []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Track the obstruction so dirty-worktree checks cannot mask preparation
	// failure. This additional head is not eligible for clean-prefix attribution.
	runRealGit(t, root, "add", "-f", ".tao")
	runRealGit(t, root, "commit", "-m", "test: obstruct cache storage")
	state.IntegrationHead = realGitOutput(t, root, "rev-parse", "HEAD")
	got, err := (BatchAggregateReviewer{
		Store: &batchReviewTestStore{dir: t.TempDir()}, Service: NewService(fixture.repoRoot, nil),
		Agent: batchReviewAgentFunc(func(context.Context, string, string) (string, error) {
			t.Fatal("cache preparation failure must not reach review")
			return "", nil
		}),
	}).Review(context.Background(), state, root, BatchReviewOptions{VerifyCommand: "touch executed"})
	if err == nil || !strings.Contains(err.Error(), "prepare verification cache") || got.State.Status != BatchStatusBlocked {
		t.Fatalf("expected blocked preparation failure: state=%+v err=%v", got.State, err)
	}
	if got.State.Verification == nil || got.State.Verification.Passed || got.State.Review != nil {
		t.Fatalf("preparation failure recorded passing evidence: %+v", got.State)
	}
	if _, err := os.Stat(filepath.Join(root, "executed")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("command ran after preparation failure: %v", err)
	}
	assertRef(t, root, "HEAD", state.IntegrationHead)
	assertRef(t, fixture.repoRoot, fixture.defaultBranch, state.DefaultStartSHA)
}

func TestBatchReviewVerificationBaselineFailure(t *testing.T) {
	t.Parallel()
	fixture, state, root := batchAttributionFixture(t)
	got, err := (BatchAggregateReviewer{
		Store: &batchReviewTestStore{dir: t.TempDir()}, Service: NewService(fixture.repoRoot, nil),
		Agent: batchReviewAgentFunc(func(context.Context, string, string) (string, error) {
			t.Fatal("baseline failure must not call an agent")
			return "", nil
		}),
	}).Review(context.Background(), state, root, BatchReviewOptions{VerifyCommand: "echo baseline-broken; false"})
	want := "aggregate verification failed: default start " + state.DefaultStartSHA + " fails verification before any candidate; fix the default branch and rerun tao merge --all --restart"
	if err == nil || err.Error() != want || got.State.BlockKind != BatchBlockKindResumable {
		t.Fatalf("expected baseline stop: %+v %v", got.State, err)
	}
	if a := got.State.VerificationAttribution; a == nil || a.Status != batchAttributionBaselineFailed || a.FailingSHA != state.DefaultStartSHA || a.ParkedSHA != "" {
		t.Fatalf("baseline evidence: %+v", a)
	}
	assertRef(t, root, "HEAD", state.IntegrationHead)
}

func batchAttributionFixture(t *testing.T) (realGitWorktree, BatchState, string) {
	t.Helper()
	fixture, state, root := batchReviewFixture(t)
	runRealGit(t, root, "checkout", "-b", "tao/attribution-integration")
	for i, file := range []string{"broken.txt", "third.txt"} {
		parent := state.IntegrationHead
		if err := os.WriteFile(filepath.Join(root, file), []byte("candidate\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		runRealGit(t, root, "add", ".")
		runRealGit(t, root, "commit", "-m", "feat: add candidate")
		head := strings.TrimSpace(realGitOutput(t, root, "rev-parse", "HEAD"))
		id := []string{"plan-b", "plan-c"}[i]
		branch := "tao/" + id
		runRealGit(t, root, "branch", branch, head)
		state.Candidates = append(state.Candidates, BatchCandidate{PlanID: id, PlanTitle: []string{"Plan Two", "Plan Three"}[i], Branch: branch, SourceTip: head, ReviewBase: state.DefaultStartSHA, ReviewHead: head})
		state.ChosenOrder = append(state.ChosenOrder, id)
		state.Integrations = append(state.Integrations, BatchIntegration{PlanID: id, SourceHead: head, IntegrationBaseSHA: parent, IntegrationSHA: head, Status: batchIntegrationApplied})
		state.IntegrationHead = head
	}
	return fixture, state, root
}

func batchReviewFixture(t *testing.T) (realGitWorktree, BatchState, string) {
	t.Helper()
	fixture := newRealGitWorktree(t)
	if err := os.WriteFile(filepath.Join(fixture.worktreePath, "combined.txt"), []byte("combined\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runRealGit(t, fixture.worktreePath, "add", ".")
	runRealGit(t, fixture.worktreePath, "commit", "-m", "feat: combined")
	base := strings.TrimSpace(realGitOutput(t, fixture.repoRoot, "rev-parse", fixture.defaultBranch))
	head := strings.TrimSpace(realGitOutput(t, fixture.worktreePath, "rev-parse", "HEAD"))
	state := BatchState{Schema: BatchStateSchema, ID: "batch-review", Status: BatchStatusReviewing, RepoRoot: fixture.repoRoot, DefaultBranch: fixture.defaultBranch, DefaultStartSHA: base, IntegrationHead: head, ChosenOrder: []string{"plan-a"}, Candidates: []BatchCandidate{{PlanID: "plan-a", Branch: fixture.planBranch, SourceTip: head, ReviewBase: base, ReviewHead: head, ReviewSummary: "approved source"}}, Integrations: []BatchIntegration{{PlanID: "plan-a", SourceHead: head, IntegrationBaseSHA: base, IntegrationSHA: head, Status: batchIntegrationApplied}}}
	return fixture, state, fixture.worktreePath
}

func reviewJSON(verdict, summary, message string) string {
	findings := "[]"
	if message != "" {
		findings = `[{"severity":"major","file":"combined.txt","line":1,"message":"` + message + `","suggestion":"fix"}]`
	}
	return "review\n```tao-review-json\n{\"verdict\":\"" + verdict + "\",\"summary\":\"" + summary + "\",\"findings\":" + findings + "}\n```"
}

func assertRef(t *testing.T, dir, ref, want string) {
	t.Helper()
	if got := strings.TrimSpace(realGitOutput(t, dir, "rev-parse", ref)); got != want {
		t.Fatalf("%s moved: got %s want %s", ref, got, want)
	}
}
