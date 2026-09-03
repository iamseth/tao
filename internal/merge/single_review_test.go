package merge

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/iamseth/tao/internal/agent"
	"github.com/iamseth/tao/internal/agentsession"
	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/prompts"
)

func TestSingleIntegrationReviewerApprovesExactResolvedHead(t *testing.T) {
	fixture, request, store, git := preparedSingleReviewFixture(t)
	calls := 0
	reviewer := GuardedSingleIntegrationReviewer{
		Git: git, Recorder: store,
		Agent: batchSessionAgentFunc(func(_ context.Context, session BatchAgentSessionRequest) (BatchAgentSessionResult, error) {
			calls++
			if session.Operation != BatchAgentOperationSinglePlanReview || session.Attempt != 1 || session.BatchID != "" || session.CandidatePlanID != request.Intent.PlanID || session.IntegrationRoot != fixture.repoRoot {
				t.Fatalf("review session attribution = %#v", session)
			}
			for _, want := range []string{
				"Review exactly the range " + request.Intent.DefaultParent + ".." + request.Intent.Resolution.IntegrationHead,
				"BEGIN TAO UNTRUSTED CANDIDATE", "Resolve README interaction",
				"BEGIN TAO UNTRUSTED SOURCE REVIEW", "approved source review",
				"BEGIN TAO UNTRUSTED RESOLUTION SUMMARY", "combined both README changes",
				"BEGIN TAO UNTRUSTED EXACT INTEGRATION DIFF", "BEGIN TAO UNTRUSTED EXACT INTEGRATION DIFF STAT",
				"BEGIN TAO UNTRUSTED VERIFICATION EVIDENCE", request.VerificationHead,
			} {
				if !strings.Contains(session.Prompt, want) {
					t.Fatalf("single review prompt lacks %q: %q", want, session.Prompt)
				}
			}
			return BatchAgentSessionResult{
				Output:   reviewJSON("approve", "resolved integration is safe", ""),
				Provider: agentSessionResult("pi"),
			}, nil
		}),
	}
	got, err := reviewer.ReviewResolvedIntegration(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 || !got.Authorized || got.Recovered || got.Outcome != SingleReviewOutcomeApprove {
		t.Fatalf("review result = %#v calls=%d", got, calls)
	}
	review := got.Intent.Resolution.Review
	if got.Intent.Resolution.Phase != plan.SingleMergeResolutionPhaseReviewed || review == nil || !review.IsApproved() || review.Base != request.Intent.DefaultParent || review.Head != request.VerificationHead || review.Agent != "pi" {
		t.Fatalf("persisted exact review = %#v", review)
	}
	if store.current.Resolution.Review != review {
		t.Fatal("review projection was not persisted in the resolution transaction")
	}
}

func TestSingleIntegrationReviewerStreamsAndMarksOversizedDiff(t *testing.T) {
	tail := "TAIL-MUST-NOT-BE-RETAINED"
	content := strings.Repeat("x", prompts.SingleMergeReviewDiffCaptureLimit*4) + tail + "\n"
	_, request, store, git := preparedSingleReviewFixtureWithContent(t, content)
	reviewer := GuardedSingleIntegrationReviewer{
		Git: git, Recorder: store,
		Agent: batchSessionAgentFunc(func(_ context.Context, session BatchAgentSessionRequest) (BatchAgentSessionResult, error) {
			if !strings.Contains(session.Prompt, "[TRUNCATED BY TAO WHILE STREAMING GIT DIFF]") {
				t.Fatalf("oversized diff packet lacks streaming truncation marker: %q", session.Prompt)
			}
			if strings.Contains(session.Prompt, tail) {
				t.Fatalf("oversized diff packet retained content beyond its bound: %q", session.Prompt)
			}
			return BatchAgentSessionResult{Output: reviewJSON("approve", "bounded evidence reviewed against exact range", ""), Provider: agentSessionResult("pi")}, nil
		}),
	}
	got, err := reviewer.ReviewResolvedIntegration(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Authorized || got.Outcome != SingleReviewOutcomeApprove {
		t.Fatalf("review result = %#v", got)
	}
}

func TestSingleIntegrationReviewerPersistsDistinctStructuredNonApprovals(t *testing.T) {
	oversized := "```tao-review-json\n" + strings.Repeat("x", 513*1024) + "\n```"
	tests := []struct {
		name        string
		output      string
		wantOutcome SingleReviewOutcome
		wantVerdict string
	}{
		{name: "comment", output: reviewJSON("comment", "manual concern", ""), wantOutcome: SingleReviewOutcomeComment, wantVerdict: plan.ReviewVerdictComment},
		{name: "changes requested", output: reviewJSON("changes_requested", "fix interaction", "README behavior regressed"), wantOutcome: SingleReviewOutcomeChangesRequested, wantVerdict: plan.ReviewVerdictChangesRequested},
		{name: "malformed", output: "not structured", wantOutcome: SingleReviewOutcomeMalformed, wantVerdict: plan.ReviewVerdictComment},
		{name: "oversized", output: oversized, wantOutcome: SingleReviewOutcomeMalformed, wantVerdict: plan.ReviewVerdictComment},
		{name: "approval missing summary and findings", output: singleReviewJSON(`{"verdict":"approve"}`), wantOutcome: SingleReviewOutcomeMalformed, wantVerdict: plan.ReviewVerdictComment},
		{name: "approval empty summary", output: singleReviewJSON(`{"verdict":"approve","summary":"  ","findings":[]}`), wantOutcome: SingleReviewOutcomeMalformed, wantVerdict: plan.ReviewVerdictComment},
		{name: "approval missing findings", output: singleReviewJSON(`{"verdict":"approve","summary":"safe"}`), wantOutcome: SingleReviewOutcomeMalformed, wantVerdict: plan.ReviewVerdictComment},
		{name: "approval null findings", output: singleReviewJSON(`{"verdict":"approve","summary":"safe","findings":null}`), wantOutcome: SingleReviewOutcomeMalformed, wantVerdict: plan.ReviewVerdictComment},
		{name: "approval wrong summary type", output: singleReviewJSON(`{"verdict":"approve","summary":[],"findings":[]}`), wantOutcome: SingleReviewOutcomeMalformed, wantVerdict: plan.ReviewVerdictComment},
		{name: "approval wrong findings type", output: singleReviewJSON(`{"verdict":"approve","summary":"safe","findings":{}}`), wantOutcome: SingleReviewOutcomeMalformed, wantVerdict: plan.ReviewVerdictComment},
		{name: "approval null finding", output: singleReviewJSON(`{"verdict":"approve","summary":"safe","findings":[null]}`), wantOutcome: SingleReviewOutcomeMalformed, wantVerdict: plan.ReviewVerdictComment},
		{name: "approval unknown field", output: singleReviewJSON(`{"verdict":"approve","summary":"safe","findings":[],"extra":true}`), wantOutcome: SingleReviewOutcomeMalformed, wantVerdict: plan.ReviewVerdictComment},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, request, store, git := preparedSingleReviewFixture(t)
			reviewer := GuardedSingleIntegrationReviewer{Git: git, Recorder: store, Agent: batchSessionAgentFunc(func(context.Context, BatchAgentSessionRequest) (BatchAgentSessionResult, error) {
				return BatchAgentSessionResult{Output: tt.output, Provider: agentSessionResult("claude")}, nil
			})}
			got, err := reviewer.ReviewResolvedIntegration(context.Background(), request)
			if !errors.Is(err, ErrSingleReviewNotApproved) || got.Authorized || got.Outcome != tt.wantOutcome {
				t.Fatalf("result/error = %#v / %v", got, err)
			}
			review := got.Intent.Resolution.Review
			if review == nil || review.Verdict != tt.wantVerdict || review.Base != request.Intent.DefaultParent || review.Head != request.VerificationHead || review.Agent != "claude" || review.Summary == "" {
				t.Fatalf("persisted non-approval = %#v", review)
			}
			if tt.wantOutcome == SingleReviewOutcomeChangesRequested && len(review.Findings) != 1 {
				t.Fatalf("changes-requested findings = %#v", review.Findings)
			}
			if tt.wantOutcome == SingleReviewOutcomeMalformed && (review.FindingsCount != 0 || len(review.Findings) != 0 || !strings.Contains(review.Summary, "malformed")) {
				t.Fatalf("malformed output persisted unsafe evidence = %#v", review)
			}
		})
	}
}

