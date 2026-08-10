package merge

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/iamseth/tao/internal/gitops"
	"github.com/iamseth/tao/internal/plan"
)

type batchSessionAgentFunc func(context.Context, BatchAgentSessionRequest) (BatchAgentSessionResult, error)

func (f batchSessionAgentFunc) Resolve(ctx context.Context, request BatchAgentSessionRequest) (BatchAgentSessionResult, error) {
	return f(ctx, request)
}

type batchResolutionAgentFunc func(context.Context, string, string) (string, error)

func (f batchResolutionAgentFunc) Resolve(ctx context.Context, request BatchAgentSessionRequest) (BatchAgentSessionResult, error) {
	output, err := f(ctx, request.IntegrationRoot, request.Prompt)
	return BatchAgentSessionResult{Output: output}, err
}

func batchResolutionJSON(summary string) string {
	return batchResolutionJSONWithProposal(summary, plan.ReviewCommitMessage{
		Subject: "fix(batch): resolve candidate integration",
		Body:    "What:\nResolve the candidate changes in the integration worktree.\n\nWhy:\nPreserve the candidate intent in the combined batch.",
	})
}

func batchResolutionJSONWithProposal(summary string, proposal plan.ReviewCommitMessage) string {
	encoded, err := json.Marshal(batchResolutionOutput{Summary: summary, CommitMessage: proposal})
	if err != nil {
		panic(err)
	}
	return string(encoded)
}

type durableFailingBatchTransitionStore struct {
	states []BatchState
	failAt int
}

func (s *durableFailingBatchTransitionStore) Transition(state BatchState, _ string) (BatchState, error) {
	if s.failAt > 0 && len(s.states)+1 == s.failAt {
		return BatchState{}, errors.New("transition failed")
	}
	encoded, err := json.Marshal(state)
	if err != nil {
		return BatchState{}, err
	}
	var persisted BatchState
	if err := json.Unmarshal(encoded, &persisted); err != nil {
		return BatchState{}, err
	}
	persisted.LogSequence++
	s.states = append(s.states, persisted)
	var returned BatchState
	if err := json.Unmarshal(encoded, &returned); err != nil {
		return BatchState{}, err
	}
	returned.LogSequence = persisted.LogSequence
	return returned, nil
}