func singleReviewJSON(payload string) string {
	return "review\n```tao-review-json\n" + payload + "\n```"
}

func TestSingleIntegrationReviewerPersistsProviderFailureAndTimeoutWithoutRetry(t *testing.T) {
	tests := []struct {
		name        string
		err         error
		wantOutcome SingleReviewOutcome
		wantSummary string
	}{
		{name: "provider failure", err: errors.New("provider unavailable"), wantOutcome: SingleReviewOutcomeProviderFailure, wantSummary: "provider failed"},
		{name: "timeout", err: &agent.SessionTimeoutError{Timeout: time.Minute}, wantOutcome: SingleReviewOutcomeTimeout, wantSummary: "timed out"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, request, store, git := preparedSingleReviewFixture(t)
			calls := 0
			reviewer := GuardedSingleIntegrationReviewer{Git: git, Recorder: store, Agent: batchSessionAgentFunc(func(context.Context, BatchAgentSessionRequest) (BatchAgentSessionResult, error) {
				calls++
				return BatchAgentSessionResult{Provider: agentSessionResult("pi")}, tt.err
			})}
			got, err := reviewer.ReviewResolvedIntegration(context.Background(), request)
			if calls != 1 || !errors.Is(err, ErrSingleReviewNotApproved) || got.Outcome != tt.wantOutcome || got.Authorized {
				t.Fatalf("calls/result/error = %d / %#v / %v", calls, got, err)
			}
			if review := got.Intent.Resolution.Review; review == nil || review.Verdict != plan.ReviewVerdictComment || !strings.Contains(strings.ToLower(review.Summary), tt.wantSummary) {
				t.Fatalf("provider failure projection = %#v", review)
			}
			request.Intent = got.Intent
			recovered, retryErr := reviewer.ReviewResolvedIntegration(context.Background(), request)
			if calls != 1 || !errors.Is(retryErr, ErrSingleReviewNotApproved) || !recovered.Recovered || recovered.Outcome != tt.wantOutcome {
				t.Fatalf("non-approval retry calls/result/error = %d / %#v / %v", calls, recovered, retryErr)
			}
		})
	}
}

func TestSingleIntegrationReviewerPreservesIntegrationDriftAfterCancellation(t *testing.T) {
	fixture, request, store, git := preparedSingleReviewFixture(t)
	configureSingleAgentIgnoredPaths(t, fixture.repoRoot, ".env")
	ctx, cancel := context.WithCancel(context.Background())
	reviewer := GuardedSingleIntegrationReviewer{Git: git, Recorder: store, Agent: batchSessionAgentFunc(func(sessionCtx context.Context, _ BatchAgentSessionRequest) (BatchAgentSessionResult, error) {
		if err := os.WriteFile(filepath.Join(fixture.repoRoot, "README.md"), []byte("cancelled tracked review edit\n"), 0o600); err != nil {
			return BatchAgentSessionResult{}, err
		}
		if err := os.WriteFile(filepath.Join(fixture.repoRoot, "reviewer-untracked.txt"), []byte("cancelled untracked edit\n"), 0o600); err != nil {
			return BatchAgentSessionResult{}, err
		}
		if err := os.WriteFile(filepath.Join(fixture.repoRoot, ".env"), []byte("cancelled ignored edit\n"), 0o600); err != nil {
			return BatchAgentSessionResult{}, err
		}
		cancel()
		return BatchAgentSessionResult{Output: reviewJSON("approve", "looks safe", ""), Provider: agentSessionResult("pi")}, sessionCtx.Err()
	})}

	got, err := reviewer.ReviewResolvedIntegration(ctx, request)
	if !errors.Is(err, ErrSingleReviewNotApproved) || got.Outcome != SingleReviewOutcomeMutation || got.Authorized {
		t.Fatalf("cancelled reviewer result/error = %#v / %v", got, err)
	}
	for path, want := range map[string]string{
		"README.md":              "cancelled tracked review edit\n",
		"reviewer-untracked.txt": "cancelled untracked edit\n",
		".env":                   "cancelled ignored edit\n",
	} {
		contents, readErr := os.ReadFile(filepath.Join(fixture.repoRoot, path)) //nolint:gosec // fixture-owned path verifies preservation.
		if readErr != nil || string(contents) != want {
			t.Fatalf("cancelled review drift in %s was overwritten: %q, %v", path, contents, readErr)
		}
	}
	if head := strings.TrimSpace(realGitOutput(t, fixture.repoRoot, "rev-parse", "HEAD")); head != request.VerificationHead {
		t.Fatalf("cancelled reviewer moved HEAD to %s", head)
	}
}