func TestBatchAgentResolvesTextConflictAndTaoOwnsCommit(t *testing.T) {
	fixture, sourceHead, defaultHead, integrationRoot := batchAgentConflictFixture(t)
	state := batchAgentDeferredState(fixture, sourceHead, defaultHead)
	reviewProposal := plan.ReviewCommitMessage{
		Subject: "feat(batch): integrate approved candidate",
		Body:    "What:\nIntegrate the approved candidate.\n\nWhy:\nReuse the exact review description.",
	}
	reviewMessage, err := singleMergeCommitMessage(reviewProposal, "plan-a", sourceHead)
	if err != nil {
		t.Fatal(err)
	}
	state.Candidates[0].ReviewCommitMessage = &reviewProposal
	state.Candidates[0].CommitMessage = reviewMessage
	state.Integrations[0].CommitMessage = reviewMessage
	store := &recordingBatchTransitionStore{}
	calls := 0
	wantAttempt := state.Integrations[0].Attempts + 1
	agent := batchSessionAgentFunc(func(_ context.Context, request BatchAgentSessionRequest) (BatchAgentSessionResult, error) {
		calls++
		if request.BatchID != state.ID || request.Operation != BatchAgentOperationCandidateResolution || request.Attempt != wantAttempt || request.CandidatePlanID != "plan-a" || request.IntegrationRoot != integrationRoot || !strings.Contains(request.Prompt, "Do not run git commit") || !strings.Contains(request.Prompt, "BEGIN TAO UNTRUSTED CONFLICT FILES") {
			t.Fatalf("unexpected attributed agent request: %#v", request)
		}
		return BatchAgentSessionResult{Output: batchResolutionJSON("resolved README")}, os.WriteFile(filepath.Join(request.IntegrationRoot, "README.md"), []byte("combined\n"), 0o600)
	})
	service := NewService(fixture.repoRoot, nil)
	got, err := (BatchAgentResolver{Store: store, Service: service, Agent: agent}).Resolve(context.Background(), state, integrationRoot, BatchResolveOptions{VerifyCommand: "grep -q combined README.md"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Resolved) != 1 || got.State.Status != BatchStatusReviewing || got.State.Integrations[0].Status != batchIntegrationApplied {
		t.Fatalf("unexpected resolution result: %#v", got)
	}
	message := strings.TrimSpace(realGitOutput(t, integrationRoot, "show", "-s", "--format=%B", "HEAD"))
	if calls != 1 {
		t.Fatalf("resolver calls = %d, want one editing/message session", calls)
	}
	if !strings.HasPrefix(message, "fix(batch): resolve candidate integration") || message == reviewMessage {
		t.Fatalf("resolution did not replace the changed review description: %q", message)
	}
	if !got.State.Candidates[0].CommitMessageResolved || got.State.Candidates[0].CommitMessage != message || got.State.Integrations[0].CommitMessage != message || got.State.Integrations[0].Resolutions[0].CommitMessage != message {
		t.Fatalf("exact resolved message was not durable across candidate, intent, and outcome: %+v", got.State)
	}
	for _, want := range []string{"Tao-Plan: plan-a", "Tao-Source-Head: " + sourceHead} {
		if !strings.Contains(message, want) {
			t.Fatalf("Tao-owned commit missing %q: %q", want, message)
		}
	}
	if gotDefault := strings.TrimSpace(realGitOutput(t, fixture.repoRoot, "rev-parse", fixture.defaultBranch)); gotDefault != defaultHead {
		t.Fatalf("default moved: got %s want %s", gotDefault, defaultHead)
	}
	if gotSource := strings.TrimSpace(realGitOutput(t, fixture.repoRoot, "rev-parse", fixture.planBranch)); gotSource != sourceHead {
		t.Fatalf("source moved: got %s want %s", gotSource, sourceHead)
	}
	if len(store.states) < 4 {
		t.Fatalf("request, outcome, commit, and phase were not durably transitioned: %d states", len(store.states))
	}
}

func TestBatchAgentSessionFailuresStopWithoutAnotherResolveCall(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{{"provider", errors.New("provider failed")}, {"timeout", context.DeadlineExceeded}} {
		t.Run(tc.name, func(t *testing.T) {
			fixture, sourceHead, defaultHead, integrationRoot := batchAgentConflictFixture(t)
			state := batchAgentDeferredState(fixture, sourceHead, defaultHead)
			calls := 0
			wantAttempt := state.Integrations[0].Attempts + 1
			agent := batchSessionAgentFunc(func(_ context.Context, request BatchAgentSessionRequest) (BatchAgentSessionResult, error) {
				calls++
				if request.Operation != BatchAgentOperationCandidateResolution || request.Attempt != wantAttempt || request.BatchID != state.ID || request.CandidatePlanID != "plan-a" {
					t.Fatalf("unexpected failure attribution: %#v", request)
				}
				return BatchAgentSessionResult{Output: "partial provider output"}, tc.err
			})
			got, err := (BatchAgentResolver{Store: &recordingBatchTransitionStore{}, Service: NewService(fixture.repoRoot, nil), Agent: agent}).Resolve(context.Background(), state, integrationRoot, BatchResolveOptions{MaxAttempts: 3})
			if err == nil || got.State.Status != BatchStatusBlocked || calls != 1 {
				t.Fatalf("session failure retried: calls=%d state=%s err=%v", calls, got.State.Status, err)
			}
		})
	}
}

func TestBatchAgentKeepsDirectReviewMessageWhenResolutionDescriptionIsUnchanged(t *testing.T) {
	fixture, sourceHead, defaultHead, integrationRoot := batchAgentConflictFixture(t)
	state := batchAgentDeferredState(fixture, sourceHead, defaultHead)
	proposal := plan.ReviewCommitMessage{
		Subject: "fix(batch): resolve candidate integration",
		Body:    "What:\nResolve the candidate changes in the integration worktree.\n\nWhy:\nPreserve the candidate intent in the combined batch.",
	}
	message, err := singleMergeCommitMessage(proposal, "plan-a", sourceHead)
	if err != nil {
		t.Fatal(err)
	}
	state.Candidates[0].ReviewCommitMessage = &proposal
	state.Candidates[0].CommitMessage = message
	state.Integrations[0].CommitMessage = message
	got, err := (BatchAgentResolver{
		Store: &recordingBatchTransitionStore{}, Service: NewService(fixture.repoRoot, nil),
		Agent: batchResolutionAgentFunc(func(_ context.Context, root, _ string) (string, error) {
			return batchResolutionJSONWithProposal("resolved without description changes", proposal), os.WriteFile(filepath.Join(root, "README.md"), []byte("combined\n"), 0o600)
		}),
	}).Resolve(context.Background(), state, integrationRoot, BatchResolveOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got.State.Candidates[0].CommitMessageResolved || got.State.Candidates[0].CommitMessage != message || got.State.Integrations[0].CommitMessage != message {
		t.Fatalf("unchanged resolution replaced the direct review message: %+v", got.State)
	}
	if committed := strings.TrimSpace(realGitOutput(t, integrationRoot, "show", "-s", "--format=%B", "HEAD")); committed != message {
		t.Fatalf("committed message = %q, want direct review message %q", committed, message)
	}
}

func TestBatchAgentResumesResolvedCandidateAfterCommitBeforeTransition(t *testing.T) {
	fixture, sourceHead, defaultHead, integrationRoot := batchAgentConflictFixture(t)
	state := batchAgentDeferredState(fixture, sourceHead, defaultHead)
	store := &durableFailingBatchTransitionStore{failAt: 4}
	calls := 0
	agent := batchResolutionAgentFunc(func(_ context.Context, root, _ string) (string, error) {
		calls++
		return batchResolutionJSON("resolved README"), os.WriteFile(filepath.Join(root, "README.md"), []byte("combined\n"), 0o600)
	})
	resolver := BatchAgentResolver{Store: store, Service: NewService(fixture.repoRoot, nil), Agent: agent}
	_, err := resolver.Resolve(context.Background(), state, integrationRoot, BatchResolveOptions{})
	if err == nil || !strings.Contains(err.Error(), "persist resolved candidate commit") {
		t.Fatalf("expected post-commit transition failure, got %v", err)
	}
	committedHead := strings.TrimSpace(realGitOutput(t, integrationRoot, "rev-parse", "HEAD"))
	intent := store.states[len(store.states)-1]
	if intent.Integrations[0].Status != batchIntegrationApplying || intent.IntegrationHead != defaultHead {
		t.Fatalf("durable commit intent = %#v", intent.Integrations)
	}

	resumeStore := &durableFailingBatchTransitionStore{}
	resolver.Store = resumeStore
	got, err := resolver.Resolve(context.Background(), intent, integrationRoot, BatchResolveOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("resume invoked agent again: %d calls", calls)
	}
	if got.State.IntegrationHead != committedHead || got.State.Integrations[0].Status != batchIntegrationApplied {
		t.Fatalf("resume did not settle exact commit: %#v", got.State.Integrations)
	}
	if head := strings.TrimSpace(realGitOutput(t, integrationRoot, "rev-parse", "HEAD")); head != committedHead {
		t.Fatalf("resume changed committed head: got %s want %s", head, committedHead)
	}
}

func TestBatchAgentResumesResolvedIntentBeforeCommitWithoutAnotherSession(t *testing.T) {
	fixture, sourceHead, defaultHead, integrationRoot := batchAgentConflictFixture(t)
	state := batchAgentDeferredState(fixture, sourceHead, defaultHead)
	store := &recordingBatchTransitionStore{}
	const interrupted = "interrupted after resolved message intent"
	store.afterTransition = func(state BatchState) error {
		if state.Integrations[0].Status == batchIntegrationApplying {
			panic(interrupted)
		}
		return nil
	}
	calls := 0
	resolver := BatchAgentResolver{
		Store: store, Service: NewService(fixture.repoRoot, nil),
		Agent: batchResolutionAgentFunc(func(_ context.Context, root, _ string) (string, error) {
			calls++
			return batchResolutionJSON("resolved before interruption"), os.WriteFile(filepath.Join(root, "README.md"), []byte("combined\n"), 0o600)
		}),
	}
	func() {
		defer func() {
			if got := recover(); got != interrupted {
				t.Fatalf("unexpected interruption: %v", got)
			}
		}()
		_, _ = resolver.Resolve(context.Background(), state, integrationRoot, BatchResolveOptions{})
		t.Fatal("expected simulated process interruption")
	}()
	intent := store.states[len(store.states)-1]
	resolution := latestBatchResolution(&intent.Integrations[0])
	if intent.Integrations[0].Status != batchIntegrationApplying || resolution == nil || resolution.CommitMessage == "" || resolution.CommitMessage != intent.Integrations[0].CommitMessage || resolution.CommitMessage != intent.Candidates[0].CommitMessage {
		t.Fatalf("resolved message intent was not exact and durable: %+v", intent)
	}
	store.afterTransition = nil
	got, err := resolver.Resolve(context.Background(), intent, integrationRoot, BatchResolveOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("resume started another agent session: %d calls", calls)
	}
	if got.State.Integrations[0].Status != batchIntegrationApplied {
		t.Fatalf("resolved intent was not committed: %+v", got.State.Integrations[0])
	}
	message := strings.TrimSpace(realGitOutput(t, integrationRoot, "show", "-s", "--format=%B", "HEAD"))
	if message != resolution.CommitMessage {
		t.Fatalf("resumed commit message = %q, want exact intent %q", message, resolution.CommitMessage)
	}
	if parent := strings.TrimSpace(realGitOutput(t, integrationRoot, "rev-parse", "HEAD^")); parent != defaultHead {
		t.Fatalf("resumed commit parent = %s, want %s", parent, defaultHead)
	}
}

func TestBatchAgentRecoveryRequiresFreshProposalWhenResolvedContentDrifts(t *testing.T) {
	for _, tc := range []struct {
		name  string
		drift func(*testing.T, string)
	}{
		{
			name: "same path",
			drift: func(t *testing.T, root string) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("same-path drift\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "additional path",
			drift: func(t *testing.T, root string) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(root, "injected.txt"), []byte("additional drift\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture, sourceHead, defaultHead, integrationRoot := batchAgentConflictFixture(t)
			state := batchAgentDeferredState(fixture, sourceHead, defaultHead)
			store := &recordingBatchTransitionStore{}
			const interrupted = "interrupted after exact resolved intent"
			store.afterTransition = func(state BatchState) error {
				if state.Integrations[0].Status == batchIntegrationApplying {
					panic(interrupted)
				}
				return nil
			}
			oldProposal := plan.ReviewCommitMessage{
				Subject: "fix(batch): resolve original candidate",
				Body:    "What:\nResolve the original candidate content.\n\nWhy:\nIntegrate the approved source safely.",
			}
			freshProposal := plan.ReviewCommitMessage{
				Subject: "fix(batch): resolve refreshed candidate",
				Body:    "What:\nResolve the refreshed candidate content.\n\nWhy:\nReplace content that drifted after interruption.",
			}
			calls := 0
			resolver := BatchAgentResolver{
				Store: store, Service: NewService(fixture.repoRoot, nil),
				Agent: batchResolutionAgentFunc(func(_ context.Context, root, _ string) (string, error) {
					calls++
					if calls == 1 {
						return batchResolutionJSONWithProposal("original resolution", oldProposal), os.WriteFile(filepath.Join(root, "README.md"), []byte("original resolved content\n"), 0o600)
					}
					if _, err := os.Stat(filepath.Join(root, "injected.txt")); !errors.Is(err, os.ErrNotExist) {
						t.Fatalf("additional drift was not restored before reproposal: %v", err)
					}
					readme, err := os.ReadFile(filepath.Join(root, "README.md")) //nolint:gosec // Test root is an isolated fixture.
					if err != nil {
						t.Fatal(err)
					}
					if strings.Contains(string(readme), "same-path drift") {
						t.Fatal("same-path drift was not restored before reproposal")
					}
					return batchResolutionJSONWithProposal("fresh resolution", freshProposal), os.WriteFile(filepath.Join(root, "README.md"), []byte("fresh resolved content\n"), 0o600)
				}),
			}
			func() {
				defer func() {
					if got := recover(); got != interrupted {
						t.Fatalf("unexpected interruption: %v", got)
					}
				}()
				_, _ = resolver.Resolve(context.Background(), state, integrationRoot, BatchResolveOptions{})
				t.Fatal("expected simulated process interruption")
			}()
			intent := store.states[len(store.states)-1]
			resolution := latestBatchResolution(&intent.Integrations[0])
			if resolution == nil || !slices.Equal(resolution.ChangedPaths, []string{"README.md"}) || resolution.ContentFingerprint == "" {
				t.Fatalf("applying intent omitted exact resolved content: %+v", resolution)
			}
			tc.drift(t, integrationRoot)
			store.afterTransition = nil

			got, err := resolver.Resolve(context.Background(), intent, integrationRoot, BatchResolveOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if calls != 2 || got.State.Integrations[0].Status != batchIntegrationApplied {
				t.Fatalf("drift recovery did not obtain one fresh proposal: calls=%d state=%+v", calls, got.State.Integrations[0])
			}
			if got.State.Integrations[0].Resolutions[0].Outcome != "content_drift" {
				t.Fatalf("drifted proposal outcome = %q", got.State.Integrations[0].Resolutions[0].Outcome)
			}
			message := strings.TrimSpace(realGitOutput(t, integrationRoot, "show", "-s", "--format=%B", "HEAD"))
			if !strings.HasPrefix(message, freshProposal.Subject+"\n\n") || strings.Contains(message, oldProposal.Subject) {
				t.Fatalf("recovery committed stale proposal: %q", message)
			}
			if _, err := os.Stat(filepath.Join(integrationRoot, "injected.txt")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("additional drift survived recovery: %v", err)
			}
			if content, err := os.ReadFile(filepath.Join(integrationRoot, "README.md")); err != nil || string(content) != "fresh resolved content\n" { //nolint:gosec // Test root is an isolated fixture.
				t.Fatalf("committed content = %q, err=%v", content, err)
			}
		})
	}
}

func TestBatchAgentResumesInterruptedResolutionWork(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit bool
	}{
		{name: "immediately after preparation"},
		{name: "during agent editing", edit: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture, sourceHead, defaultHead, integrationRoot := batchAgentConflictFixture(t)
			state := batchAgentDeferredState(fixture, sourceHead, defaultHead)
			git, err := NewService(fixture.repoRoot, nil).gitClientForRoot(integrationRoot)
			if err != nil {
				t.Fatal(err)
			}
			state.Integrations[0].Attempts++
			state.Integrations[0].Resolutions = append(state.Integrations[0].Resolutions, BatchResolution{
				Attempt: state.Integrations[0].Attempts, Kind: "conflict", BaseSHA: defaultHead, RequestedAt: "2026-07-16T00:00:00Z",
			})
			durableIntent := &durableFailingBatchTransitionStore{}
			persisted, err := durableIntent.Transition(state, "2026-07-16T00:00:00Z")
			if err != nil {
				t.Fatal(err)
			}
			if err := git.MergeSquash(context.Background(), sourceHead); err == nil {
				t.Fatal("expected prepared squash conflict")
			}
			state = persisted
			if tc.edit {
				if err := os.WriteFile(filepath.Join(integrationRoot, "README.md"), []byte("partial agent edit\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if !recoverableBatchResolutionWork(state, defaultHead) {
				t.Fatal("durable request did not identify recoverable dirty work")
			}

			calls := 0
			got, err := (BatchAgentResolver{
				Store: &recordingBatchTransitionStore{}, Service: NewService(fixture.repoRoot, nil),
				Agent: batchResolutionAgentFunc(func(_ context.Context, root, _ string) (string, error) {
					calls++
					return batchResolutionJSON("resolved after interruption"), os.WriteFile(filepath.Join(root, "README.md"), []byte("combined\n"), 0o600)
				}),
			}).Resolve(context.Background(), state, integrationRoot, BatchResolveOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if calls != 1 || got.State.Integrations[0].Status != batchIntegrationApplied || got.State.Integrations[0].Resolutions[0].Outcome != "interrupted" {
				t.Fatalf("interrupted request was not safely retried: calls=%d state=%#v", calls, got.State.Integrations[0])
			}
		})
	}
}

func TestBatchAgentResumesCrashImmediatelyAfterPreparation(t *testing.T) {
	fixture, sourceHead, defaultHead, integrationRoot := batchAgentConflictFixture(t)
	state := batchAgentDeferredState(fixture, sourceHead, defaultHead)
	store := &durableFailingBatchTransitionStore{}
	const interrupted = "interrupted after preparation"
	resolver := BatchAgentResolver{
		Store: store, Service: NewService(fixture.repoRoot, nil),
		Agent: batchResolutionAgentFunc(func(context.Context, string, string) (string, error) {
			panic(interrupted)
		}),
	}
	func() {
		defer func() {
			if got := recover(); got != interrupted {
				t.Fatalf("unexpected interruption: %v", got)
			}
		}()
		_, _ = resolver.Resolve(context.Background(), state, integrationRoot, BatchResolveOptions{})
		t.Fatal("expected simulated process interruption")
	}()
	if len(store.states) != 1 {
		t.Fatalf("resolution intent was not durable before preparation: %d transitions", len(store.states))
	}
	intent := store.states[0]
	resolution := activeBatchResolution(&intent.Integrations[0])
	if resolution == nil || resolution.BaseSHA != defaultHead {
		t.Fatalf("prepared work has no exact write-ahead intent: %#v", intent.Integrations[0])
	}
	if !recoverableBatchResolutionWork(intent, defaultHead) {
		t.Fatal("prepared work was not recognized as recoverable")
	}
	if status := strings.TrimSpace(realGitOutput(t, integrationRoot, "status", "--porcelain")); status == "" {
		t.Fatal("fault did not leave prepared squash work to recover")
	}

	calls := 0
	resolver.Store = &durableFailingBatchTransitionStore{}
	resolver.Agent = batchResolutionAgentFunc(func(_ context.Context, root, _ string) (string, error) {
		calls++
		return batchResolutionJSON("resolved after preparation interruption"), os.WriteFile(filepath.Join(root, "README.md"), []byte("combined\n"), 0o600)
	})
	got, err := resolver.Resolve(context.Background(), intent, integrationRoot, BatchResolveOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 || got.State.Integrations[0].Status != batchIntegrationApplied || got.State.Integrations[0].Resolutions[0].Outcome != "interrupted" {
		t.Fatalf("prepared interruption was not safely recovered: calls=%d state=%#v", calls, got.State.Integrations[0])
	}
}

func TestBatchAgentResumesLegacyApplyingIntentBeforeCommit(t *testing.T) {
	fixture, sourceHead, defaultHead, integrationRoot := batchAgentConflictFixture(t)
	state := batchAgentDeferredState(fixture, sourceHead, defaultHead)
	git, err := NewService(fixture.repoRoot, nil).gitClientForRoot(integrationRoot)
	if err != nil {
		t.Fatal(err)
	}
	if err := git.MergeSquash(context.Background(), sourceHead); err == nil {
		t.Fatal("expected prepared squash conflict")
	}
	if err := os.WriteFile(filepath.Join(integrationRoot, "README.md"), []byte("interrupted resolved edit\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	stage, ok := git.(interface {
		Add(context.Context, ...string) error
	})
	if !ok {
		t.Fatal("Git client cannot stage applying intent fixture")
	}
	if err := stage.Add(context.Background(), "."); err != nil {
		t.Fatal(err)
	}
	state.Integrations[0].Status = batchIntegrationApplying
	state.Integrations[0].Attempts++
	state.Integrations[0].Resolutions = append(state.Integrations[0].Resolutions, BatchResolution{
		Attempt: state.Integrations[0].Attempts, Kind: "conflict", BaseSHA: defaultHead, RequestedAt: "2026-07-16T00:00:00Z", CompletedAt: "2026-07-16T00:01:00Z", Outcome: "agent_returned",
	})
	if !recoverableBatchResolutionWork(state, defaultHead) {
		t.Fatal("pre-commit applying intent was not recognized as recoverable")
	}

	calls := 0
	got, err := (BatchAgentResolver{
		Store: &recordingBatchTransitionStore{}, Service: NewService(fixture.repoRoot, nil),
		Agent: batchResolutionAgentFunc(func(_ context.Context, root, _ string) (string, error) {
			calls++
			return batchResolutionJSON("resolved after applying interruption"), os.WriteFile(filepath.Join(root, "README.md"), []byte("combined\n"), 0o600)
		}),
	}).Resolve(context.Background(), state, integrationRoot, BatchResolveOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 || got.State.Integrations[0].Status != batchIntegrationApplied {
		t.Fatalf("pre-commit intent was not safely retried: calls=%d state=%#v", calls, got.State.Integrations[0])
	}
}

func TestBatchAgentRejectsChangesToAnotherCandidateSourceRef(t *testing.T) {
	fixture, sourceHead, defaultHead, integrationRoot := batchAgentConflictFixture(t)
	state := batchAgentDeferredState(fixture, sourceHead, defaultHead)
	protectedBranch := "tao/protected-candidate"
	runRealGit(t, fixture.repoRoot, "branch", protectedBranch, sourceHead)
	state.Candidates = append(state.Candidates, BatchCandidate{PlanID: "plan-b", Branch: protectedBranch, SourceTip: sourceHead})

	got, err := (BatchAgentResolver{
		Store: &recordingBatchTransitionStore{}, Service: NewService(fixture.repoRoot, nil),
		Agent: batchResolutionAgentFunc(func(_ context.Context, root, _ string) (string, error) {
			runRealGit(t, root, "update-ref", "refs/heads/"+protectedBranch, defaultHead)
			return batchResolutionJSON("resolved README"), os.WriteFile(filepath.Join(root, "README.md"), []byte("combined\n"), 0o600)
		}),
	}).Resolve(context.Background(), state, integrationRoot, BatchResolveOptions{})
	if err == nil || got.State.Status != BatchStatusBlocked || !strings.Contains(err.Error(), "protected Git refs") {
		t.Fatalf("expected protected candidate ref change to block resolution, got state=%#v err=%v", got.State, err)
	}
	assertRef(t, fixture.repoRoot, fixture.defaultBranch, defaultHead)
	assertRef(t, fixture.repoRoot, fixture.planBranch, sourceHead)
	assertRef(t, fixture.repoRoot, protectedBranch, sourceHead)
}

func TestBatchAgentRecordsEarlyDeferralAfterLaterAppliedCandidate(t *testing.T) {
	fixture, sourceA, defaultHead, integrationRoot := batchAgentConflictFixture(t)
	planBRoot := filepath.Join(filepath.Dir(fixture.repoRoot), "plan-b")
	runRealGit(t, fixture.repoRoot, "worktree", "add", "-b", "tao/plan-b", planBRoot, defaultHead)
	if err := os.WriteFile(filepath.Join(planBRoot, "plan-b.txt"), []byte("plan b\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runRealGit(t, planBRoot, "add", "plan-b.txt")
	runRealGit(t, planBRoot, "commit", "-m", "plan b")
	sourceB := strings.TrimSpace(realGitOutput(t, planBRoot, "rev-parse", "HEAD"))

	state := BatchState{
		Schema: BatchStateSchema, ID: "batch-agent-order", Status: BatchStatusPlanned,
		RepoRoot: fixture.repoRoot, DefaultBranch: fixture.defaultBranch, DefaultStartSHA: defaultHead,
		ChosenOrder: []string{"plan-a", "plan-b"},
		Candidates: []BatchCandidate{
			{PlanID: "plan-a", PlanTitle: "agent candidate", RepoRoot: fixture.repoRoot, Branch: fixture.planBranch, SourceTip: sourceA, DefaultBranch: fixture.defaultBranch, DefaultStartSHA: defaultHead, CommitMessage: testBatchCommitMessage("plan-a", sourceA)},
			{PlanID: "plan-b", PlanTitle: "clean candidate", RepoRoot: fixture.repoRoot, Branch: "tao/plan-b", SourceTip: sourceB, DefaultBranch: fixture.defaultBranch, DefaultStartSHA: defaultHead, CommitMessage: testBatchCommitMessage("plan-b", sourceB)},
		},
	}
	store := &recordingBatchTransitionStore{}
	service := NewService(fixture.repoRoot, nil)
	integrated, err := (BatchIntegrator{Store: store, Service: service}).Integrate(context.Background(), state, integrationRoot, BatchIntegrateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(integrated.Deferred) != 1 || integrated.Deferred[0].PlanID != "plan-a" || len(integrated.Applied) != 1 || integrated.Applied[0] != "plan-b" {
		t.Fatalf("unexpected initial integration: %#v", integrated)
	}

	resolved, err := (BatchAgentResolver{Store: store, Service: service, Agent: batchResolutionAgentFunc(func(_ context.Context, root, _ string) (string, error) {
		return batchResolutionJSON("resolved early candidate"), os.WriteFile(filepath.Join(root, "README.md"), []byte("combined\n"), 0o600)
	})}).Resolve(context.Background(), integrated.State, integrationRoot, BatchResolveOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got := []string{resolved.State.Integrations[0].PlanID, resolved.State.Integrations[1].PlanID}; strings.Join(got, ",") != "plan-b,plan-a" {
		t.Fatalf("integration records are not in commit-chain order: %v", got)
	}
	if resolved.State.Integrations[1].IntegrationBaseSHA != resolved.State.Integrations[0].IntegrationSHA {
		t.Fatalf("integration chain is not contiguous: %#v", resolved.State.Integrations)
	}
	if drifts := validatePersistedProgress(resolved.State); len(drifts) != 0 {
		t.Fatalf("resolved progress does not validate: %#v", drifts)
	}
	resolved.State.Review = &BatchReview{}
	landing, err := landingIntentFromState(resolved.State)
	if err != nil {
		t.Fatal(err)
	}
	if got := landing.Plans[0].PlanID + "," + landing.Plans[1].PlanID; got != "plan-b,plan-a" {
		t.Fatalf("landing does not use commit-chain order: %s", got)
	}
}

func TestBatchAgentRepairsVerificationFailureBeforeTaoCommit(t *testing.T) {
	fixture := newRealGitWorktree(t)
	if err := os.WriteFile(filepath.Join(fixture.worktreePath, "value.txt"), []byte("bad\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runRealGit(t, fixture.worktreePath, "add", "value.txt")
	runRealGit(t, fixture.worktreePath, "commit", "-m", "add value")
	sourceHead := realGitOutput(t, fixture.worktreePath, "rev-parse", "HEAD")
	defaultHead := realGitOutput(t, fixture.repoRoot, "rev-parse", "HEAD")
	integrationRoot := filepath.Join(filepath.Dir(fixture.repoRoot), "verification-integration")
	runRealGit(t, fixture.repoRoot, "worktree", "add", "-b", "tao/integration/verify", integrationRoot, defaultHead)
	state := batchAgentDeferredState(fixture, sourceHead, defaultHead)
	state.Integrations[0].ConflictFiles = nil
	state.Integrations[0].DeferredReason = "verification failed"
	state.Integrations[0].VerificationOutput = "wanted good"
	got, err := (BatchAgentResolver{Store: &recordingBatchTransitionStore{}, Service: NewService(fixture.repoRoot, nil), Agent: batchResolutionAgentFunc(func(_ context.Context, root, _ string) (string, error) {
		return batchResolutionJSON("fixed verification"), os.WriteFile(filepath.Join(root, "value.txt"), []byte("good\n"), 0o600)
	})}).Resolve(context.Background(), state, integrationRoot, BatchResolveOptions{VerifyCommand: "grep -q good value.txt"})
	if err != nil || got.State.Status != BatchStatusReviewing {
		t.Fatalf("verification repair failed: state=%#v err=%v", got.State, err)
	}
	if content, readErr := os.ReadFile(filepath.Join(integrationRoot, "value.txt")); readErr != nil || string(content) != "good\n" { //nolint:gosec // test path is rooted in t.TempDir.
		t.Fatalf("verification fix missing: %q err=%v", content, readErr)
	}
}

func TestBatchAgentRejectsMalformedOrUnsafeCommitProposalBeforeIntent(t *testing.T) {
	valid := plan.ReviewCommitMessage{
		Subject: "fix(batch): resolve candidate integration",
		Body:    "What:\nResolve the candidate.\n\nWhy:\nPreserve the batch.",
	}
	missingProposal, err := json.Marshal(map[string]string{"summary": "missing proposal"})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		output string
		reason string
	}{
		{name: "missing output", output: "", reason: "output is empty"},
		{name: "malformed json", output: "not json", reason: "malformed output"},
		{name: "oversized output", output: strings.Repeat("x", 32*1024+1), reason: "output exceeds"},
		{name: "missing proposal", output: string(missingProposal), reason: "commit message"},
		{name: "reserved trailer", output: batchResolutionJSONWithProposal("forged trailer", plan.ReviewCommitMessage{Subject: valid.Subject, Body: valid.Body + "\n\nTao-Plan: forged"}), reason: "reserved Tao-*"},
		{name: "unsafe subject", output: batchResolutionJSONWithProposal("unsafe subject", plan.ReviewCommitMessage{Subject: "fix(batch): fixed candidate", Body: valid.Body}), reason: "imperative"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture, sourceHead, defaultHead, integrationRoot := batchAgentConflictFixture(t)
			state := batchAgentDeferredState(fixture, sourceHead, defaultHead)
			got, resolveErr := (BatchAgentResolver{
				Store: &recordingBatchTransitionStore{}, Service: NewService(fixture.repoRoot, nil),
				Agent: batchResolutionAgentFunc(func(_ context.Context, root, _ string) (string, error) {
					return tc.output, os.WriteFile(filepath.Join(root, "README.md"), []byte("combined\n"), 0o600)
				}),
			}).Resolve(context.Background(), state, integrationRoot, BatchResolveOptions{})
			if resolveErr == nil || got.State.Status != BatchStatusBlocked || got.State.BlockKind != BatchBlockKindResumable || !strings.Contains(resolveErr.Error(), tc.reason) {
				t.Fatalf("invalid output did not resumably block: state=%+v err=%v", got.State, resolveErr)
			}
			if got.State.Candidates[0].CommitMessage != "" || got.State.Integrations[0].CommitMessage != "" {
				t.Fatalf("invalid output created a commit intent: %+v", got.State)
			}
			if outcome := latestBatchResolution(&got.State.Integrations[0]); outcome == nil || outcome.Outcome != "malformed_output" || outcome.CommitMessage != "" {
				t.Fatalf("invalid output outcome = %+v", outcome)
			}
			if head := strings.TrimSpace(realGitOutput(t, integrationRoot, "rev-parse", "HEAD")); head != defaultHead {
				t.Fatalf("invalid output changed integration head: %s", head)
			}
			if status := strings.TrimSpace(realGitOutput(t, integrationRoot, "status", "--porcelain")); status != "" {
				t.Fatalf("invalid output left staged or working-tree changes: %q", status)
			}
		})
	}
}

func TestBatchAgentNoProgressBlocksAndRestoresIntegration(t *testing.T) {
	fixture, sourceHead, defaultHead, integrationRoot := batchAgentConflictFixture(t)
	state := batchAgentDeferredState(fixture, sourceHead, defaultHead)
	got, err := (BatchAgentResolver{Store: &recordingBatchTransitionStore{}, Service: NewService(fixture.repoRoot, nil), Agent: batchResolutionAgentFunc(func(context.Context, string, string) (string, error) {
		return batchResolutionJSON("nothing changed"), nil
	})}).Resolve(context.Background(), state, integrationRoot, BatchResolveOptions{MaxAttempts: 2})
	if err == nil || got.State.Status != BatchStatusBlocked || !strings.Contains(got.State.BlockedReason, "unresolved conflicts") {
		t.Fatalf("expected blocked no-op agent, got state=%#v err=%v", got.State, err)
	}
	if head := strings.TrimSpace(realGitOutput(t, integrationRoot, "rev-parse", "HEAD")); head != defaultHead {
		t.Fatalf("blocked resolution left integration moved: %s", head)
	}
	if status := realGitOutput(t, integrationRoot, "status", "--porcelain"); strings.TrimSpace(status) != "" {
		t.Fatalf("blocked resolution left dirty integration: %q", status)
	}
}

func batchAgentConflictFixture(t *testing.T) (realGitWorktree, string, string, string) {
	t.Helper()
	fixture := newRealGitWorktree(t)
	if err := os.WriteFile(filepath.Join(fixture.worktreePath, "README.md"), []byte("source\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runRealGit(t, fixture.worktreePath, "add", "README.md")
	runRealGit(t, fixture.worktreePath, "commit", "-m", "source")
	sourceHead := strings.TrimSpace(realGitOutput(t, fixture.worktreePath, "rev-parse", "HEAD"))
	if err := os.WriteFile(filepath.Join(fixture.repoRoot, "README.md"), []byte("default\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runRealGit(t, fixture.repoRoot, "add", "README.md")
	runRealGit(t, fixture.repoRoot, "commit", "-m", "default")
	defaultHead := strings.TrimSpace(realGitOutput(t, fixture.repoRoot, "rev-parse", "HEAD"))
	integrationRoot := filepath.Join(filepath.Dir(fixture.repoRoot), "integration")
	runRealGit(t, fixture.repoRoot, "worktree", "add", "-b", "tao/integration/test", integrationRoot, defaultHead)
	return fixture, sourceHead, defaultHead, integrationRoot
}

func batchAgentDeferredState(fixture realGitWorktree, sourceHead, defaultHead string) BatchState {
	deferral := &BatchDeferral{PlanID: "plan-a", Reason: "squash conflict"}
	return BatchState{Schema: BatchStateSchema, ID: "batch-agent", Status: BatchStatusResolving, RepoRoot: fixture.repoRoot, DefaultBranch: fixture.defaultBranch, DefaultStartSHA: defaultHead, IntegrationHead: defaultHead, ChosenOrder: []string{"plan-a"}, Candidates: []BatchCandidate{{PlanID: "plan-a", PlanTitle: "agent candidate", RepoRoot: fixture.repoRoot, Branch: fixture.planBranch, SourceTip: sourceHead, DefaultBranch: fixture.defaultBranch, DefaultStartSHA: defaultHead, Deferred: deferral}}, Integrations: []BatchIntegration{{PlanID: "plan-a", SourceHead: sourceHead, IntegrationBaseSHA: defaultHead, Status: batchIntegrationDeferred, DeferredReason: "squash conflict", ConflictFiles: []string{"README.md"}, Attempts: 1}}}
}

func TestConflictMarkersRemainIgnoresMarkerLikeSource(t *testing.T) {
	root := t.TempDir()
	source := "package scan\n\n" +
		"var l = \"" + strings.Repeat("<", 7) + "\"\n" +
		"var r = \"" + strings.Repeat(">", 7) + "\"\n"
	if err := os.WriteFile(filepath.Join(root, "scanner.go"), []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	if conflictMarkersRemain(root, []string{"scanner.go"}) {
		t.Fatal("marker-like string literals must not count as unresolved conflicts")
	}

	conflicted := "before\n" + strings.Repeat("<", 7) + " HEAD\nours\n=======\ntheirs\n" + strings.Repeat(">", 7) + " tao/plan-a\n"
	if err := os.WriteFile(filepath.Join(root, "conflicted.txt"), []byte(conflicted), 0o600); err != nil {
		t.Fatal(err)
	}
	if !conflictMarkersRemain(root, []string{"conflicted.txt"}) {
		t.Fatal("real line-anchored conflict markers must be detected")
	}
}

func TestValidateAgentEditsScansOnlyRequestedFiles(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "changed.txt"), []byte("safe edit\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	conflicted := strings.Repeat("<", 7) + " HEAD\nours\n=======\ntheirs\n" + strings.Repeat(">", 7) + " branch\n"
	if err := os.WriteFile(filepath.Join(root, "unchanged.txt"), []byte(conflicted), 0o600); err != nil {
		t.Fatal(err)
	}

	validation := validateAgentEdits(root, []string{"changed.txt"}, []string{"changed.txt"})
	if validation.issue != agentEditIssueNone {
		t.Fatalf("unchanged marker file affected validation: %+v", validation)
	}
	validation = validateAgentEdits(root, []string{"changed.txt"}, []string{"unchanged.txt"})
	if validation.issue != agentEditIssueConflictMarkers || !reflect.DeepEqual(validation.markerPaths, []string{"unchanged.txt"}) {
		t.Fatalf("requested marker file was not reported: %+v", validation)
	}
	validation = validateAgentEdits(root, []string{"missing.txt"}, []string{"missing.txt"})
	if validation.issue != agentEditIssueUnscannablePaths || validation.scanErr == nil || !strings.Contains(validation.scanErr.Error(), "missing.txt") {
		t.Fatalf("unreadable requested path did not fail closed: %+v", validation)
	}
}

func TestParseConcretePorcelainChangesDecodesQuotedPaths(t *testing.T) {
	status := " M \"quoted\\tname.txt\"\n D deleted.txt\n?? nested/file.txt\n"
	got, err := parseConcretePorcelainV1(status)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"deleted.txt", "nested/file.txt", "quoted\tname.txt"}; !slices.Equal(got.changedPaths, want) {
		t.Fatalf("changed paths = %q, want %q", got.changedPaths, want)
	}
	if want := []string{"nested/file.txt", "quoted\tname.txt"}; !slices.Equal(got.markerScanPaths, want) {
		t.Fatalf("marker scan paths = %q, want %q", got.markerScanPaths, want)
	}
}

func TestParsePorcelainV1ZPreservesConcreteUntrackedAndRenamePaths(t *testing.T) {
	status := "R  renamed name.txt\x00old name.txt\x00?? new dir/deeper/marker\nfile.txt\x00D  deleted.txt\x00"
	got, err := parsePorcelainV1Z(status)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"deleted.txt", "new dir/deeper/marker\nfile.txt", "old name.txt", "renamed name.txt"}; !slices.Equal(got.changedPaths, want) {
		t.Fatalf("changed paths = %q, want %q", got.changedPaths, want)
	}
	if want := []string{"new dir/deeper/marker\nfile.txt", "renamed name.txt"}; !slices.Equal(got.markerScanPaths, want) {
		t.Fatalf("marker scan paths = %q, want %q", got.markerScanPaths, want)
	}
}

func TestConcretePorcelainChangesRejectsCollapsedFallbackStatus(t *testing.T) {
	_, err := concretePorcelainChanges(context.Background(), &fakeGitClient{status: "?? nested/\n"})
	if err == nil || !strings.Contains(err.Error(), "collapsed untracked directory") {
		t.Fatalf("collapsed fallback status did not fail closed: %v", err)
	}
}

func TestCollectConflictFilesOnlyUnmergedPaths(t *testing.T) {
	fixture := newRealGitWorktree(t)
	if err := os.WriteFile(filepath.Join(fixture.worktreePath, "README.md"), []byte("plan\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fixture.worktreePath, "clean.txt"), []byte("plan-only\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runRealGit(t, fixture.worktreePath, "add", ".")
	runRealGit(t, fixture.worktreePath, "commit", "-m", "source")
	if err := os.WriteFile(filepath.Join(fixture.repoRoot, "README.md"), []byte("default\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runRealGit(t, fixture.repoRoot, "add", "README.md")
	runRealGit(t, fixture.repoRoot, "commit", "-m", "default")
	squash := exec.Command("git", "merge", "--squash", fixture.planBranch) //nolint:gosec // G204: test invokes fixed git command with test-controlled args.
	squash.Dir = fixture.repoRoot
	if err := squash.Run(); err == nil {
		t.Fatal("expected squash conflict")
	}
	files := collectConflictFiles(context.Background(), gitops.NewClient(fixture.repoRoot, nil))
	if !reflect.DeepEqual(files, []string{"README.md"}) {
		t.Fatalf("conflict files = %v, want only the unmerged path [README.md]", files)
	}
}