func TestSingleIntegrationReviewerRejectsAndPreservesConcurrentWorktreeDrift(t *testing.T) {
	fixture, request, store, git := preparedSingleReviewFixture(t)
	configureSingleAgentIgnoredPaths(t, fixture.repoRoot, ".env")
	reviewStarted := make(chan struct{})
	continueReview := make(chan struct{})
	reviewer := GuardedSingleIntegrationReviewer{Git: git, Recorder: store, Agent: batchSessionAgentFunc(func(context.Context, BatchAgentSessionRequest) (BatchAgentSessionResult, error) {
		close(reviewStarted)
		<-continueReview
		return BatchAgentSessionResult{Output: reviewJSON("approve", "looks safe", ""), Provider: agentSessionResult("pi")}, nil
	})}
	type reviewResult struct {
		result SingleReviewResult
		err    error
	}
	done := make(chan reviewResult, 1)
	go func() {
		result, err := reviewer.ReviewResolvedIntegration(context.Background(), request)
		done <- reviewResult{result: result, err: err}
	}()
	<-reviewStarted

	changes := map[string]string{
		"README.md":               "concurrent tracked change\n",
		"verification-output.txt": "concurrent untracked change\n",
		".env":                    "concurrent ignored change\n",
	}
	var writeErr error
	for path, contents := range changes {
		if err := os.WriteFile(filepath.Join(fixture.repoRoot, path), []byte(contents), 0o600); err != nil {
			writeErr = errors.Join(writeErr, err)
		}
	}
	close(continueReview)
	call := <-done
	if writeErr != nil {
		t.Fatal(writeErr)
	}
	if !errors.Is(call.err, ErrSingleReviewNotApproved) || call.result.Outcome != SingleReviewOutcomeMutation || call.result.Authorized {
		t.Fatalf("concurrent drift result/error = %#v / %v", call.result, call.err)
	}
	if review := call.result.Intent.Resolution.Review; review == nil || review.Verdict != plan.ReviewVerdictComment || !strings.Contains(review.Summary, "preserved") {
		t.Fatalf("concurrent drift projection = %#v", review)
	}
	for path, want := range changes {
		contents, err := os.ReadFile(filepath.Join(fixture.repoRoot, path)) //nolint:gosec // fixture-owned path verifies concurrent work survives.
		if err != nil || string(contents) != want {
			t.Fatalf("concurrent change in %s was overwritten: %q, %v", path, contents, err)
		}
	}
	status := realGitOutput(t, fixture.repoRoot, "status", "--porcelain")
	if !strings.Contains(status, "M README.md") || !strings.Contains(status, "?? verification-output.txt") {
		t.Fatalf("preserved worktree status = %q", status)
	}
	if head := strings.TrimSpace(realGitOutput(t, fixture.repoRoot, "rev-parse", "HEAD")); head != request.VerificationHead {
		t.Fatalf("review rejection moved HEAD: %s", head)
	}
	if source := strings.TrimSpace(realGitOutput(t, fixture.repoRoot, "rev-parse", fixture.planBranch)); source != request.Intent.SourceHead {
		t.Fatalf("review rejection moved source ref: %s", source)
	}
}

func TestSingleIntegrationReviewerPreservesConcurrentSourceRef(t *testing.T) {
	fixture, request, store, git := preparedSingleReviewFixture(t)
	reviewer := GuardedSingleIntegrationReviewer{Git: git, Recorder: store, Agent: batchSessionAgentFunc(func(context.Context, BatchAgentSessionRequest) (BatchAgentSessionResult, error) {
		done := make(chan error, 1)
		go func() {
			cmd := exec.Command("git", "update-ref", "refs/heads/"+fixture.planBranch, request.Intent.DefaultParent) //nolint:gosec // fixture-owned concurrent ref update.
			cmd.Dir = fixture.repoRoot
			done <- cmd.Run()
		}()
		if err := <-done; err != nil {
			return BatchAgentSessionResult{}, err
		}
		return BatchAgentSessionResult{Output: reviewJSON("approve", "looks safe", ""), Provider: agentSessionResult("pi")}, nil
	})}
	got, err := reviewer.ReviewResolvedIntegration(context.Background(), request)
	if !errors.Is(err, ErrSingleReviewNotApproved) || got.Outcome != SingleReviewOutcomeMutation || got.Authorized {
		t.Fatalf("concurrent ref result/error = %#v / %v", got, err)
	}
	if source := strings.TrimSpace(realGitOutput(t, fixture.repoRoot, "rev-parse", fixture.planBranch)); source != request.Intent.DefaultParent {
		t.Fatalf("concurrent source ref was overwritten: got %s want %s", source, request.Intent.DefaultParent)
	}
}

func TestSingleIntegrationReviewerRejectsObjectDatabaseWritesAndPreservesHistory(t *testing.T) {
	for _, mutation := range objectDatabaseMutationTestCases() {
		t.Run(mutation.name, func(t *testing.T) {
			fixture, request, store, git := preparedSingleReviewFixture(t)
			objectRoot, historicalObject := historicalObjectPath(t, fixture.repoRoot, request.Intent.DefaultParent)
			requireGitObjectConfinement(t, fixture.repoRoot, objectRoot)
			before := objectDatabaseFingerprint(t, objectRoot)
			historicalContents, err := os.ReadFile(historicalObject) //nolint:gosec // fixture-owned loose Git object is exact preservation evidence.
			if err != nil {
				t.Fatal(err)
			}
			reviewer := GuardedSingleIntegrationReviewer{Git: git, Recorder: store, Agent: batchSessionAgentFunc(func(ctx context.Context, session BatchAgentSessionRequest) (BatchAgentSessionResult, error) {
				if session.ProtectedGitObjectRoot != objectRoot {
					t.Fatalf("protected object root = %q, want %q", session.ProtectedGitObjectRoot, objectRoot)
				}
				mutationErr := mutation.run(ctx, session.IntegrationRoot, session.ProtectedGitObjectRoot, historicalObject)
				if mutationErr == nil || !strings.Contains(mutationErr.Error(), "object database mutation denied") {
					t.Fatalf("provider object mutation was not denied after execution: %v", mutationErr)
				}
				return BatchAgentSessionResult{Output: reviewJSON("approve", "must reject object mutation", ""), Provider: agentSessionResult("pi")}, mutationErr
			})}

			got, err := reviewer.ReviewResolvedIntegration(context.Background(), request)
			if !errors.Is(err, ErrSingleReviewNotApproved) || got.Authorized || got.Outcome != SingleReviewOutcomeProviderFailure {
				t.Fatalf("object mutation result/error = %#v / %v", got, err)
			}
			assertObjectDatabasePreserved(t, objectRoot, historicalObject, before, historicalContents)
			if head := strings.TrimSpace(realGitOutput(t, fixture.repoRoot, "rev-parse", "HEAD")); head != request.VerificationHead {
				t.Fatalf("object-mutation rejection moved integration HEAD: %s", head)
			}
			if got := strings.TrimSpace(realGitOutput(t, fixture.repoRoot, "rev-parse", fixture.defaultBranch)); got != request.VerificationHead {
				t.Fatalf("object-mutation rejection moved default ref: %s", got)
			}
			if got := strings.TrimSpace(realGitOutput(t, fixture.repoRoot, "rev-parse", fixture.planBranch)); got != request.Intent.SourceHead {
				t.Fatalf("object-mutation rejection moved source ref: %s", got)
			}
			if status := strings.TrimSpace(realGitOutput(t, fixture.repoRoot, "status", "--porcelain")); status != "" {
				t.Fatalf("object-mutation rejection left dirty worktree: %q", status)
			}
		})
	}
}

func TestSingleIntegrationReviewerHidesSnapshotBackingAndPreservesDrift(t *testing.T) {
	fixture, request, store, git := preparedSingleReviewFixture(t)
	configureSingleAgentIgnoredPaths(t, fixture.repoRoot, ".env")
	ignored := filepath.Join(fixture.repoRoot, ".env")
	original := []byte("reviewer original secret\n")
	if err := os.WriteFile(ignored, original, 0o600); err != nil {
		t.Fatal(err)
	}
	tempRoot := t.TempDir()
	t.Setenv("TMPDIR", tempRoot)
	tampered := -1
	reviewer := GuardedSingleIntegrationReviewer{Git: git, Recorder: store, Agent: batchSessionAgentFunc(func(context.Context, BatchAgentSessionRequest) (BatchAgentSessionResult, error) {
		tampered = tamperDiscoverableSnapshotBackings(t, tempRoot)
		if err := os.WriteFile(ignored, []byte("reviewer mutation\n"), 0o600); err != nil {
			return BatchAgentSessionResult{}, err
		}
		return BatchAgentSessionResult{Output: reviewJSON("approve", "must be rejected", ""), Provider: agentSessionResult("pi")}, nil
	})}

	got, err := reviewer.ReviewResolvedIntegration(context.Background(), request)
	if !errors.Is(err, ErrSingleReviewNotApproved) || got.Authorized || got.Outcome != SingleReviewOutcomeMutation {
		t.Fatalf("reviewer result/error = %#v / %v", got, err)
	}
	if tampered != 0 {
		t.Fatalf("provider discovered and overwrote %d snapshot backing files", tampered)
	}
	preserved, readErr := os.ReadFile(ignored) //nolint:gosec // test path is rooted in a temporary Git fixture.
	if readErr != nil || string(preserved) != "reviewer mutation\n" {
		t.Fatalf("ignored drift was overwritten: %q, %v", preserved, readErr)
	}
}

func TestSingleIntegrationReviewerPreservesConcurrentLinkedCheckoutEdits(t *testing.T) {
	fixture, request, store, git := preparedSingleReviewFixture(t)
	checkouts := prepareLinkedCheckoutMutationFixture(t, fixture)
	reviewer := GuardedSingleIntegrationReviewer{Git: git, Recorder: store, Agent: batchSessionAgentFunc(func(context.Context, BatchAgentSessionRequest) (BatchAgentSessionResult, error) {
		checkouts.mutate(t)
		return BatchAgentSessionResult{Output: reviewJSON("approve", "looks safe", ""), Provider: agentSessionResult("pi")}, nil
	})}

	got, err := reviewer.ReviewResolvedIntegration(context.Background(), request)
	if !errors.Is(err, ErrSingleReviewNotApproved) || got.Outcome != SingleReviewOutcomeMutation || got.Authorized {
		t.Fatalf("linked-checkout mutation result/error = %#v / %v", got, err)
	}
	checkouts.assertMutated(t, "forbidden")
	if head := strings.TrimSpace(realGitOutput(t, fixture.repoRoot, "rev-parse", "HEAD")); head != request.VerificationHead {
		t.Fatalf("review rejection moved integration HEAD: %s", head)
	}
}

func TestSingleIntegrationReviewerRejectsAndPreservesGitMetadataAndUnlistedRefs(t *testing.T) {
	tests := []struct {
		name   string
		setup  func(*testing.T, realGitWorktree, SingleReviewRequest) func(*testing.T)
		mutate func(*testing.T, realGitWorktree, SingleReviewRequest)
	}{
		{
			name: "repository excludes",
			setup: func(t *testing.T, fixture realGitWorktree, _ SingleReviewRequest) func(*testing.T) {
				path := filepath.Join(fixture.repoRoot, ".git", "info", "exclude")
				before, err := os.ReadFile(path) //nolint:gosec // test reads a temporary fixture path.
				if err != nil {
					t.Fatal(err)
				}
				return func(t *testing.T) {
					after, err := os.ReadFile(path) //nolint:gosec // test reads a temporary fixture path.
					if err != nil || slices.Equal(after, before) || !strings.Contains(string(after), "reviewer-forbidden") {
						t.Fatalf("concurrent repository excludes were overwritten: %v", err)
					}
				}
			},
			mutate: func(t *testing.T, fixture realGitWorktree, _ SingleReviewRequest) {
				path := filepath.Join(fixture.repoRoot, ".git", "info", "exclude")
				file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600) //nolint:gosec // temporary fixture metadata is intentionally mutated.
				if err != nil {
					t.Fatal(err)
				}
				_, writeErr := file.WriteString("\nreviewer-forbidden\n")
				if err := errors.Join(writeErr, file.Close()); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "moved unlisted ref",
			setup: func(t *testing.T, fixture realGitWorktree, request SingleReviewRequest) func(*testing.T) {
				runRealGit(t, fixture.repoRoot, "update-ref", "refs/tags/unlisted-existing", request.Intent.SourceHead)
				return func(t *testing.T) {
					got := strings.TrimSpace(realGitOutput(t, fixture.repoRoot, "rev-parse", "refs/tags/unlisted-existing"))
					if got != request.Intent.DefaultParent {
						t.Fatalf("concurrent unlisted ref was overwritten: got %s want %s", got, request.Intent.DefaultParent)
					}
				}
			},
			mutate: func(t *testing.T, fixture realGitWorktree, request SingleReviewRequest) {
				runRealGit(t, fixture.repoRoot, "update-ref", "refs/tags/unlisted-existing", request.Intent.DefaultParent)
			},
		},
		{
			name: "source ref lock and other linked-worktree index",
			setup: func(t *testing.T, fixture realGitWorktree, _ SingleReviewRequest) func(*testing.T) {
				lock := realGitRefLockPath(t, fixture.repoRoot, fixture.planBranch)
				otherIndex := filepath.Join(realGitAdminDir(t, fixture.worktreePath), "index")
				beforeIndex, err := os.ReadFile(otherIndex) //nolint:gosec // test reads a temporary Git fixture path.
				if err != nil {
					t.Fatal(err)
				}
				return func(t *testing.T) {
					if _, err := os.Lstat(lock); err != nil {
						t.Fatalf("concurrent source ref lock was overwritten: %v", err)
					}
					afterIndex, err := os.ReadFile(otherIndex) //nolint:gosec // test reads a temporary Git fixture path.
					if err != nil || slices.Equal(afterIndex, beforeIndex) || !strings.HasSuffix(string(afterIndex), "reviewer corruption") {
						t.Fatalf("concurrent linked-worktree index was overwritten: %v", err)
					}
				}
			},
			mutate: func(t *testing.T, fixture realGitWorktree, _ SingleReviewRequest) {
				if err := os.WriteFile(realGitRefLockPath(t, fixture.repoRoot, fixture.planBranch), []byte("reviewer lock\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				otherIndex := filepath.Join(realGitAdminDir(t, fixture.worktreePath), "index")
				contents, err := os.ReadFile(otherIndex) //nolint:gosec // test reads a temporary Git fixture path.
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(otherIndex, append(contents, []byte("reviewer corruption")...), 0o600); err != nil { //nolint:gosec // test writes a temporary Git fixture path.
					t.Fatal(err)
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixture, request, store, git := preparedSingleReviewFixture(t)
			assertRestored := tt.setup(t, fixture, request)
			reviewer := GuardedSingleIntegrationReviewer{Git: git, Recorder: store, Agent: batchSessionAgentFunc(func(context.Context, BatchAgentSessionRequest) (BatchAgentSessionResult, error) {
				tt.mutate(t, fixture, request)
				return BatchAgentSessionResult{Output: reviewJSON("approve", "looks safe", ""), Provider: agentSessionResult("pi")}, nil
			})}
			got, err := reviewer.ReviewResolvedIntegration(context.Background(), request)
			if !errors.Is(err, ErrSingleReviewNotApproved) || got.Outcome != SingleReviewOutcomeMutation || got.Authorized {
				t.Fatalf("Git-boundary mutation result/error = %#v / %v", got, err)
			}
			assertRestored(t)
			if head := strings.TrimSpace(realGitOutput(t, fixture.repoRoot, "rev-parse", "HEAD")); head != request.VerificationHead {
				t.Fatalf("HEAD was not restored: %s", head)
			}
		})
	}
}

func TestSingleIntegrationReviewerRejectsAndPreservesIgnoredOnlyDrift(t *testing.T) {
	for _, mutation := range ignoredMutationTestCases() {
		t.Run(mutation.name, func(t *testing.T) {
			fixture, request, store, git := preparedSingleReviewFixture(t)
			configureSingleAgentIgnoredPaths(t, fixture.repoRoot, ".env")
			ignored := filepath.Join(fixture.repoRoot, ".env")
			mutation.prepare(t, ignored)
			reviewer := GuardedSingleIntegrationReviewer{Git: git, Recorder: store, Agent: batchSessionAgentFunc(func(context.Context, BatchAgentSessionRequest) (BatchAgentSessionResult, error) {
				if err := mutation.mutate(ignored); err != nil {
					return BatchAgentSessionResult{}, err
				}
				if status := strings.TrimSpace(realGitOutput(t, fixture.repoRoot, "status", "--porcelain")); status != "" {
					t.Fatalf("ordinary porcelain unexpectedly exposed ignored-only mutation: %q", status)
				}
				return BatchAgentSessionResult{Output: reviewJSON("approve", "looks safe", ""), Provider: agentSessionResult("pi")}, nil
			})}
			got, err := reviewer.ReviewResolvedIntegration(context.Background(), request)
			if !errors.Is(err, ErrSingleReviewNotApproved) || got.Outcome != SingleReviewOutcomeMutation || got.Authorized {
				t.Fatalf("ignored-only mutation result/error = %#v / %v", got, err)
			}
			mutation.assertMutated(t, ignored)
			if status := strings.TrimSpace(realGitOutput(t, fixture.repoRoot, "status", "--porcelain")); status != "" {
				t.Fatalf("ignored drift unexpectedly appeared in ordinary status: %q", status)
			}
		})
	}
}

func TestSingleIntegrationReviewerFailsClosedOnStaleEvidence(t *testing.T) {
	t.Run("verification head", func(t *testing.T) {
		_, request, store, git := preparedSingleReviewFixture(t)
		request.VerificationHead = request.Intent.DefaultParent
		calls := 0
		_, err := (GuardedSingleIntegrationReviewer{Git: git, Recorder: store, Agent: batchSessionAgentFunc(func(context.Context, BatchAgentSessionRequest) (BatchAgentSessionResult, error) {
			calls++
			return BatchAgentSessionResult{}, nil
		})}).ReviewResolvedIntegration(context.Background(), request)
		if !errors.Is(err, ErrSingleReviewRejected) || calls != 0 || store.current.Resolution.Phase != plan.SingleMergeResolutionPhaseCommitted {
			t.Fatalf("stale verification error/calls/state = %v / %d / %#v", err, calls, store.current.Resolution)
		}
	})

	t.Run("source ref", func(t *testing.T) {
		fixture, request, store, git := preparedSingleReviewFixture(t)
		runRealGit(t, fixture.repoRoot, "update-ref", "refs/heads/"+fixture.planBranch, request.Intent.DefaultParent)
		calls := 0
		_, err := (GuardedSingleIntegrationReviewer{Git: git, Recorder: store, Agent: batchSessionAgentFunc(func(context.Context, BatchAgentSessionRequest) (BatchAgentSessionResult, error) {
			calls++
			return BatchAgentSessionResult{}, nil
		})}).ReviewResolvedIntegration(context.Background(), request)
		if !errors.Is(err, ErrSingleReviewRejected) || calls != 0 || !strings.Contains(err.Error(), "source ref") {
			t.Fatalf("stale source error/calls = %v / %d", err, calls)
		}
	})
}

func TestSingleIntegrationReviewerExactRetryIsIdempotent(t *testing.T) {
	_, request, store, git := preparedSingleReviewFixture(t)
	calls := 0
	reviewer := GuardedSingleIntegrationReviewer{Git: git, Recorder: store, Agent: batchSessionAgentFunc(func(context.Context, BatchAgentSessionRequest) (BatchAgentSessionResult, error) {
		calls++
		return BatchAgentSessionResult{Output: reviewJSON("approve", "safe", ""), Provider: agentSessionResult("pi")}, nil
	})}
	first, err := reviewer.ReviewResolvedIntegration(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	request.Intent = first.Intent
	second, err := reviewer.ReviewResolvedIntegration(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 || !second.Recovered || !second.Authorized || second.Outcome != SingleReviewOutcomeApprove {
		t.Fatalf("idempotent retry result/calls = %#v / %d", second, calls)
	}
}

func preparedSingleReviewFixture(t *testing.T) (realGitWorktree, SingleReviewRequest, *recordingSingleResolutionStore, GitClient) {
	t.Helper()
	return preparedSingleReviewFixtureWithContent(t, "combined\n")
}

func preparedSingleReviewFixtureWithContent(t *testing.T, content string) (realGitWorktree, SingleReviewRequest, *recordingSingleResolutionStore, GitClient) {
	t.Helper()
	fixture, resolutionRequest, git := preparedSingleResolutionFixture(t)
	store := &recordingSingleResolutionStore{current: resolutionRequest.Intent}
	resolver := GuardedSingleConflictResolver{Git: git, Recorder: store, Agent: batchSessionAgentFunc(func(_ context.Context, session BatchAgentSessionRequest) (BatchAgentSessionResult, error) {
		return BatchAgentSessionResult{Output: batchResolutionJSON("combined both README changes")}, os.WriteFile(filepath.Join(session.IntegrationRoot, "README.md"), []byte(content), 0o600)
	})}
	resolved, err := resolver.ResolveConflict(context.Background(), resolutionRequest)
	if err != nil {
		t.Fatal(err)
	}
	resolutionRequest.Intent = resolved.Intent
	settled, err := resolver.SettleResolved(context.Background(), resolutionRequest)
	if err != nil {
		t.Fatal(err)
	}
	request := SingleReviewRequest{
		Intent: settled.Intent, SourceBranch: fixture.planBranch, IntegrationRoot: fixture.repoRoot,
		PlanTitle: "Resolve README interaction", SourceReview: "approved source review",
		VerifyCommand: "go test ./...", VerificationHead: settled.Head, VerificationEvidence: "passed",
	}
	return fixture, request, store, git
}

func agentSessionResult(label string) agentsession.Result {
	return agentsession.Result{AgentLabel: label}
}
