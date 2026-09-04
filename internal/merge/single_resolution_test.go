package merge

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/iamseth/tao/internal/agent"
	"github.com/iamseth/tao/internal/agentsession"
	"github.com/iamseth/tao/internal/gitops"
	"github.com/iamseth/tao/internal/plan"
)

type recordingSingleResolutionStore struct {
	current   plan.SingleMergeCommitIntent
	records   int
	advances  int
	onAdvance func(plan.SingleMergeResolution) error
	rearmErr  error
}

func (s *recordingSingleResolutionStore) RecordSingleMergeResolution(expected plan.SingleMergeCommitIntent, resolution plan.SingleMergeResolution) error {
	if !reflect.DeepEqual(s.current, expected) {
		return errors.New("record compare-and-set mismatch")
	}
	candidate := expected
	candidate.Resolution = &resolution
	if err := candidate.Validate(); err != nil {
		return err
	}
	s.records++
	s.current = candidate
	return nil
}

func (s *recordingSingleResolutionStore) AdvanceSingleMergeResolution(expected plan.SingleMergeCommitIntent, resolution plan.SingleMergeResolution) error {
	if !reflect.DeepEqual(s.current, expected) {
		return errors.New("advance compare-and-set mismatch")
	}
	candidate := expected
	candidate.Resolution = &resolution
	if err := candidate.Validate(); err != nil {
		return err
	}
	if s.onAdvance != nil {
		if err := s.onAdvance(resolution); err != nil {
			return err
		}
	}
	s.advances++
	s.current = candidate
	return nil
}

func (s *recordingSingleResolutionStore) RearmSingleMergeResolution(expected plan.SingleMergeCommitIntent, failure plan.SingleMergeStartupFailure) error {
	if !reflect.DeepEqual(s.current, expected) {
		return errors.New("rearm compare-and-set mismatch")
	}
	if err := failure.Validate(); err != nil {
		return err
	}
	if s.rearmErr != nil {
		return s.rearmErr
	}
	s.current.Resolution = nil
	return nil
}

type resetFailingSingleResolutionGit struct {
	GitClient
	err error
}

func (g resetFailingSingleResolutionGit) ResetHard(context.Context, string) error { return g.err }

type preflightSingleMergeAgent struct {
	preflightErr error
	calls        int
	resolve      func(context.Context, BatchAgentSessionRequest) (BatchAgentSessionResult, error)
}

func (a *preflightSingleMergeAgent) Preflight(context.Context, BatchAgentSessionRequest) error {
	return a.preflightErr
}

func (a *preflightSingleMergeAgent) Resolve(ctx context.Context, request BatchAgentSessionRequest) (BatchAgentSessionResult, error) {
	a.calls++
	return a.resolve(ctx, request)
}

func TestGuardedSingleConflictResolverPreflightsBeforeRequestedEvidence(t *testing.T) {
	_, request, git := preparedSingleResolutionFixture(t)
	store := &recordingSingleResolutionStore{current: request.Intent}
	probeErr := errors.New("bubblewrap is unavailable")
	agent := &preflightSingleMergeAgent{preflightErr: probeErr}
	resolver := GuardedSingleConflictResolver{Git: git, Recorder: store, Agent: agent}

	if _, err := resolver.ResolveConflict(context.Background(), request); !errors.Is(err, ErrSingleResolutionPreflight) || !errors.Is(err, probeErr) {
		t.Fatalf("unavailable-confinement error = %v", err)
	}
	if store.records != 0 || store.current.Resolution != nil || agent.calls != 0 {
		t.Fatalf("preflight failure persisted or started provider: records=%d intent=%+v calls=%d", store.records, store.current, agent.calls)
	}
}

func TestGuardedSingleConflictResolverRearmsOnlyProvenPreAcceptanceFailure(t *testing.T) {
	for _, tc := range []struct {
		name       string
		acceptance agent.PromptAcceptance
		wantRearm  bool
	}{
		{name: "explicit rejection", acceptance: agent.PromptAcceptanceRejected, wantRearm: true},
		{name: "startup not transmitted", acceptance: agent.PromptAcceptanceNotTransmitted, wantRearm: true},
		{name: "accepted", acceptance: agent.PromptAcceptanceAccepted},
		{name: "unknown", acceptance: agent.PromptAcceptanceUnknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture, request, git := preparedSingleResolutionFixture(t)
			store := &recordingSingleResolutionStore{current: request.Intent}
			agentSession := &preflightSingleMergeAgent{resolve: func(context.Context, BatchAgentSessionRequest) (BatchAgentSessionResult, error) {
				return BatchAgentSessionResult{Provider: agentsession.Result{PromptAcceptance: tc.acceptance}}, errors.New("fixture provider startup failed")
			}}
			resolver := GuardedSingleConflictResolver{Git: git, Recorder: store, Agent: agentSession}
			result, err := resolver.ResolveConflict(context.Background(), request)
			startup, ok := errors.AsType[*SingleResolutionStartupError](err)
			if !errors.Is(err, ErrSingleResolutionRejected) || !ok {
				t.Fatalf("startup failure = %+v, %v", result, err)
			}
			if tc.wantRearm {
				if startup.Authority != SingleResolutionAuthorityRearmed || store.current.Resolution != nil || result.Intent.Resolution != nil {
					t.Fatalf("proven pre-acceptance did not rearm: startup=%+v store=%+v result=%+v", startup, store.current, result)
				}
			} else if startup.Authority != SingleResolutionAuthorityConsumed || store.current.Resolution == nil || result.Intent.Resolution == nil {
				t.Fatalf("ambiguous/accepted failure did not remain consumed: startup=%+v store=%+v result=%+v", startup, store.current, result)
			}
			if head := strings.TrimSpace(realGitOutput(t, fixture.repoRoot, "rev-parse", "HEAD")); head != request.Intent.DefaultParent {
				t.Fatalf("startup rollback left HEAD at %s", head)
			}
			if status := strings.TrimSpace(realGitOutput(t, fixture.repoRoot, "status", "--porcelain")); status != "" {
				t.Fatalf("startup rollback left dirty worktree: %q", status)
			}
		})
	}
}

func TestGuardedSingleConflictResolverPreservesRequestedAuthorityWhenRearmSettlementFails(t *testing.T) {
	for _, tc := range []struct {
		name     string
		wrapGit  func(GitClient) GitClient
		rearmErr error
		want     string
	}{
		{
			name: "rollback failure",
			wrapGit: func(git GitClient) GitClient {
				return resetFailingSingleResolutionGit{GitClient: git, err: errors.New("fixture reset failure")}
			},
			want: "restore durable parent",
		},
		{name: "compare and set failure", rearmErr: errors.New("fixture concurrent state drift"), want: "persist exact request rearm"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, request, git := preparedSingleResolutionFixture(t)
			store := &recordingSingleResolutionStore{current: request.Intent, rearmErr: tc.rearmErr}
			guardedGit := func() GitClient {
				if tc.wrapGit != nil {
					return tc.wrapGit(git)
				}
				return git
			}()
			resolver := GuardedSingleConflictResolver{Git: guardedGit, Recorder: store, Agent: &preflightSingleMergeAgent{resolve: func(context.Context, BatchAgentSessionRequest) (BatchAgentSessionResult, error) {
				return BatchAgentSessionResult{Provider: agentsession.Result{PromptAcceptance: agent.PromptAcceptanceRejected}}, errors.New("explicit prompt rejection")
			}}}
			result, err := resolver.ResolveConflict(context.Background(), request)
			startup, ok := errors.AsType[*SingleResolutionStartupError](err)
			if !ok || startup.Authority != SingleResolutionAuthorityConsumed || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("failed settlement = %+v, %v", result, err)
			}
			if store.current.Resolution == nil || result.Intent.Resolution == nil || store.current.Resolution.Phase != plan.SingleMergeResolutionPhaseRequested {
				t.Fatalf("failed settlement cleared consumed request: store=%+v result=%+v", store.current, result.Intent)
			}
		})
	}
}

func TestGuardedSingleConflictResolverExplicitRerunSucceedsAfterRearm(t *testing.T) {
	fixture, request, git := preparedSingleResolutionFixture(t)
	store := &recordingSingleResolutionStore{current: request.Intent}
	resolver := GuardedSingleConflictResolver{Git: git, Recorder: store, Agent: &preflightSingleMergeAgent{resolve: func(context.Context, BatchAgentSessionRequest) (BatchAgentSessionResult, error) {
		return BatchAgentSessionResult{Provider: agentsession.Result{PromptAcceptance: agent.PromptAcceptanceRejected}}, errors.New("explicit prompt rejection")
	}}}
	if _, err := resolver.ResolveConflict(context.Background(), request); err == nil || store.current.Resolution != nil {
		t.Fatalf("first explicit attempt did not rearm: %v / %+v", err, store.current)
	}

	request.Intent = store.current
	if err := git.MergeSquash(context.Background(), fixture.planBranch); err == nil {
		t.Fatal("explicit rerun fixture unexpectedly merged without conflict")
	}
	resolver.Agent = batchResolutionAgentFunc(func(_ context.Context, root, _ string) (string, error) {
		return batchResolutionJSON("resolved on explicit rerun"), os.WriteFile(filepath.Join(root, "README.md"), []byte("combined\n"), 0o600)
	})
	result, err := resolver.ResolveConflict(context.Background(), request)
	if err != nil || result.Intent.Resolution == nil || result.Intent.Resolution.Phase != plan.SingleMergeResolutionPhaseResolved {
		t.Fatalf("explicit rerun result = %+v, %v", result, err)
	}
}

func TestGuardedSingleConflictResolverProviderProbeIsReadOnlyAndOutputBoundedBeforeRequestedEvidence(t *testing.T) {
	tests := []struct {
		name             string
		script           string
		wantOutput       string
		dontWantOutput   string
		wantBoundedError bool
	}{
		{
			name: "mutating version probe",
			script: `#!/bin/sh
if ! printf runtime >"$TMPDIR/version-probe-runtime"; then
  printf 'runtime-write-denied\n' >&2
  exit 41
fi
printf 'runtime-write-succeeded\n' >&2
if printf 'probe mutation\n' >"$TAO_TEST_VERSION_PROBE_TARGET"; then
  printf 'worktree-write-succeeded\n' >&2
else
  printf 'worktree-write-denied\n' >&2
fi
exit 23
`,
			wantOutput:     "runtime-write-succeeded",
			dontWantOutput: "worktree-write-succeeded",
		},
		{
			name: "oversized version output",
			script: `#!/bin/sh
i=0
while [ "$i" -lt 8192 ]; do
  printf '0123456789abcdef'
  i=$((i + 1))
done
printf 'version-output-tail'
exit 23
`,
			dontWantOutput:   "version-output-tail",
			wantBoundedError: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("TAO_AGENT", "claude")
			probeRoot, protectedRoot := t.TempDir(), t.TempDir()
			if err := probeSingleMergeFilesystemConfinement(context.Background(), singleMergeFilesystemConfinement{
				protectedPaths: []string{protectedRoot}, integrationRoot: probeRoot, allowEdits: true,
			}, "/usr/bin/true"); err != nil {
				t.Skipf("OS confinement is unavailable for provider launch regression: %v", err)
			}

			fixture, request, git := preparedSingleResolutionFixture(t)
			target := filepath.Join(fixture.repoRoot, "README.md")
			t.Setenv("TAO_TEST_VERSION_PROBE_TARGET", target)
			provider := filepath.Join(t.TempDir(), "claude")
			if err := os.WriteFile(provider, []byte(tt.script), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(provider, 0o700); err != nil { //nolint:gosec // executable mode is required for the fixture provider.
				t.Fatal(err)
			}

			starts := 0
			session, err := NewSingleMergeAgentSession(SingleMergeAgentSessionConfig{
				ProviderLookPath: func(string) (string, error) { return provider, nil },
				ProcessStarter: func(context.Context, string, string, []string) (agent.Process, error) {
					starts++
					return nil, errors.New("unexpected attributed provider start")
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			store := &recordingSingleResolutionStore{current: request.Intent}
			resolver := GuardedSingleConflictResolver{Git: git, Recorder: store, Agent: session}
			_, resolveErr := resolver.ResolveConflict(context.Background(), request)
			if !errors.Is(resolveErr, ErrSingleResolutionPreflight) {
				t.Fatalf("provider probe error = %v, want preflight failure", resolveErr)
			}
			if starts != 0 || store.records != 0 || store.current.Resolution != nil {
				t.Fatalf("probe started attributed provider or persisted requested evidence: starts=%d records=%d intent=%+v", starts, store.records, store.current)
			}
			errorText := resolveErr.Error()
			if tt.wantOutput != "" && !strings.Contains(errorText, tt.wantOutput) {
				t.Fatalf("provider probe did not retain expected bounded diagnostic %q: %v", tt.wantOutput, resolveErr)
			}
			if tt.name == "mutating version probe" && !strings.Contains(errorText, "worktree-write-denied") {
				t.Fatalf("provider probe did not prove the integration worktree was read-only: %v", resolveErr)
			}
			if tt.dontWantOutput != "" && strings.Contains(errorText, tt.dontWantOutput) {
				t.Fatalf("provider probe retained forbidden or over-limit output %q: %v", tt.dontWantOutput, resolveErr)
			}
			if tt.wantBoundedError && len(errorText) > maxSingleMergeConfinementProbeOutputBytes+512 {
				t.Fatalf("provider probe retained an unbounded diagnostic: got %d bytes", len(errorText))
			}
		})
	}
}

func TestGuardedSingleConflictResolverRejectsCleanTrackedExternalHardLink(t *testing.T) {
	t.Setenv("TAO_AGENT", "claude")
	var external string
	fixture, request, git := preparedSingleResolutionFixtureWithSetup(t, func(fixture realGitWorktree) {
		tracked := filepath.Join(fixture.repoRoot, "clean-tracked.txt")
		if err := os.WriteFile(tracked, []byte("clean tracked contents\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		runRealGit(t, fixture.repoRoot, "add", "clean-tracked.txt")
		runRealGit(t, fixture.repoRoot, "commit", "-m", "add clean tracked file")
		external = filepath.Join(filepath.Dir(fixture.repoRoot), "external-alias.txt")
		if err := os.Link(tracked, external); err != nil {
			t.Skipf("filesystem does not support hard links: %v", err)
		}
	})
	store := &recordingSingleResolutionStore{current: request.Intent}
	starts := 0
	agentSession, err := NewSingleMergeAgentSession(SingleMergeAgentSessionConfig{
		ConfinementProbe: func() error { return nil },
		ProcessStarter: func(context.Context, string, string, []string) (agent.Process, error) {
			starts++
			if writeErr := os.WriteFile(filepath.Join(fixture.repoRoot, "clean-tracked.txt"), []byte("resolver mutation\n"), 0o600); writeErr != nil {
				return nil, writeErr
			}
			return nil, errors.New("unexpected provider start")
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	resolver := GuardedSingleConflictResolver{Git: git, Recorder: store, Agent: agentSession}

	_, err = resolver.ResolveConflict(context.Background(), request)
	if !errors.Is(err, ErrSingleResolutionPreflight) || !strings.Contains(err.Error(), `multiply linked regular file "clean-tracked.txt"`) {
		t.Fatalf("hard-link preflight error = %v", err)
	}
	if starts != 0 || store.records != 0 || store.current.Resolution != nil {
		t.Fatalf("hard-link preflight started or persisted: starts=%d records=%d intent=%+v", starts, store.records, store.current)
	}
	if contents, readErr := os.ReadFile(external); readErr != nil || string(contents) != "clean tracked contents\n" { //nolint:gosec // fixture-owned external alias is the security assertion.
		t.Fatalf("resolver changed external inode: %q, %v", contents, readErr)
	}
	if status := strings.TrimSpace(realGitOutput(t, fixture.repoRoot, "status", "--porcelain")); status != "" {
		t.Fatalf("hard-link rejection left integration dirty: %q", status)
	}
}

func TestMergeCapabilityPreflightFailureRestoresRetryableBoundary(t *testing.T) {
	tests := []struct {
		name                       string
		providerLookPath           agent.LookPath
		setupProviderLookPath      func(*testing.T) agent.LookPath
		confinementProbe           func() error
		requiresWorkingConfinement bool
		wantError                  string
	}{
		{
			name: "missing provider",
			providerLookPath: func(name string) (string, error) {
				return "", fmt.Errorf("%s missing: %w", name, exec.ErrNotFound)
			},
			confinementProbe: func() error { return errors.New("confinement probe must not run") },
			wantError:        `resolve provider executable "claude"`,
		},
		{
			name:             "confinement executable cannot create sandbox",
			providerLookPath: func(name string) (string, error) { return "/installed/" + name, nil },
			confinementProbe: func() error { return errors.New("bwrap: creating new namespace failed") },
			wantError:        "creating new namespace failed",
		},
		{
			name: "provider has missing interpreter",
			setupProviderLookPath: func(t *testing.T) agent.LookPath {
				provider := filepath.Join(t.TempDir(), "claude")
				if err := os.WriteFile(provider, []byte("#!/tao/definitely-missing-interpreter\nexit 0\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(provider, 0o700); err != nil { //nolint:gosec // executable mode is the launch precondition under test.
					t.Fatal(err)
				}
				if err := exec.Command(provider, "--version").Run(); !errors.Is(err, os.ErrNotExist) { //nolint:gosec // fixture path must exercise the kernel's missing-interpreter launch failure.
					t.Fatalf("invalid-interpreter provider launch error = %v, want file-not-found", err)
				}
				return func(string) (string, error) { return provider, nil }
			},
			requiresWorkingConfinement: true,
			wantError:                  "probe provider filesystem confinement",
		},
		{
			name: "provider is on noexec mount",
			setupProviderLookPath: func(t *testing.T) agent.LookPath {
				if runtime.GOOS != "linux" {
					t.Skip("no writable noexec fixture mount is available on this platform")
				}
				root, err := os.MkdirTemp("/dev/shm", "tao-noexec-provider-*")
				if err != nil {
					t.Skipf("create provider on expected noexec mount: %v", err)
				}
				t.Cleanup(func() { _ = os.RemoveAll(root) })
				provider := filepath.Join(root, "claude")
				if err := os.WriteFile(provider, []byte("#!/bin/sh\nexit 0\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(provider, 0o700); err != nil { //nolint:gosec // executable mode is the launch precondition under test.
					t.Fatal(err)
				}
				if err := exec.Command(provider, "--version").Run(); !errors.Is(err, os.ErrPermission) { //nolint:gosec // fixture path must prove the selected mount rejects execution.
					if err == nil {
						t.Skip("/dev/shm permits executable files on this host")
					}
					t.Skipf("/dev/shm provider did not fail with noexec permission denial: %v", err)
				}
				return func(string) (string, error) { return provider, nil }
			},
			requiresWorkingConfinement: true,
			wantError:                  "probe provider filesystem confinement",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("TAO_AGENT", "claude")
			providerLookPath := tc.providerLookPath
			if tc.setupProviderLookPath != nil {
				providerLookPath = tc.setupProviderLookPath(t)
			}
			if tc.requiresWorkingConfinement {
				probeRoot, protectedRoot := t.TempDir(), t.TempDir()
				if err := probeSingleMergeFilesystemConfinement(context.Background(), singleMergeFilesystemConfinement{
					protectedPaths: []string{protectedRoot}, integrationRoot: probeRoot, allowEdits: true,
				}, "/usr/bin/true"); err != nil {
					t.Skipf("OS confinement is unavailable for provider launch regression: %v", err)
				}
			}
			fixture, _, _, _ := batchAgentConflictFixture(t)
			sourceHead := strings.TrimSpace(realGitOutput(t, fixture.worktreePath, "rev-parse", "HEAD"))
			defaultHead := strings.TrimSpace(realGitOutput(t, fixture.repoRoot, "rev-parse", "HEAD"))
			base := strings.TrimSpace(realGitOutput(t, fixture.repoRoot, "merge-base", defaultHead, sourceHead))
			detail := mergeReadyDetail(base)
			detail.Dir = t.TempDir()
			detail.State.Repo.Root = fixture.repoRoot
			detail.State.Plan.Review.Head = sourceHead
			detail.State.Workspace.Branch = fixture.planBranch
			detail.State.Workspace.BaseBranch = fixture.defaultBranch
			events := &fakeEventAppender{}
			record, err := plan.NewPlanRecordWithStore(events, detail.Dir, detail)
			if err != nil {
				t.Fatal(err)
			}

			starts := 0
			agentSession := NewFreshSingleMergeAgentSession(SingleMergeAgentSessionConfig{
				ProviderLookPath: providerLookPath, ConfinementProbe: tc.confinementProbe,
				ProcessStarter: func(context.Context, string, string, []string) (agent.Process, error) {
					starts++
					return nil, errors.New("unexpected provider start")
				},
			})
			git := gitops.NewClient(fixture.repoRoot, nil)
			service := Service{
				Git: git, Events: events, Cleaner: successfulCleanup(),
				SingleResolver: GuardedSingleConflictResolver{Git: git, Recorder: record, Agent: agentSession},
				SingleReviewer: GuardedSingleIntegrationReviewer{Git: git, Recorder: record, Agent: agentSession},
			}
			err = service.Merge(context.Background(), detail, Options{NoVerify: true})
			if !errors.Is(err, ErrSingleResolutionPreflight) || !strings.Contains(err.Error(), tc.wantError) {
				t.Fatalf("capability preflight merge error = %v", err)
			}
			if starts != 0 || detail.State.Plan.MergeCommitIntent == nil || detail.State.Plan.MergeCommitIntent.Resolution != nil {
				t.Fatalf("preflight failure started provider or persisted requested evidence: starts=%d intent=%+v", starts, detail.State.Plan.MergeCommitIntent)
			}
			if status := strings.TrimSpace(realGitOutput(t, fixture.repoRoot, "status", "--porcelain")); status != "" {
				t.Fatalf("preflight failure left non-retryable worktree state: %q", status)
			}
			if got := strings.TrimSpace(realGitOutput(t, fixture.repoRoot, "rev-parse", fixture.defaultBranch)); got != defaultHead {
				t.Fatalf("preflight failure moved default: got %s want %s", got, defaultHead)
			}

			retryAgent := &preflightSingleMergeAgent{}
			retryAgent.resolve = func(_ context.Context, request BatchAgentSessionRequest) (BatchAgentSessionResult, error) {
				switch request.Operation {
				case BatchAgentOperationSinglePlanResolution:
					if err := os.WriteFile(filepath.Join(request.IntegrationRoot, "README.md"), []byte("combined retry\n"), 0o600); err != nil {
						return BatchAgentSessionResult{}, err
					}
					return BatchAgentSessionResult{Output: batchResolutionJSON("combined after capability became available")}, nil
				case BatchAgentOperationSinglePlanReview:
					return BatchAgentSessionResult{Output: reviewJSON("approve", "exact retry integration is safe", "")}, nil
				default:
					return BatchAgentSessionResult{}, fmt.Errorf("unexpected operation %s", request.Operation)
				}
			}
			service.SingleResolver = GuardedSingleConflictResolver{Git: git, Recorder: record, Agent: retryAgent}
			service.SingleReviewer = GuardedSingleIntegrationReviewer{Git: git, Recorder: record, Agent: retryAgent}
			if err := service.Merge(context.Background(), detail, Options{NoVerify: true}); err != nil {
				t.Fatalf("retry after restoring capability: %v", err)
			}
			if retryAgent.calls != 2 || events.count(plan.EventTypePlanMerged) != 1 {
				t.Fatalf("retry sessions/events = %d / %#v", retryAgent.calls, events.events)
			}
		})
	}
}

func TestMergeRearmsRejectedAttributedStartupForExactLaterExplicitSuccess(t *testing.T) {
	fixture, _, _, _ := batchAgentConflictFixture(t)
	sourceHead := strings.TrimSpace(realGitOutput(t, fixture.worktreePath, "rev-parse", "HEAD"))
	defaultHead := strings.TrimSpace(realGitOutput(t, fixture.repoRoot, "rev-parse", "HEAD"))
	base := strings.TrimSpace(realGitOutput(t, fixture.repoRoot, "merge-base", defaultHead, sourceHead))
	detail := mergeReadyDetail(base)
	detail.Dir = t.TempDir()
	detail.State.Repo.Root = fixture.repoRoot
	detail.State.Plan.Review.Head = sourceHead
	detail.State.Workspace.Branch = fixture.planBranch
	detail.State.Workspace.BaseBranch = fixture.defaultBranch
	events := &fakeEventAppender{}
	record, err := plan.NewPlanRecordWithStore(events, detail.Dir, detail)
	if err != nil {
		t.Fatal(err)
	}
	git := gitops.NewClient(fixture.repoRoot, nil)
	failedAgent := &preflightSingleMergeAgent{resolve: func(context.Context, BatchAgentSessionRequest) (BatchAgentSessionResult, error) {
		return BatchAgentSessionResult{Provider: agentsession.Result{PromptAcceptance: agent.PromptAcceptanceRejected}}, errors.New("fixture explicit rejection")
	}}
	service := Service{
		Git: git, Events: events, Cleaner: successfulCleanup(),
		SingleResolver: GuardedSingleConflictResolver{Git: git, Recorder: record, Agent: failedAgent},
	}
	err = service.Merge(context.Background(), detail, Options{NoVerify: true})
	startup, ok := errors.AsType[*SingleResolutionStartupError](err)
	if !ok || startup.Authority != SingleResolutionAuthorityRearmed {
		t.Fatalf("first merge startup failure = %v", err)
	}
	if detail.State.Plan.MergeCommitIntent == nil || detail.State.Plan.MergeCommitIntent.Resolution != nil || events.count(plan.EventTypeSingleMergeRearmed) != 1 {
		t.Fatalf("rearmed merge state/events = %#v / %#v", detail.State.Plan.MergeCommitIntent, events.events)
	}
	if status := strings.TrimSpace(realGitOutput(t, fixture.repoRoot, "status", "--porcelain")); status != "" {
		t.Fatalf("rearmed merge left dirty worktree: %q", status)
	}

	retryAgent := &preflightSingleMergeAgent{resolve: func(_ context.Context, request BatchAgentSessionRequest) (BatchAgentSessionResult, error) {
		switch request.Operation {
		case BatchAgentOperationSinglePlanResolution:
			if err := os.WriteFile(filepath.Join(request.IntegrationRoot, "README.md"), []byte("combined after explicit retry\n"), 0o600); err != nil {
				return BatchAgentSessionResult{}, err
			}
			return BatchAgentSessionResult{Output: batchResolutionJSON("resolved after explicit retry")}, nil
		case BatchAgentOperationSinglePlanReview:
			return BatchAgentSessionResult{Output: reviewJSON("approve", "exact retry is safe", "")}, nil
		default:
			return BatchAgentSessionResult{}, fmt.Errorf("unexpected operation %s", request.Operation)
		}
	}}
	service.SingleResolver = GuardedSingleConflictResolver{Git: git, Recorder: record, Agent: retryAgent}
	service.SingleReviewer = GuardedSingleIntegrationReviewer{Git: git, Recorder: record, Agent: retryAgent}
	if err := service.Merge(context.Background(), detail, Options{NoVerify: true}); err != nil {
		t.Fatalf("later explicit merge: %v", err)
	}
	if events.count(plan.EventTypePlanMerged) != 1 || retryAgent.calls != 2 {
		t.Fatalf("later explicit merge sessions/events = %d / %#v", retryAgent.calls, events.events)
	}
}

func TestGuardedSingleConflictResolverResolvesAndSettlesExactCommit(t *testing.T) {
	fixture, request, git := preparedSingleResolutionFixture(t)
	store := &recordingSingleResolutionStore{current: request.Intent}
	calls := 0
	store.onAdvance = func(resolution plan.SingleMergeResolution) error {
		if resolution.Phase == plan.SingleMergeResolutionPhaseResolved {
			unmerged := exec.Command("git", "ls-files", "-u")
			unmerged.Dir = fixture.repoRoot
			output, err := unmerged.Output()
			if err != nil || len(output) == 0 {
				t.Fatal("resolved intent was not persisted before Tao staged conflict paths")
			}
		}
		return nil
	}
	agent := batchSessionAgentFunc(func(_ context.Context, session BatchAgentSessionRequest) (BatchAgentSessionResult, error) {
		calls++
		if store.current.Resolution == nil || store.current.Resolution.Phase != plan.SingleMergeResolutionPhaseRequested {
			t.Fatal("agent ran before durable requested evidence")
		}
		for _, want := range []string{"single-plan squash conflict resolution", "BEGIN TAO UNTRUSTED VERIFICATION COMMAND", `"go test ./..."`, "BEGIN TAO UNTRUSTED PLAN BRIEF"} {
			if !strings.Contains(session.Prompt, want) {
				t.Fatalf("single resolution prompt lacks %q: %q", want, session.Prompt)
			}
		}
		if session.Operation != BatchAgentOperationSinglePlanResolution || session.Attempt != 1 || session.BatchID != "" || session.CandidatePlanID != request.Intent.PlanID {
			t.Fatalf("unexpected session attribution: %#v", session)
		}
		return BatchAgentSessionResult{Output: batchResolutionJSON("combined both README changes")}, os.WriteFile(filepath.Join(session.IntegrationRoot, "README.md"), []byte("combined\n"), 0o600)
	})
	resolver := GuardedSingleConflictResolver{Git: git, Recorder: store, Agent: agent}
	resolved, err := resolver.ResolveConflict(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 || store.records != 1 || store.advances != 1 || resolved.Intent.Resolution == nil || resolved.Intent.Resolution.Phase != plan.SingleMergeResolutionPhaseResolved {
		t.Fatalf("unexpected resolved transaction: calls=%d records=%d advances=%d result=%#v", calls, store.records, store.advances, resolved)
	}
	if !slices.Equal(resolved.Intent.Resolution.ChangedPaths, []string{"README.md"}) || len(resolved.Intent.Resolution.ContentFingerprint) != 64 {
		t.Fatalf("resolution omitted exact edit evidence: %#v", resolved.Intent.Resolution)
	}
	for _, trailer := range []string{"Tao-Plan: plan-a", "Tao-Source-Head: " + request.Intent.SourceHead} {
		if !strings.Contains(resolved.Intent.Resolution.CommitMessage, trailer) {
			t.Fatalf("centrally formatted message lacks %q: %q", trailer, resolved.Intent.Resolution.CommitMessage)
		}
	}
	if got := strings.TrimSpace(realGitOutput(t, fixture.repoRoot, "rev-parse", fixture.defaultBranch)); got != request.Intent.DefaultParent {
		t.Fatalf("resolver moved default ref: %s", got)
	}
	if got := strings.TrimSpace(realGitOutput(t, fixture.repoRoot, "rev-parse", fixture.planBranch)); got != request.Intent.SourceHead {
		t.Fatalf("resolver moved source ref: %s", got)
	}

	request.Intent = resolved.Intent
	settled, err := resolver.SettleResolved(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if settled.Intent.Resolution.Phase != plan.SingleMergeResolutionPhaseCommitted || settled.Head == "" || settled.Recovered {
		t.Fatalf("unexpected settlement: %#v", settled)
	}
	message := strings.TrimSpace(realGitOutput(t, fixture.repoRoot, "show", "-s", "--format=%B", settled.Head))
	if message != resolved.Intent.Resolution.CommitMessage {
		t.Fatalf("committed message drifted\n got: %q\nwant: %q", message, resolved.Intent.Resolution.CommitMessage)
	}
	if calls != 1 {
		t.Fatalf("settlement reran resolver: calls=%d", calls)
	}
}

func TestGuardedSingleConflictResolverRejectsResidualConflictMarkersAndRollsBack(t *testing.T) {
	for _, tc := range []struct {
		name       string
		content    string
		markerSize int
	}{
		{name: "separator", content: "ours\n=======\ntheirs\n"},
		{name: "diff3 ancestor", content: "ours\n||||||| parent\nancestor\n"},
		{name: "custom width across streaming buffer", content: strings.Repeat("x", conflictMarkerScanBufferBytes-4) + "\n" + strings.Repeat("=", 12) + "\n", markerSize: 12},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture, request, git := preparedSingleResolutionFixtureForMarkerSize(t, tc.markerSize)
			if tc.markerSize > 0 {
				prepared, readErr := os.ReadFile(filepath.Join(fixture.repoRoot, "README.md"))
				if readErr != nil || !strings.Contains(string(prepared), strings.Repeat("<", tc.markerSize)+" HEAD") {
					t.Fatalf("Git did not prepare the configured %d-byte conflict marker: %q, %v", tc.markerSize, prepared, readErr)
				}
			}
			store := &recordingSingleResolutionStore{current: request.Intent}
			resolver := GuardedSingleConflictResolver{Git: git, Recorder: store, Agent: batchResolutionAgentFunc(func(_ context.Context, root, _ string) (string, error) {
				return batchResolutionJSON("claimed resolution"), os.WriteFile(filepath.Join(root, "README.md"), []byte(tc.content), 0o600)
			})}

			result, err := resolver.ResolveConflict(context.Background(), request)
			if !errors.Is(err, ErrSingleResolutionRejected) || !strings.Contains(err.Error(), "resolver left conflict markers in README.md") {
				t.Fatalf("residual %s marker error = %v", tc.name, err)
			}
			if store.records != 1 || store.advances != 0 || result.Intent.Resolution == nil || result.Intent.Resolution.Phase != plan.SingleMergeResolutionPhaseRequested {
				t.Fatalf("residual marker gained authority: records=%d advances=%d result=%+v", store.records, store.advances, result)
			}
			if head := strings.TrimSpace(realGitOutput(t, fixture.repoRoot, "rev-parse", "HEAD")); head != request.Intent.DefaultParent {
				t.Fatalf("residual marker rollback left HEAD at %s", head)
			}
			if got := strings.TrimSpace(realGitOutput(t, fixture.repoRoot, "rev-parse", fixture.defaultBranch)); got != request.Intent.DefaultParent {
				t.Fatalf("residual marker moved default ref: %s", got)
			}
			if got := strings.TrimSpace(realGitOutput(t, fixture.repoRoot, "rev-parse", fixture.planBranch)); got != request.Intent.SourceHead {
				t.Fatalf("residual marker moved source ref: %s", got)
			}
			if status := strings.TrimSpace(realGitOutput(t, fixture.repoRoot, "status", "--porcelain")); status != "" {
				t.Fatalf("residual marker rollback left dirty worktree: %q", status)
			}
			contents, readErr := os.ReadFile(filepath.Join(fixture.repoRoot, "README.md"))
			if readErr != nil || string(contents) != "default\n" {
				t.Fatalf("residual marker rollback contents = %q, %v", contents, readErr)
			}
		})
	}
}

func TestGuardedSingleConflictResolverRejectsOriginalWidthMarkersAfterAttributesChange(t *testing.T) {
	const originalMarkerSize = 12
	fixture, request, git := preparedSingleResolutionFixtureWithSourceMarkerSize(t, originalMarkerSize)
	prepared, err := os.ReadFile(filepath.Join(fixture.repoRoot, "README.md"))
	if err != nil || !strings.Contains(string(prepared), strings.Repeat("<", originalMarkerSize)+" HEAD") {
		t.Fatalf("Git did not prepare the source-configured marker width: %q, %v", prepared, err)
	}
	store := &recordingSingleResolutionStore{current: request.Intent}
	resolver := GuardedSingleConflictResolver{Git: git, Recorder: store, Agent: batchResolutionAgentFunc(func(_ context.Context, root, _ string) (string, error) {
		if err := os.WriteFile(filepath.Join(root, ".gitattributes"), []byte("README.md conflict-marker-size=20\n"), 0o600); err != nil {
			return "", err
		}
		marker := strings.Repeat("=", originalMarkerSize)
		return batchResolutionJSON("claimed resolution after changing attributes"), os.WriteFile(filepath.Join(root, "README.md"), []byte("ours\n"+marker+"\ntheirs\n"), 0o600)
	})}

	result, err := resolver.ResolveConflict(context.Background(), request)
	if !errors.Is(err, ErrSingleResolutionRejected) || !strings.Contains(err.Error(), "resolver left conflict markers in README.md") {
		t.Fatalf("original-width marker error = %v", err)
	}
	if store.records != 1 || store.advances != 0 || result.Intent.Resolution == nil || result.Intent.Resolution.Phase != plan.SingleMergeResolutionPhaseRequested {
		t.Fatalf("original-width marker gained authority: records=%d advances=%d result=%+v", store.records, store.advances, result)
	}
	assertSingleResolutionMarkerRollback(t, fixture, request, originalMarkerSize)
}

func TestSingleResolutionSettlementRejectsOriginalWidthMarkersAfterAttributesChange(t *testing.T) {
	const originalMarkerSize = 12
	fixture, request, git := preparedSingleResolutionFixtureWithSourceMarkerSize(t, originalMarkerSize)
	if err := os.WriteFile(filepath.Join(fixture.repoRoot, ".gitattributes"), []byte("README.md conflict-marker-size=20\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	marker := strings.Repeat("=", originalMarkerSize)
	if err := os.WriteFile(filepath.Join(fixture.repoRoot, "README.md"), []byte("ours\n"+marker+"\ntheirs\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	changes, err := concretePorcelainChanges(context.Background(), git)
	if err != nil {
		t.Fatal(err)
	}
	fingerprint, err := resolutionContentFingerprint(fixture.repoRoot, changes.changedPaths)
	if err != nil {
		t.Fatal(err)
	}
	requestedAt := request.Intent.CreatedAt.Add(time.Second)
	request.Intent.Resolution = &plan.SingleMergeResolution{
		Phase: plan.SingleMergeResolutionPhaseResolved, ConflictFiles: []string{"README.md"}, RequestedAt: requestedAt,
		Outcome: plan.SingleMergeResolutionOutcomeResolved, Summary: "Historical resolver claimed success.", ChangedPaths: changes.changedPaths,
		ContentFingerprint: fingerprint, CommitMessage: request.Intent.Message, ResolvedAt: requestedAt.Add(time.Second),
	}
	store := &recordingSingleResolutionStore{current: request.Intent}
	resolver := GuardedSingleConflictResolver{Git: git, Recorder: store}

	settlement, err := resolver.SettleResolved(context.Background(), request)
	if err == nil || !strings.Contains(err.Error(), "resolver left conflict markers in README.md") {
		t.Fatalf("original-width marker settlement = %+v, %v", settlement, err)
	}
	if store.advances != 0 || settlement.Head != "" {
		t.Fatalf("original-width marker settlement gained authority: advances=%d settlement=%+v", store.advances, settlement)
	}
	assertSingleResolutionMarkerRollback(t, fixture, request, originalMarkerSize)
}

func TestSingleResolutionSettlementRejectsResidualConflictMarkersAndRollsBack(t *testing.T) {
	for _, tc := range []struct {
		name       string
		content    string
		markerSize int
	}{
		{name: "separator", content: "ours\n=======\ntheirs\n"},
		{name: "diff3 ancestor", content: "ours\n||||||| parent\nancestor\n"},
		{name: "custom width across streaming buffer", content: strings.Repeat("x", conflictMarkerScanBufferBytes-4) + "\n" + strings.Repeat("=", 12) + "\n", markerSize: 12},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture, request, git := preparedSingleResolutionFixtureForMarkerSize(t, tc.markerSize)
			if tc.markerSize > 0 {
				prepared, readErr := os.ReadFile(filepath.Join(fixture.repoRoot, "README.md"))
				if readErr != nil || !strings.Contains(string(prepared), strings.Repeat("<", tc.markerSize)+" HEAD") {
					t.Fatalf("Git did not prepare the configured %d-byte conflict marker: %q, %v", tc.markerSize, prepared, readErr)
				}
			}
			if err := os.WriteFile(filepath.Join(fixture.repoRoot, "README.md"), []byte(tc.content), 0o600); err != nil {
				t.Fatal(err)
			}
			changes, err := concretePorcelainChanges(context.Background(), git)
			if err != nil {
				t.Fatal(err)
			}
			fingerprint, err := resolutionContentFingerprint(fixture.repoRoot, changes.changedPaths)
			if err != nil {
				t.Fatal(err)
			}
			requestedAt := request.Intent.CreatedAt.Add(time.Second)
			request.Intent.Resolution = &plan.SingleMergeResolution{
				Phase: plan.SingleMergeResolutionPhaseResolved, ConflictFiles: []string{"README.md"}, RequestedAt: requestedAt,
				Outcome: plan.SingleMergeResolutionOutcomeResolved, Summary: "Historical resolver claimed success.", ChangedPaths: changes.changedPaths,
				ContentFingerprint: fingerprint, CommitMessage: request.Intent.Message, ResolvedAt: requestedAt.Add(time.Second),
			}
			store := &recordingSingleResolutionStore{current: request.Intent}
			resolver := GuardedSingleConflictResolver{Git: git, Recorder: store}

			settlement, err := resolver.SettleResolved(context.Background(), request)
			if err == nil || !strings.Contains(err.Error(), "resolver left conflict markers in README.md") {
				t.Fatalf("residual %s marker settlement = %+v, %v", tc.name, settlement, err)
			}
			if store.advances != 0 || settlement.Head != "" {
				t.Fatalf("residual marker settlement gained authority: advances=%d settlement=%+v", store.advances, settlement)
			}
			if head := strings.TrimSpace(realGitOutput(t, fixture.repoRoot, "rev-parse", "HEAD")); head != request.Intent.DefaultParent {
				t.Fatalf("residual marker settlement rollback left HEAD at %s", head)
			}
			if got := strings.TrimSpace(realGitOutput(t, fixture.repoRoot, "rev-parse", fixture.defaultBranch)); got != request.Intent.DefaultParent {
				t.Fatalf("residual marker settlement moved default ref: %s", got)
			}
			if got := strings.TrimSpace(realGitOutput(t, fixture.repoRoot, "rev-parse", fixture.planBranch)); got != request.Intent.SourceHead {
				t.Fatalf("residual marker settlement moved source ref: %s", got)
			}
			if status := strings.TrimSpace(realGitOutput(t, fixture.repoRoot, "status", "--porcelain")); status != "" {
				t.Fatalf("residual marker settlement rollback left dirty worktree: %q", status)
			}
			contents, readErr := os.ReadFile(filepath.Join(fixture.repoRoot, "README.md"))
			if readErr != nil || string(contents) != "default\n" {
				t.Fatalf("residual marker settlement rollback contents = %q, %v", contents, readErr)
			}
		})
	}
}

func TestMergeForceRefusesConflictResolutionWithPreExistingTrackedAndUntrackedDirt(t *testing.T) {
	fixture, _, _, _ := batchAgentConflictFixture(t)
	trackedPath := filepath.Join(fixture.repoRoot, "local.txt")
	if err := os.WriteFile(filepath.Join(fixture.worktreePath, "local.txt"), []byte("shared\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runRealGit(t, fixture.worktreePath, "add", "local.txt")
	runRealGit(t, fixture.worktreePath, "commit", "-m", "add shared source file")
	sourceHead := strings.TrimSpace(realGitOutput(t, fixture.worktreePath, "rev-parse", "HEAD"))
	if err := os.WriteFile(trackedPath, []byte("shared\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runRealGit(t, fixture.repoRoot, "add", "local.txt")
	runRealGit(t, fixture.repoRoot, "commit", "-m", "add shared default file")
	defaultHead := strings.TrimSpace(realGitOutput(t, fixture.repoRoot, "rev-parse", "HEAD"))
	largeUserEdit := strings.Repeat("user edit block\n", (8<<20)/len("user edit block\n"))
	if err := os.WriteFile(trackedPath, []byte(largeUserEdit), 0o600); err != nil {
		t.Fatal(err)
	}
	untrackedPath := filepath.Join(fixture.repoRoot, "scratch.txt")
	if err := os.WriteFile(untrackedPath, []byte("user scratch\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	base := strings.TrimSpace(realGitOutput(t, fixture.repoRoot, "merge-base", defaultHead, sourceHead))
	detail := mergeReadyDetail(base)
	detail.Dir = t.TempDir()
	detail.State.Repo.Root = fixture.repoRoot
	detail.State.Plan.Review.Head = sourceHead
	detail.State.Workspace.Branch = fixture.planBranch
	detail.State.Workspace.BaseBranch = fixture.defaultBranch
	events := &fakeEventAppender{}
	record, err := plan.NewPlanRecordWithStore(events, detail.Dir, detail)
	if err != nil {
		t.Fatal(err)
	}
	resolverCalls := 0
	git := gitops.NewClient(fixture.repoRoot, nil)
	resolver := GuardedSingleConflictResolver{Git: git, Recorder: record, Agent: batchResolutionAgentFunc(func(_ context.Context, _, _ string) (string, error) {
		resolverCalls++
		return batchResolutionJSON("must not run"), nil
	})}
	service := Service{Git: git, Events: events, Cleaner: successfulCleanup(), SingleResolver: resolver}

	preexistingPaths, preexistingBoundary, err := snapshotPreexistingWorktreeBoundary(context.Background(), git)
	if err != nil {
		t.Fatal(err)
	}
	if preexistingBoundary == nil {
		t.Fatal("forced-dirty snapshot boundary is nil")
	}
	trackedState := preexistingBoundary.preserved["local.txt"]
	if !trackedState.backed || trackedState.content != "" || trackedState.contentSize != int64(len(largeUserEdit)) {
		t.Fatalf("large tracked dirt was not streamed to bounded backing: %#v", trackedState)
	}
	if preexistingBoundary.backing == nil || preexistingBoundary.backing.size != int64(len(largeUserEdit)+len("user scratch\n")) {
		t.Fatalf("forced-dirty backing size = %#v", preexistingBoundary.backing)
	}
	if !slices.Contains(preexistingPaths, "local.txt") || !slices.Contains(preexistingPaths, "scratch.txt") {
		t.Fatalf("forced-dirty paths = %v", preexistingPaths)
	}
	preexistingBoundary.cleanup()

	err = service.Merge(context.Background(), detail, Options{Force: true, NoVerify: true})
	if !errors.Is(err, ErrSingleResolutionRejected) || !strings.Contains(err.Error(), "pre-existing non-ignored worktree changes") {
		t.Fatalf("forced dirty conflict error = %v", err)
	}
	if resolverCalls != 0 {
		t.Fatalf("forced dirty conflict invoked resolver %d times", resolverCalls)
	}
	if events.count(plan.EventTypePlanMerged) != 0 {
		t.Fatalf("forced dirty conflict recorded merge: %#v", events.events)
	}
	if got := strings.TrimSpace(realGitOutput(t, fixture.repoRoot, "rev-parse", fixture.defaultBranch)); got != defaultHead {
		t.Fatalf("default ref moved: got %s want %s", got, defaultHead)
	}
	if got := strings.TrimSpace(realGitOutput(t, fixture.repoRoot, "rev-parse", fixture.planBranch)); got != sourceHead {
		t.Fatalf("source ref moved: got %s want %s", got, sourceHead)
	}
	if contents, readErr := os.ReadFile(trackedPath); readErr != nil || string(contents) != largeUserEdit { //nolint:gosec // test path is rooted in a temporary Git fixture.
		t.Fatalf("large tracked user dirt was not preserved: got %d bytes, error %v", len(contents), readErr)
	}
	if contents, readErr := os.ReadFile(untrackedPath); readErr != nil || string(contents) != "user scratch\n" { //nolint:gosec // test path is rooted in a temporary Git fixture.
		t.Fatalf("untracked user dirt was not preserved: %q, %v", contents, readErr)
	}
	status := strings.TrimSpace(realGitOutput(t, fixture.repoRoot, "status", "--porcelain"))
	if status != "M local.txt\n?? scratch.txt" && status != " M local.txt\n?? scratch.txt" {
		t.Fatalf("user dirt status changed: %q", status)
	}
	if intent := detail.State.Plan.MergeCommitIntent; intent == nil || intent.Resolution != nil {
		t.Fatalf("forced dirty conflict persisted resolution authority: %#v", intent)
	}
}

func TestSingleResolutionSettlementRecoversExactCommittedResultWithoutAgent(t *testing.T) {
	fixture, request, git := preparedSingleResolutionFixture(t)
	store := &recordingSingleResolutionStore{current: request.Intent}
	resolver := GuardedSingleConflictResolver{Git: git, Recorder: store, Agent: batchResolutionAgentFunc(func(_ context.Context, root, _ string) (string, error) {
		return batchResolutionJSON("resolved"), os.WriteFile(filepath.Join(root, "README.md"), []byte("combined\n"), 0o600)
	})}
	resolved, err := resolver.ResolveConflict(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	request.Intent = resolved.Intent
	runRealGit(t, fixture.repoRoot, "add", "README.md")
	runRealGit(t, fixture.repoRoot, "commit", "-m", resolved.Intent.Resolution.CommitMessage)
	exactHead := strings.TrimSpace(realGitOutput(t, fixture.repoRoot, "rev-parse", "HEAD"))

	settled, err := resolver.SettleResolved(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if !settled.Recovered || settled.Head != exactHead || settled.Intent.Resolution.Phase != plan.SingleMergeResolutionPhaseCommitted {
		t.Fatalf("exact commit was not recovered: %#v", settled)
	}
	again := request
	again.Intent = settled.Intent
	settledAgain, err := resolver.SettleResolved(context.Background(), again)
	if err != nil {
		t.Fatal(err)
	}
	if !settledAgain.Recovered || settledAgain.Head != exactHead {
		t.Fatalf("committed settlement was not idempotent: %#v", settledAgain)
	}
}

func TestSingleResolutionSettlementDisablesPostCommitHookSideEffects(t *testing.T) {
	var hook string
	fixture, request, git := preparedSingleResolutionFixtureWithSetup(t, func(fixture realGitWorktree) {
		hook = filepath.Join(fixture.repoRoot, ".githooks", "post-commit")
		sourceHook := filepath.Join(fixture.worktreePath, ".githooks", "post-commit")
		if err := os.MkdirAll(filepath.Dir(sourceHook), 0o700); err != nil {
			t.Fatal(err)
		}
		script := "#!/bin/sh\n" +
			"echo ran > \"$0.ran\"\n" +
			"echo injected > hook-added.txt\n" +
			"git update-ref refs/heads/hook-side-effect HEAD\n"
		if err := os.WriteFile(sourceHook, []byte(script), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(sourceHook, 0o700); err != nil { //nolint:gosec // G302: test hook must be executable.
			t.Fatal(err)
		}
		runRealGit(t, fixture.worktreePath, "add", ".githooks/post-commit")
		runRealGit(t, fixture.worktreePath, "commit", "-m", "plant repository post-commit hook")
		runRealGit(t, fixture.repoRoot, "config", "core.hooksPath", ".githooks")
	})
	request.ChangedFiles = append(request.ChangedFiles, ".githooks/post-commit")
	store := &recordingSingleResolutionStore{current: request.Intent}
	resolver := GuardedSingleConflictResolver{Git: git, Recorder: store, Agent: batchResolutionAgentFunc(func(_ context.Context, root, _ string) (string, error) {
		return batchResolutionJSON("resolved"), os.WriteFile(filepath.Join(root, "README.md"), []byte("combined\n"), 0o600)
	})}
	resolved, err := resolver.ResolveConflict(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(resolved.Intent.Resolution.ChangedPaths, ".githooks/post-commit") {
		t.Fatalf("resolution did not include source-supplied tracked hook: %v", resolved.Intent.Resolution.ChangedPaths)
	}
	request.Intent = resolved.Intent
	settled, err := resolver.SettleResolved(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if settled.Intent.Resolution.Phase != plan.SingleMergeResolutionPhaseCommitted || settled.Head == "" {
		t.Fatalf("resolution was not accepted: %#v", settled)
	}
	for _, path := range []string{hook + ".ran", filepath.Join(fixture.repoRoot, "hook-added.txt")} {
		if _, statErr := os.Stat(path); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("disabled post-commit hook left side effect %s: %v", path, statErr)
		}
	}
	showRef := exec.Command("git", "show-ref", "--verify", "--quiet", "refs/heads/hook-side-effect")
	showRef.Dir = fixture.repoRoot
	var exitErr *exec.ExitError
	if err := showRef.Run(); err == nil || !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 {
		t.Fatalf("disabled post-commit hook ref lookup = %v, want absent", err)
	}
	if status := strings.TrimSpace(realGitOutput(t, fixture.repoRoot, "status", "--porcelain")); status != "" {
		t.Fatalf("accepted settlement left dirty worktree: %q", status)
	}
}

func TestSingleResolutionSettlementRejectsRecoveryWithInexactCommittedTree(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(string) error
	}{
		{name: "extra path", mutate: func(root string) error {
			return os.WriteFile(filepath.Join(root, "extra.txt"), []byte("unexpected\n"), 0o600)
		}},
		{name: "content", mutate: func(root string) error {
			return os.WriteFile(filepath.Join(root, "README.md"), []byte("altered after fingerprint\n"), 0o600)
		}},
		{name: "mode", mutate: func(root string) error {
			return os.Chmod(filepath.Join(root, "README.md"), 0o700) //nolint:gosec // G302: test deliberately creates executable-mode drift.
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fixture, request, git := preparedSingleResolutionFixture(t)
			store := &recordingSingleResolutionStore{current: request.Intent}
			resolver := GuardedSingleConflictResolver{Git: git, Recorder: store, Agent: batchResolutionAgentFunc(func(_ context.Context, root, _ string) (string, error) {
				return batchResolutionJSON("resolved"), os.WriteFile(filepath.Join(root, "README.md"), []byte("combined\n"), 0o600)
			})}
			resolved, err := resolver.ResolveConflict(context.Background(), request)
			if err != nil {
				t.Fatal(err)
			}
			if err := tc.mutate(fixture.repoRoot); err != nil {
				t.Fatal(err)
			}
			runRealGit(t, fixture.repoRoot, "add", "-A")
			runRealGit(t, fixture.repoRoot, "commit", "-m", resolved.Intent.Resolution.CommitMessage)
			request.Intent = resolved.Intent
			if _, err := resolver.SettleResolved(context.Background(), request); !errors.Is(err, ErrSingleResolutionDrift) {
				t.Fatalf("inexact recovery error = %v", err)
			}
			if store.current.Resolution.Phase != plan.SingleMergeResolutionPhaseResolved {
				t.Fatalf("inexact recovery advanced durable evidence: %#v", store.current.Resolution)
			}
		})
	}
}

func TestSingleResolutionSettlementRefusesContentDriftAfterResolvedCrashWithoutMutation(t *testing.T) {
	fixture, request, git := preparedSingleResolutionFixture(t)
	store := &recordingSingleResolutionStore{current: request.Intent}
	resolver := GuardedSingleConflictResolver{Git: git, Recorder: store, Agent: batchResolutionAgentFunc(func(_ context.Context, root, _ string) (string, error) {
		return batchResolutionJSON("resolved"), os.WriteFile(filepath.Join(root, "README.md"), []byte("combined\n"), 0o600)
	})}
	resolved, err := resolver.ResolveConflict(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	readme := filepath.Join(fixture.repoRoot, "README.md")
	if err := os.WriteFile(readme, []byte("intervening content\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	statusBefore := realGitOutput(t, fixture.repoRoot, "status", "--porcelain")
	request.Intent = resolved.Intent
	rerun := GuardedSingleConflictResolver{Git: git, Recorder: store}
	if _, err := rerun.SettleResolved(context.Background(), request); !errors.Is(err, ErrSingleResolutionDrift) {
		t.Fatalf("content drift error = %v", err)
	}
	if head := strings.TrimSpace(realGitOutput(t, fixture.repoRoot, "rev-parse", "HEAD")); head != request.Intent.DefaultParent {
		t.Fatalf("drift refusal moved HEAD to %s", head)
	}
	if status := realGitOutput(t, fixture.repoRoot, "status", "--porcelain"); status != statusBefore {
		t.Fatalf("drift refusal changed status: got %q want %q", status, statusBefore)
	}
	if contents, readErr := os.ReadFile(readme); readErr != nil || string(contents) != "intervening content\n" { //nolint:gosec // test path is rooted in a temporary Git fixture.
		t.Fatalf("drift refusal changed intervening content: %q, %v", contents, readErr)
	}
}

func TestSingleResolutionSettlementRefusesChangedPathDriftAfterResolvedCrashWithoutMutation(t *testing.T) {
	fixture, request, git := preparedSingleResolutionFixture(t)
	store := &recordingSingleResolutionStore{current: request.Intent}
	resolver := GuardedSingleConflictResolver{Git: git, Recorder: store, Agent: batchResolutionAgentFunc(func(_ context.Context, root, _ string) (string, error) {
		return batchResolutionJSON("resolved"), os.WriteFile(filepath.Join(root, "README.md"), []byte("combined\n"), 0o600)
	})}
	resolved, err := resolver.ResolveConflict(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	stagedPath := filepath.Join(fixture.repoRoot, "intervening-staged.txt")
	if err := os.WriteFile(stagedPath, []byte("staged user work\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runRealGit(t, fixture.repoRoot, "add", "intervening-staged.txt")
	untrackedPath := filepath.Join(fixture.repoRoot, "intervening-untracked.txt")
	if err := os.WriteFile(untrackedPath, []byte("untracked user work\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	statusBefore := realGitOutput(t, fixture.repoRoot, "status", "--porcelain")
	request.Intent = resolved.Intent
	rerun := GuardedSingleConflictResolver{Git: git, Recorder: store}
	if _, err := rerun.SettleResolved(context.Background(), request); !errors.Is(err, ErrSingleResolutionDrift) {
		t.Fatalf("changed-path drift error = %v", err)
	}
	if head := strings.TrimSpace(realGitOutput(t, fixture.repoRoot, "rev-parse", "HEAD")); head != request.Intent.DefaultParent {
		t.Fatalf("drift refusal moved HEAD to %s", head)
	}
	if status := realGitOutput(t, fixture.repoRoot, "status", "--porcelain"); status != statusBefore {
		t.Fatalf("drift refusal changed status: got %q want %q", status, statusBefore)
	}
	for path, want := range map[string]string{stagedPath: "staged user work\n", untrackedPath: "untracked user work\n"} {
		contents, readErr := os.ReadFile(path) //nolint:gosec // test paths are rooted in a temporary Git fixture.
		if readErr != nil || string(contents) != want {
			t.Fatalf("drift refusal changed %s: %q, %v", filepath.Base(path), contents, readErr)
		}
	}
}

func TestGuardedSingleConflictResolverRejectsUnsafeMalformedNoopAndProviderFailures(t *testing.T) {
	tests := []struct {
		name string
		run  func(string) (string, error)
	}{
		{name: "unsafe metadata", run: func(root string) (string, error) {
			if err := os.MkdirAll(filepath.Join(root, ".tao"), 0o700); err != nil {
				return "", err
			}
			if err := os.WriteFile(filepath.Join(root, ".tao", "forged"), []byte("bad"), 0o600); err != nil {
				return "", err
			}
			return batchResolutionJSON("unsafe"), os.WriteFile(filepath.Join(root, "README.md"), []byte("combined\n"), 0o600)
		}},
		{name: "malformed output", run: func(root string) (string, error) {
			return `{"summary":"missing proposal"}`, os.WriteFile(filepath.Join(root, "README.md"), []byte("combined\n"), 0o600)
		}},
		{name: "no op", run: func(string) (string, error) { return batchResolutionJSON("claimed resolution"), nil }},
		{name: "provider failure", run: func(root string) (string, error) {
			return "partial", errors.Join(os.WriteFile(filepath.Join(root, "README.md"), []byte("combined\n"), 0o600), errors.New("provider unavailable"))
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fixture, request, git := preparedSingleResolutionFixture(t)
			store := &recordingSingleResolutionStore{current: request.Intent}
			calls := 0
			resolver := GuardedSingleConflictResolver{Git: git, Recorder: store, Agent: batchResolutionAgentFunc(func(_ context.Context, root, _ string) (string, error) {
				calls++
				return tc.run(root)
			})}
			result, err := resolver.ResolveConflict(context.Background(), request)
			if !errors.Is(err, ErrSingleResolutionRejected) || calls != 1 {
				t.Fatalf("calls/result/error = %d, %#v, %v", calls, result, err)
			}
			if result.Provider.Output == "" && tc.name != "provider failure" {
				t.Fatalf("provider result was not exposed: %#v", result.Provider)
			}
			if head := strings.TrimSpace(realGitOutput(t, fixture.repoRoot, "rev-parse", "HEAD")); head != request.Intent.DefaultParent {
				t.Fatalf("rejection left HEAD at %s", head)
			}
			if status := strings.TrimSpace(realGitOutput(t, fixture.repoRoot, "status", "--porcelain")); status != "" {
				t.Fatalf("rejection left dirty worktree: %q", status)
			}
		})
	}
}

func TestGuardedSingleConflictResolverRejectsOversizedOutputAndRollsBack(t *testing.T) {
	fixture, request, git := preparedSingleResolutionFixture(t)
	store := &recordingSingleResolutionStore{current: request.Intent}
	resolver := GuardedSingleConflictResolver{Git: git, Recorder: store, Agent: batchResolutionAgentFunc(func(_ context.Context, root, _ string) (string, error) {
		path := filepath.Join(root, "README.md")
		file, err := os.OpenFile(path, os.O_WRONLY|os.O_TRUNC, 0) //nolint:gosec // fixture-owned conflict path is the bounded rejection input.
		if err != nil {
			return "", err
		}
		if err := file.Truncate(maxConflictMarkerScanFileBytes + 1); err != nil {
			_ = file.Close()
			return "", err
		}
		return batchResolutionJSON("claimed oversized resolution"), file.Close()
	})}

	_, err := resolver.ResolveConflict(context.Background(), request)
	if !errors.Is(err, ErrSingleResolutionRejected) || !strings.Contains(err.Error(), "per-file scan limit") {
		t.Fatalf("oversized resolver output error = %v", err)
	}
	if store.records != 1 || store.advances != 0 || store.current.Resolution == nil || store.current.Resolution.Phase != plan.SingleMergeResolutionPhaseRequested {
		t.Fatalf("oversized output gained resolution authority: records=%d advances=%d intent=%+v", store.records, store.advances, store.current)
	}
	if head := strings.TrimSpace(realGitOutput(t, fixture.repoRoot, "rev-parse", "HEAD")); head != request.Intent.DefaultParent {
		t.Fatalf("oversized output rollback left HEAD at %s", head)
	}
	if got := strings.TrimSpace(realGitOutput(t, fixture.repoRoot, "rev-parse", fixture.defaultBranch)); got != request.Intent.DefaultParent {
		t.Fatalf("oversized output moved default ref: %s", got)
	}
	if got := strings.TrimSpace(realGitOutput(t, fixture.repoRoot, "rev-parse", fixture.planBranch)); got != request.Intent.SourceHead {
		t.Fatalf("oversized output moved source ref: %s", got)
	}
	if status := strings.TrimSpace(realGitOutput(t, fixture.repoRoot, "status", "--porcelain")); status != "" {
		t.Fatalf("oversized output rollback left dirty worktree: %q", status)
	}
	contents, readErr := os.ReadFile(filepath.Join(fixture.repoRoot, "README.md"))
	if readErr != nil || string(contents) != "default\n" {
		t.Fatalf("oversized output rollback contents = %q, %v", contents, readErr)
	}
}

func TestGuardedSingleConflictResolverRejectsAndRestoresIgnoredMutationWithOtherwiseValidResolution(t *testing.T) {
	for _, mutation := range ignoredMutationTestCases() {
		t.Run(mutation.name, func(t *testing.T) {
			fixture, request, git := preparedSingleResolutionFixture(t)
			configureSingleAgentIgnoredPaths(t, fixture.repoRoot, ".env")
			ignored := filepath.Join(fixture.repoRoot, ".env")
			mutation.prepare(t, ignored)
			store := &recordingSingleResolutionStore{current: request.Intent}
			resolver := GuardedSingleConflictResolver{Git: git, Recorder: store, Agent: batchResolutionAgentFunc(func(_ context.Context, root, _ string) (string, error) {
				if err := mutation.mutate(filepath.Join(root, ".env")); err != nil {
					return "", err
				}
				if status := realGitOutput(t, root, "status", "--porcelain"); strings.Contains(status, ".env") {
					t.Fatalf("ordinary porcelain unexpectedly exposed ignored mutation: %q", status)
				}
				return batchResolutionJSON("combined both README changes"), os.WriteFile(filepath.Join(root, "README.md"), []byte("combined\n"), 0o600)
			})}
			if _, err := resolver.ResolveConflict(context.Background(), request); !errors.Is(err, ErrSingleResolutionRejected) || !strings.Contains(err.Error(), "ignored path") {
				t.Fatalf("resolver ignored-mutation rejection = %v", err)
			}
			mutation.assertRestored(t, ignored)
			if status := strings.TrimSpace(realGitOutput(t, fixture.repoRoot, "status", "--porcelain")); status != "" {
				t.Fatalf("rejection left dirty worktree: %q", status)
			}
		})
	}
}

type ignoredMutationTestCase struct {
	name           string
	prepare        func(*testing.T, string)
	mutate         func(string) error
	assertRestored func(*testing.T, string)
	assertMutated  func(*testing.T, string)
}

func ignoredMutationTestCases() []ignoredMutationTestCase {
	prepareFile := func(t *testing.T, path string) {
		t.Helper()
		if err := os.WriteFile(path, []byte("user-secret\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	assertFile := func(t *testing.T, path string) {
		t.Helper()
		contents, err := os.ReadFile(path) //nolint:gosec // test path is rooted in a temporary Git fixture.
		if err != nil || string(contents) != "user-secret\n" {
			t.Fatalf("ignored file was not restored: %q, %v", contents, err)
		}
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
			t.Fatalf("ignored file type/mode was not restored: %v, %v", info, err)
		}
	}
	return []ignoredMutationTestCase{
		{
			name:    "creation",
			prepare: func(*testing.T, string) {},
			mutate:  func(path string) error { return os.WriteFile(path, []byte("created\n"), 0o600) },
			assertRestored: func(t *testing.T, path string) {
				t.Helper()
				if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("created ignored path remains: %v", err)
				}
			},
			assertMutated: func(t *testing.T, path string) {
				t.Helper()
				contents, err := os.ReadFile(path) //nolint:gosec // test path is rooted in a temporary Git fixture.
				if err != nil || string(contents) != "created\n" {
					t.Fatalf("created ignored path was overwritten: %q, %v", contents, err)
				}
			},
		},
		{
			name: "deletion", prepare: prepareFile, mutate: os.Remove, assertRestored: assertFile,
			assertMutated: func(t *testing.T, path string) {
				t.Helper()
				if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("deleted ignored path was restored: %v", err)
				}
			},
		},
		{
			name:    "type change",
			prepare: prepareFile,
			mutate: func(path string) error {
				if err := os.Remove(path); err != nil {
					return err
				}
				return os.Symlink("replacement", path)
			},
			assertRestored: assertFile,
			assertMutated: func(t *testing.T, path string) {
				t.Helper()
				target, err := os.Readlink(path)
				if err != nil || target != "replacement" {
					t.Fatalf("ignored symlink replacement was overwritten: %q, %v", target, err)
				}
			},
		},
		{
			name:    "content and mode change",
			prepare: prepareFile,
			mutate: func(path string) error {
				if err := os.WriteFile(path, []byte("changed\n"), 0o600); err != nil {
					return err
				}
				return os.Chmod(path, 0o755) //nolint:gosec // G302: test deliberately creates executable-mode drift in an ignored file.
			},
			assertRestored: assertFile,
			assertMutated: func(t *testing.T, path string) {
				t.Helper()
				contents, err := os.ReadFile(path) //nolint:gosec // test path is rooted in a temporary Git fixture.
				if err != nil || string(contents) != "changed\n" {
					t.Fatalf("ignored content change was overwritten: %q, %v", contents, err)
				}
				info, err := os.Lstat(path)
				if err != nil || info.Mode().Perm() != 0o755 {
					t.Fatalf("ignored mode change was overwritten: %v, %v", info, err)
				}
			},
		},
	}
}

func TestIgnoredWorktreeSnapshotStreamsLargeTreeThroughBoundedBacking(t *testing.T) {
	fixture, _, git := preparedSingleResolutionFixture(t)
	configureSingleAgentIgnoredPaths(t, fixture.repoRoot, "cache/")
	cacheRoot := filepath.Join(fixture.repoRoot, "cache")
	const (
		fileCount = 32
		fileBytes = 256 * 1024
	)
	contents := []byte(strings.Repeat("x", fileBytes))
	for index := range fileCount {
		path := filepath.Join(cacheRoot, "package", fmt.Sprintf("%02d", index), "artifact.bin")
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, contents, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	boundary, err := snapshotWorktreePaths(context.Background(), git)
	if err != nil {
		t.Fatal(err)
	}
	defer boundary.cleanup()
	if boundary.backing == nil || boundary.backing.size != fileCount*fileBytes {
		t.Fatalf("temporary backing size = %#v, want %d", boundary.backing, fileCount*fileBytes)
	}
	for path, state := range boundary.ignored {
		if state.mode.IsRegular() && (!state.backed || state.content != "") {
			t.Fatalf("ignored file %s was retained in memory: %#v", path, state)
		}
	}
	changed, err := ignoredWorktreeChanged(context.Background(), git, boundary)
	if err != nil || changed {
		t.Fatalf("unchanged large ignored tree = %v, %v", changed, err)
	}

	mutated := filepath.Join(cacheRoot, "package", "00", "artifact.bin")
	if err := os.WriteFile(mutated, []byte("mutated"), 0o600); err != nil {
		t.Fatal(err)
	}
	changed, err = ignoredWorktreeChanged(context.Background(), git, boundary)
	if err != nil || !changed {
		t.Fatalf("mutated large ignored tree = %v, %v", changed, err)
	}
	if err := restoreWorktreePathStates(fixture.repoRoot, boundary.ignored, boundary.backing); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(mutated) //nolint:gosec // test path is rooted in a temporary Git fixture.
	if err != nil || !slices.Equal(got, contents) {
		t.Fatalf("restored large ignored file has %d bytes, error %v", len(got), err)
	}
}

func TestWorktreeSnapshotBackingIsUnlinkedAndVerifiedBeforeRestoration(t *testing.T) {
	fixture, _, git := preparedSingleResolutionFixture(t)
	configureSingleAgentIgnoredPaths(t, fixture.repoRoot, ".env")
	ignored := filepath.Join(fixture.repoRoot, ".env")
	if err := os.WriteFile(ignored, []byte("original ignored bytes\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	tempRoot := t.TempDir()
	t.Setenv("TMPDIR", tempRoot)

	boundary, err := snapshotWorktreePaths(context.Background(), git)
	if err != nil {
		t.Fatal(err)
	}
	defer boundary.cleanup()
	if matches := discoverableSnapshotBackings(t, tempRoot); len(matches) != 0 {
		t.Fatalf("snapshot backing remained discoverable: %v", matches)
	}
	state := boundary.ignored[".env"]
	if !state.backed {
		t.Fatalf("ignored file was not descriptor-backed: %#v", state)
	}
	if err := os.WriteFile(ignored, []byte("provider mutation\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := boundary.backing.file.WriteAt([]byte("X"), state.backingStart); err != nil {
		t.Fatal(err)
	}

	err = restoreWorktreePathStates(fixture.repoRoot, boundary.ignored, boundary.backing)
	if err == nil || !strings.Contains(err.Error(), "backing fingerprint changed") {
		t.Fatalf("corrupt-backing restoration error = %v", err)
	}
	got, readErr := os.ReadFile(ignored) //nolint:gosec // test path is rooted in a temporary Git fixture.
	if readErr != nil || string(got) != "provider mutation\n" {
		t.Fatalf("corrupt backing destructively changed guarded file: %q, %v", got, readErr)
	}
}

func TestGuardedSingleConflictResolverHidesSnapshotBackingFromProvider(t *testing.T) {
	fixture, request, git := preparedSingleResolutionFixture(t)
	configureSingleAgentIgnoredPaths(t, fixture.repoRoot, ".env")
	ignored := filepath.Join(fixture.repoRoot, ".env")
	original := []byte("resolver original secret\n")
	if err := os.WriteFile(ignored, original, 0o600); err != nil {
		t.Fatal(err)
	}
	tempRoot := t.TempDir()
	t.Setenv("TMPDIR", tempRoot)
	tampered := -1
	store := &recordingSingleResolutionStore{current: request.Intent}
	resolver := GuardedSingleConflictResolver{Git: git, Recorder: store, Agent: batchResolutionAgentFunc(func(_ context.Context, root, _ string) (string, error) {
		tampered = tamperDiscoverableSnapshotBackings(t, tempRoot)
		if err := os.WriteFile(filepath.Join(root, ".env"), []byte("resolver mutation\n"), 0o600); err != nil {
			return "", err
		}
		return batchResolutionJSON("must be rejected"), nil
	})}

	_, err := resolver.ResolveConflict(context.Background(), request)
	if !errors.Is(err, ErrSingleResolutionRejected) {
		t.Fatalf("resolver error = %v", err)
	}
	if tampered != 0 {
		t.Fatalf("provider discovered and overwrote %d snapshot backing files", tampered)
	}
	got, readErr := os.ReadFile(ignored) //nolint:gosec // test path is rooted in a temporary Git fixture.
	if readErr != nil || !slices.Equal(got, original) {
		t.Fatalf("resolver rollback did not restore exact ignored bytes: %q, %v", got, readErr)
	}
}

func TestWorktreeSnapshotFailsClosedAtExplicitLimitsAndCancellation(t *testing.T) {
	fixture, _, git := preparedSingleResolutionFixture(t)
	configureSingleAgentIgnoredPaths(t, fixture.repoRoot, "cache/")
	if err := os.MkdirAll(filepath.Join(fixture.repoRoot, "cache"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fixture.repoRoot, "cache", "large.bin"), []byte(strings.Repeat("x", 2048)), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := snapshotWorktreePathsWithLimits(context.Background(), git, worktreeSnapshotLimits{maxPaths: 100, maxBackupBytes: 1024}); err == nil || !strings.Contains(err.Error(), "1024-byte limit") {
		t.Fatalf("byte-limited snapshot error = %v", err)
	}
	if _, err := snapshotWorktreePathsWithLimits(context.Background(), git, worktreeSnapshotLimits{maxPaths: 1, maxBackupBytes: 4096}); err == nil || !strings.Contains(err.Error(), "1-path limit") {
		t.Fatalf("path-limited snapshot error = %v", err)
	}

	backing, err := newWorktreeSnapshotBacking("tao-selected-boundary-*")
	if err != nil {
		t.Fatal(err)
	}
	defer backing.cleanup()
	largePath := filepath.Join(fixture.repoRoot, "cache", "large.bin")
	if _, err := snapshotSelectedWorktreePaths(context.Background(), fixture.repoRoot, []string{"cache/large.bin"}, backing, worktreeSnapshotLimits{maxPaths: 100, maxBackupBytes: 1024}); err == nil || !strings.Contains(err.Error(), "1024-byte limit") {
		t.Fatalf("byte-limited selected snapshot error = %v", err)
	}
	if info, err := backing.file.Stat(); err != nil || backing.size != 0 || info.Size() != 0 {
		t.Fatalf("failed selected snapshot left backing bytes: logical=%d info=%v error=%v", backing.size, info, err)
	}
	if _, err := snapshotSelectedWorktreePaths(context.Background(), fixture.repoRoot, []string{"cache", "cache/large.bin"}, backing, worktreeSnapshotLimits{maxPaths: 1, maxBackupBytes: 4096}); err == nil || !strings.Contains(err.Error(), "1-path limit") {
		t.Fatalf("path-limited selected snapshot error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := snapshotSelectedWorktreePaths(ctx, fixture.repoRoot, []string{"cache/large.bin"}, backing, worktreeSnapshotLimits{maxPaths: 100, maxBackupBytes: 4096}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled selected snapshot error = %v", err)
	}
	if _, _, err := streamWorktreeFile(ctx, largePath, nil, 4096, 4096); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled streaming fingerprint error = %v", err)
	}
}

func discoverableSnapshotBackings(t *testing.T, tempRoot string) []string {
	t.Helper()
	var matches []string
	for _, pattern := range []string{"tao-merge-boundary-*", "tao-git-boundary-*"} {
		found, err := filepath.Glob(filepath.Join(tempRoot, pattern))
		if err != nil {
			t.Fatal(err)
		}
		matches = append(matches, found...)
	}
	sort.Strings(matches)
	return matches
}

func tamperDiscoverableSnapshotBackings(t *testing.T, tempRoot string) int {
	t.Helper()
	matches := discoverableSnapshotBackings(t, tempRoot)
	for _, path := range matches {
		if err := os.WriteFile(path, []byte("provider-controlled rollback bytes\n"), 0o600); err != nil {
			t.Fatalf("tamper with discoverable snapshot backing %s: %v", path, err)
		}
	}
	return len(matches)
}

func TestGuardedSingleConflictResolverRestoresIntegrationMutationsAfterCancellation(t *testing.T) {
	fixture, request, git := preparedSingleResolutionFixture(t)
	configureSingleAgentIgnoredPaths(t, fixture.repoRoot, ".env")
	ctx, cancel := context.WithCancel(context.Background())
	store := &recordingSingleResolutionStore{current: request.Intent}
	resolver := GuardedSingleConflictResolver{Git: git, Recorder: store, Agent: batchResolutionAgentFunc(func(sessionCtx context.Context, root, _ string) (string, error) {
		if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("cancelled tracked edit\n"), 0o600); err != nil {
			return "", err
		}
		if err := os.WriteFile(filepath.Join(root, "resolver-untracked.txt"), []byte("cancelled untracked edit\n"), 0o600); err != nil {
			return "", err
		}
		if err := os.WriteFile(filepath.Join(root, ".env"), []byte("cancelled ignored edit\n"), 0o600); err != nil {
			return "", err
		}
		cancel()
		return batchResolutionJSON("cancelled resolution"), sessionCtx.Err()
	})}

	if _, err := resolver.ResolveConflict(ctx, request); !errors.Is(err, ErrSingleResolutionRejected) {
		t.Fatalf("cancelled resolver error = %v", err)
	}
	for _, path := range []string{"resolver-untracked.txt", ".env"} {
		if _, err := os.Lstat(filepath.Join(fixture.repoRoot, path)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("cancelled resolver left %s: %v", path, err)
		}
	}
	if status := strings.TrimSpace(realGitOutput(t, fixture.repoRoot, "status", "--porcelain")); status != "" {
		t.Fatalf("cancelled resolver left dirty worktree: %q", status)
	}
	if head := strings.TrimSpace(realGitOutput(t, fixture.repoRoot, "rev-parse", "HEAD")); head != request.Intent.DefaultParent {
		t.Fatalf("cancelled resolver left HEAD at %s", head)
	}
}

func TestGuardedSingleConflictResolverPreservesConcurrentProtectedSourceRef(t *testing.T) {
	fixture, request, git := preparedSingleResolutionFixture(t)
	store := &recordingSingleResolutionStore{current: request.Intent}
	resolver := GuardedSingleConflictResolver{Git: git, Recorder: store, Agent: batchResolutionAgentFunc(func(_ context.Context, root, _ string) (string, error) {
		done := make(chan error, 1)
		go func() {
			cmd := exec.Command("git", "update-ref", "refs/heads/"+fixture.planBranch, request.Intent.DefaultParent) //nolint:gosec // fixed git command with fixture-owned values.
			cmd.Dir = root
			output, err := cmd.CombinedOutput()
			if err != nil {
				err = fmt.Errorf("advance concurrent source ref: %w: %s", err, output)
			}
			done <- err
		}()
		return batchResolutionJSON("observed concurrent source movement"), <-done
	})}
	if _, err := resolver.ResolveConflict(context.Background(), request); !errors.Is(err, ErrSingleResolutionRejected) || !strings.Contains(err.Error(), "protected") {
		t.Fatalf("protected-ref mutation error = %v", err)
	}
	if source := strings.TrimSpace(realGitOutput(t, fixture.repoRoot, "rev-parse", fixture.planBranch)); source != request.Intent.DefaultParent {
		t.Fatalf("concurrent source ref was overwritten: got %s want %s", source, request.Intent.DefaultParent)
	}
}

func TestGuardedSingleConflictResolverPreservesConcurrentDefaultRef(t *testing.T) {
	fixture, request, git := preparedSingleResolutionFixture(t)
	store := &recordingSingleResolutionStore{current: request.Intent}
	resolver := GuardedSingleConflictResolver{Git: git, Recorder: store, Agent: batchResolutionAgentFunc(func(_ context.Context, root, _ string) (string, error) {
		done := make(chan error, 1)
		go func() {
			cmd := exec.Command("git", "update-ref", "refs/heads/"+fixture.defaultBranch, request.Intent.SourceHead, request.Intent.DefaultParent) //nolint:gosec // fixed Git command with fixture-owned values.
			cmd.Dir = root
			done <- cmd.Run()
		}()
		return batchResolutionJSON("observed concurrent default movement"), <-done
	})}
	if _, err := resolver.ResolveConflict(context.Background(), request); !errors.Is(err, ErrSingleResolutionRejected) || !strings.Contains(err.Error(), "changed concurrently") {
		t.Fatalf("concurrent default-ref error = %v", err)
	}
	if head := strings.TrimSpace(realGitOutput(t, fixture.repoRoot, "rev-parse", fixture.defaultBranch)); head != request.Intent.SourceHead {
		t.Fatalf("concurrent default ref was overwritten: got %s want %s", head, request.Intent.SourceHead)
	}
}

func TestGuardedSingleConflictResolverRejectsAndPreservesGitMetadataAndUnlistedRefs(t *testing.T) {
	tests := []struct {
		name   string
		setup  func(*testing.T, realGitWorktree, SingleResolutionRequest) func(*testing.T)
		mutate func(*testing.T, realGitWorktree, SingleResolutionRequest)
	}{
		{
			name: "repository config",
			setup: func(t *testing.T, fixture realGitWorktree, _ SingleResolutionRequest) func(*testing.T) {
				path := filepath.Join(fixture.repoRoot, ".git", "config")
				before, err := os.ReadFile(path) //nolint:gosec // test reads a temporary fixture path.
				if err != nil {
					t.Fatal(err)
				}
				return func(t *testing.T) {
					after, err := os.ReadFile(path) //nolint:gosec // test reads a temporary fixture path.
					if err != nil || slices.Equal(after, before) || !strings.Contains(string(after), "forbidden") {
						t.Fatalf("concurrent repository config was overwritten: %v", err)
					}
				}
			},
			mutate: func(t *testing.T, fixture realGitWorktree, _ SingleResolutionRequest) {
				runRealGit(t, fixture.repoRoot, "config", "tao.forbidden", "true")
			},
		},
		{
			name: "created unlisted ref",
			setup: func(_ *testing.T, fixture realGitWorktree, _ SingleResolutionRequest) func(*testing.T) {
				return func(t *testing.T) {
					cmd := exec.Command("git", "show-ref", "--verify", "--quiet", "refs/tags/session-created")
					cmd.Dir = fixture.repoRoot
					if err := cmd.Run(); err != nil {
						t.Fatalf("concurrent unlisted ref was overwritten: %v", err)
					}
				}
			},
			mutate: func(t *testing.T, fixture realGitWorktree, request SingleResolutionRequest) {
				runRealGit(t, fixture.repoRoot, "update-ref", "refs/tags/session-created", request.Intent.SourceHead)
			},
		},
		{
			name: "default ref lock and other linked-worktree HEAD",
			setup: func(t *testing.T, fixture realGitWorktree, request SingleResolutionRequest) func(*testing.T) {
				lock := realGitRefLockPath(t, fixture.repoRoot, fixture.defaultBranch)
				otherHead := filepath.Join(realGitAdminDir(t, fixture.worktreePath), "HEAD")
				beforeHead, err := os.ReadFile(otherHead) //nolint:gosec // test reads a temporary Git fixture path.
				if err != nil {
					t.Fatal(err)
				}
				return func(t *testing.T) {
					if _, err := os.Lstat(lock); err != nil {
						t.Fatalf("concurrent default ref lock was overwritten: %v", err)
					}
					afterHead, err := os.ReadFile(otherHead) //nolint:gosec // test reads a temporary Git fixture path.
					if err != nil || slices.Equal(afterHead, beforeHead) || string(afterHead) != request.Intent.DefaultParent+"\n" {
						t.Fatalf("concurrent linked-worktree HEAD was overwritten: %q, %v", afterHead, err)
					}
				}
			},
			mutate: func(t *testing.T, fixture realGitWorktree, request SingleResolutionRequest) {
				if err := os.WriteFile(realGitRefLockPath(t, fixture.repoRoot, fixture.defaultBranch), []byte("resolver lock\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				otherHead := filepath.Join(realGitAdminDir(t, fixture.worktreePath), "HEAD")
				if err := os.WriteFile(otherHead, []byte(request.Intent.DefaultParent+"\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixture, request, git := preparedSingleResolutionFixture(t)
			assertRestored := tt.setup(t, fixture, request)
			store := &recordingSingleResolutionStore{current: request.Intent}
			resolver := GuardedSingleConflictResolver{Git: git, Recorder: store, Agent: batchResolutionAgentFunc(func(_ context.Context, root, _ string) (string, error) {
				tt.mutate(t, fixture, request)
				return batchResolutionJSON("resolved after forbidden Git mutation"), os.WriteFile(filepath.Join(root, "README.md"), []byte("combined\n"), 0o600)
			})}
			if _, err := resolver.ResolveConflict(context.Background(), request); !errors.Is(err, ErrSingleResolutionRejected) || !strings.Contains(err.Error(), "changed concurrently") {
				t.Fatalf("Git-boundary mutation error = %v", err)
			}
			assertRestored(t)
		})
	}
}

func TestGitSessionBoundaryDetectsAndPreservesNestedGitControlDrift(t *testing.T) {
	fixture := newRealGitWorktree(t)
	embeddedRoot := filepath.Join(fixture.repoRoot, "embedded")
	if err := os.MkdirAll(embeddedRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	runRealGit(t, embeddedRoot, "init", "-b", "main")
	runRealGit(t, embeddedRoot, "config", "user.name", "Tao Test")
	runRealGit(t, embeddedRoot, "config", "user.email", "tao@example.invalid")
	if err := os.WriteFile(filepath.Join(embeddedRoot, "tracked.txt"), []byte("embedded contents\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runRealGit(t, embeddedRoot, "add", "tracked.txt")
	runRealGit(t, embeddedRoot, "commit", "-m", "initial embedded repository")

	boundary, err := snapshotGitSessionBoundary(context.Background(), fixture.repoRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer boundary.cleanup()
	controlPath := filepath.Join(embeddedRoot, ".git")
	if !slices.Contains(boundary.nestedControlRoots, controlPath) || !slices.Contains(boundary.protectedGitWritePaths(), controlPath) {
		t.Fatalf("nested Git directory is outside the exact and read-only boundaries: roots=%q protected=%q", boundary.nestedControlRoots, boundary.protectedGitWritePaths())
	}
	configPath := filepath.Join(controlPath, "config")
	if err := os.WriteFile(configPath, []byte("[external]\n\towner = concurrent\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := compareNestedGitControls(context.Background(), boundary); err == nil {
		t.Fatal("changed nested Git directory was accepted")
	}
	contents, err := os.ReadFile(configPath) //nolint:gosec // fixture-owned concurrent metadata is preservation evidence.
	if err != nil || !strings.Contains(string(contents), "owner = concurrent") {
		t.Fatalf("concurrent nested Git metadata was overwritten: %q, %v", contents, err)
	}
}

func TestGuardedSingleConflictResolverRejectsAndPreservesConcurrentNestedSubmoduleGitControl(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(string) error
	}{
		{
			name: "alter",
			mutate: func(path string) error {
				return os.WriteFile(path, []byte("gitdir: forbidden\n"), 0o600)
			},
		},
		{name: "delete", mutate: os.Remove},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			const submodulePath = "deps/child"
			fixture, request, git := preparedSingleResolutionFixtureWithSetup(t, func(fixture realGitWorktree) {
				origin := filepath.Join(t.TempDir(), "child-origin")
				if err := os.MkdirAll(origin, 0o700); err != nil {
					t.Fatal(err)
				}
				runRealGit(t, origin, "init", "-b", "main")
				runRealGit(t, origin, "config", "user.name", "Tao Test")
				runRealGit(t, origin, "config", "user.email", "tao@example.invalid")
				if err := os.WriteFile(filepath.Join(origin, "child.txt"), []byte("submodule contents\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				runRealGit(t, origin, "add", "child.txt")
				runRealGit(t, origin, "commit", "-m", "initial child")

				runRealGit(t, fixture.repoRoot, "-c", "protocol.file.allow=always", "submodule", "add", origin, submodulePath)
				runRealGit(t, fixture.repoRoot, "commit", "-m", "add child submodule")
				addition := realGitOutput(t, fixture.repoRoot, "rev-parse", "HEAD")
				runRealGit(t, fixture.worktreePath, "cherry-pick", addition)
			})
			controlPath := filepath.Join(fixture.repoRoot, filepath.FromSlash(submodulePath), ".git")
			if _, err := os.Lstat(controlPath); err != nil {
				t.Fatal(err)
			}
			startExternalMutation := make(chan struct{})
			externalMutationDone := make(chan error, 1)
			go func() {
				<-startExternalMutation
				externalMutationDone <- tt.mutate(controlPath)
			}()
			store := &recordingSingleResolutionStore{current: request.Intent}
			resolver := GuardedSingleConflictResolver{Git: git, Recorder: store, Agent: batchSessionAgentFunc(func(_ context.Context, session BatchAgentSessionRequest) (BatchAgentSessionResult, error) {
				if !slices.Contains(session.ProtectedGitWritePaths, controlPath) {
					t.Fatalf("submodule control file is outside read-only confinement: %q", session.ProtectedGitWritePaths)
				}
				close(startExternalMutation)
				if err := <-externalMutationDone; err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(session.IntegrationRoot, "README.md"), []byte("combined\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				return BatchAgentSessionResult{Output: batchResolutionJSON("resolved while concurrent submodule metadata changed")}, nil
			})}

			if _, err := resolver.ResolveConflict(context.Background(), request); !errors.Is(err, ErrSingleResolutionRejected) || !strings.Contains(err.Error(), "changed concurrently") {
				t.Fatalf("concurrent nested Git-control mutation error = %v", err)
			}
			switch tt.name {
			case "alter":
				after, err := os.ReadFile(controlPath) //nolint:gosec // fixture-owned concurrent metadata is preservation evidence.
				if err != nil || string(after) != "gitdir: forbidden\n" {
					t.Fatalf("concurrent submodule control contents were overwritten: %q, %v", after, err)
				}
			case "delete":
				if _, err := os.Lstat(controlPath); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("concurrently deleted submodule control was recreated: %v", err)
				}
			}
			contents, err := os.ReadFile(filepath.Join(fixture.repoRoot, "README.md"))
			if err != nil || string(contents) != "combined\n" {
				t.Fatalf("concurrent-drift rejection unexpectedly rolled back the integration worktree: %q, %v", contents, err)
			}
			if got := realGitOutput(t, fixture.repoRoot, "rev-parse", fixture.defaultBranch); got != request.Intent.DefaultParent {
				t.Fatalf("nested-control rejection moved default ref: %s", got)
			}
			if got := realGitOutput(t, fixture.repoRoot, "rev-parse", fixture.planBranch); got != request.Intent.SourceHead {
				t.Fatalf("nested-control rejection moved source ref: %s", got)
			}
		})
	}
}

func TestGuardedSingleConflictResolverRollsBackCreatedUnsupportedNestedGitControls(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("unsupported nested Git control fixtures require Unix filesystem types")
	}
	tests := []struct {
		name   string
		create func(string) error
	}{
		{
			name: "symlink",
			create: func(path string) error {
				return os.Symlink("missing-provider-target", path)
			},
		},
		{
			name: "named pipe",
			create: func(path string) error {
				return exec.Command("mkfifo", path).Run() //nolint:gosec // fixed command creates a fixture-owned unsupported filesystem type.
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixture, request, git := preparedSingleResolutionFixture(t)
			store := &recordingSingleResolutionStore{current: request.Intent}
			createdRoot := filepath.Join(fixture.repoRoot, "provider-created")
			controlPath := filepath.Join(createdRoot, ".git")
			resolver := GuardedSingleConflictResolver{Git: git, Recorder: store, Agent: batchResolutionAgentFunc(func(_ context.Context, root, _ string) (string, error) {
				if err := os.MkdirAll(createdRoot, 0o700); err != nil {
					return "", err
				}
				if err := tt.create(controlPath); err != nil {
					return "", err
				}
				return batchResolutionJSON("created forbidden nested control"), os.WriteFile(filepath.Join(root, "README.md"), []byte("combined\n"), 0o600)
			})}

			if _, err := resolver.ResolveConflict(context.Background(), request); !errors.Is(err, ErrSingleResolutionRejected) || !strings.Contains(err.Error(), "resolver created nested Git control metadata") {
				t.Fatalf("created unsupported nested-control error = %v", err)
			}
			if store.records != 1 || store.advances != 0 || store.current.Resolution == nil || store.current.Resolution.Phase != plan.SingleMergeResolutionPhaseRequested {
				t.Fatalf("unsupported nested control gained resolution authority: records=%d advances=%d intent=%+v", store.records, store.advances, store.current)
			}
			if _, err := os.Lstat(controlPath); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("transaction-created unsupported nested control remains: %v", err)
			}
			if _, err := os.Lstat(createdRoot); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("ordinary resolver rollback did not remove provider output: %v", err)
			}
			if status := strings.TrimSpace(realGitOutput(t, fixture.repoRoot, "status", "--porcelain")); status != "" {
				t.Fatalf("unsupported nested-control rollback left dirty worktree: %q", status)
			}
			if contents, err := os.ReadFile(filepath.Join(fixture.repoRoot, "README.md")); err != nil || string(contents) != "default\n" {
				t.Fatalf("unsupported nested-control rollback contents = %q, %v", contents, err)
			}
			if got := realGitOutput(t, fixture.repoRoot, "rev-parse", fixture.defaultBranch); got != request.Intent.DefaultParent {
				t.Fatalf("unsupported nested-control rejection moved default ref: %s", got)
			}
			if got := realGitOutput(t, fixture.repoRoot, "rev-parse", fixture.planBranch); got != request.Intent.SourceHead {
				t.Fatalf("unsupported nested-control rejection moved source ref: %s", got)
			}
		})
	}
}

func TestGitSessionBoundaryRemovesOnlyTransactionCreatedNestedControl(t *testing.T) {
	fixture := newRealGitWorktree(t)
	boundary, err := snapshotGitSessionBoundary(context.Background(), fixture.repoRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer boundary.cleanup()

	embeddedRoot := filepath.Join(fixture.repoRoot, "provider-created")
	controlPath := filepath.Join(embeddedRoot, ".git")
	if err := os.MkdirAll(controlPath, 0o700); err != nil {
		t.Fatal(err)
	}
	markerPath := filepath.Join(embeddedRoot, "provider-output.txt")
	if err := os.WriteFile(markerPath, []byte("leave ordinary rollback to its owner\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	created, err := inspectNestedGitControls(context.Background(), boundary)
	if err == nil || !slices.Equal(created, []string{controlPath}) {
		t.Fatalf("created nested controls = %q, %v", created, err)
	}
	if err := removeCreatedNestedGitControls(context.Background(), boundary, created); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(controlPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("transaction-created nested control remains: %v", err)
	}
	contents, err := os.ReadFile(markerPath) //nolint:gosec // fixture-owned marker proves cleanup stayed control-scoped.
	if err != nil || string(contents) != "leave ordinary rollback to its owner\n" {
		t.Fatalf("nested-control cleanup removed ordinary provider output: %q, %v", contents, err)
	}
}

const interruptedGitBoundaryRootEnv = "TAO_TEST_INTERRUPTED_GIT_BOUNDARY_ROOT"
const interruptedGitBoundaryReadyEnv = "TAO_TEST_INTERRUPTED_GIT_BOUNDARY_READY"

func TestGitSessionBoundaryLeavesObjectModesUnchangedAfterInterruption(t *testing.T) {
	if root := os.Getenv(interruptedGitBoundaryRootEnv); root != "" {
		ready := os.Getenv(interruptedGitBoundaryReadyEnv)
		boundary, err := snapshotGitSessionBoundary(context.Background(), root)
		if err != nil {
			_ = os.WriteFile(ready, []byte(err.Error()), 0o600) //nolint:gosec // parent test supplies the private synchronization path.
			return
		}
		if err := os.WriteFile(ready, []byte("ready"), 0o600); err != nil { //nolint:gosec // parent test supplies the private synchronization path.
			t.Fatal(err)
		}
		_ = boundary
		for {
			time.Sleep(time.Hour)
		}
	}
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("Git session confinement is supported only on macOS and Linux")
	}

	fixture := newRealGitWorktree(t)
	paths, before := objectModeProbe(t, fixture.repoRoot)
	ready := filepath.Join(t.TempDir(), "ready")
	cmd := exec.Command(os.Args[0], "-test.run=^TestGitSessionBoundaryLeavesObjectModesUnchangedAfterInterruption$") //nolint:gosec // fixed test binary exercises abrupt process interruption.
	cmd.Env = append(os.Environ(), interruptedGitBoundaryRootEnv+"="+fixture.repoRoot, interruptedGitBoundaryReadyEnv+"="+ready, "TMPDIR="+t.TempDir())
	var output strings.Builder
	cmd.Stdout = &output
	cmd.Stderr = &output
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	waited := false
	defer func() {
		if !waited {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	}()

	deadline := time.Now().Add(10 * time.Second)
	for {
		contents, err := os.ReadFile(ready) //nolint:gosec // fixture-owned child synchronization file.
		if err == nil {
			if string(contents) != "ready" {
				t.Fatalf("interrupted boundary helper failed: %s", contents)
			}
			break
		}
		if !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for interrupted boundary helper: %s", output.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
	assertObjectModes(t, paths, before, "while session is active")
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err == nil {
		t.Fatal("interrupted boundary helper exited without being killed")
	}
	waited = true
	assertObjectModes(t, paths, before, "after abrupt interruption")
}

func TestOverlappingGitSessionBoundariesLeaveSharedObjectModesUnchanged(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("Git session confinement is supported only on macOS and Linux")
	}

	fixture := newRealGitWorktree(t)
	paths, before := objectModeProbe(t, fixture.repoRoot)
	first, err := snapshotGitSessionBoundary(context.Background(), fixture.repoRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer first.cleanup()
	assertObjectModes(t, paths, before, "during first session")

	second, err := snapshotGitSessionBoundary(context.Background(), fixture.worktreePath)
	if err != nil {
		t.Fatal(err)
	}
	defer second.cleanup()
	assertObjectModes(t, paths, before, "while sessions overlap")

	first.cleanup()
	assertObjectModes(t, paths, before, "after first session cleanup")
	second.cleanup()
	assertObjectModes(t, paths, before, "after second session cleanup")
}

func objectModeProbe(t *testing.T, root string) ([]string, map[string]os.FileMode) {
	t.Helper()
	objectRoot, err := gitBoundaryPath(context.Background(), root, "--path-format=absolute", "--git-common-dir")
	if err != nil {
		t.Fatal(err)
	}
	objectRoot = filepath.Join(objectRoot, "objects")
	objectID := strings.TrimSpace(realGitOutput(t, root, "rev-parse", "HEAD:README.md"))
	if len(objectID) < 3 {
		t.Fatalf("object ID = %q", objectID)
	}
	paths := []string{objectRoot, filepath.Join(objectRoot, objectID[:2]), filepath.Join(objectRoot, objectID[:2], objectID[2:])}
	modes := []os.FileMode{0o753, 0o731, 0o642}
	for i, path := range paths {
		if err := os.Chmod(path, modes[i]); err != nil {
			t.Fatal(err)
		}
	}
	return paths, captureObjectModes(t, paths)
}

func captureObjectModes(t *testing.T, paths []string) map[string]os.FileMode {
	t.Helper()
	modes := make(map[string]os.FileMode, len(paths))
	for _, path := range paths {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		modes[path] = info.Mode().Perm()
	}
	return modes
}

func assertObjectModes(t *testing.T, paths []string, want map[string]os.FileMode, stage string) {
	t.Helper()
	got := captureObjectModes(t, paths)
	for _, path := range paths {
		if got[path] != want[path] {
			t.Fatalf("object mode changed %s for %s: got %04o want %04o", stage, path, got[path], want[path])
		}
	}
}

func TestGuardedSingleConflictResolverRejectsObjectDatabaseWritesAndPreservesHistory(t *testing.T) {
	for _, mutation := range objectDatabaseMutationTestCases() {
		t.Run(mutation.name, func(t *testing.T) {
			fixture, request, git := preparedSingleResolutionFixture(t)
			objectRoot, historicalObject := historicalObjectPath(t, fixture.repoRoot, request.Intent.DefaultParent)
			requireGitObjectConfinement(t, fixture.repoRoot, objectRoot)
			before := objectDatabaseFingerprint(t, objectRoot)
			historicalContents, err := os.ReadFile(historicalObject) //nolint:gosec // fixture-owned loose Git object is exact preservation evidence.
			if err != nil {
				t.Fatal(err)
			}
			store := &recordingSingleResolutionStore{current: request.Intent}
			resolver := GuardedSingleConflictResolver{Git: git, Recorder: store, Agent: batchSessionAgentFunc(func(ctx context.Context, session BatchAgentSessionRequest) (BatchAgentSessionResult, error) {
				if session.ProtectedGitObjectRoot != objectRoot {
					t.Fatalf("protected object root = %q, want %q", session.ProtectedGitObjectRoot, objectRoot)
				}
				protected := strings.Join(session.ProtectedGitWritePaths, "\x00")
				if !strings.Contains(protected, filepath.Join(fixture.repoRoot, ".git")) || !strings.Contains(protected, fixture.worktreePath) {
					t.Fatalf("single resolver confinement omitted Git metadata or linked checkout: %q", session.ProtectedGitWritePaths)
				}
				mutationErr := mutation.run(ctx, session.IntegrationRoot, session.ProtectedGitObjectRoot, historicalObject)
				if mutationErr == nil || !strings.Contains(mutationErr.Error(), "object database mutation denied") {
					t.Fatalf("provider object mutation was not denied after execution: %v", mutationErr)
				}
				return BatchAgentSessionResult{Output: batchResolutionJSON("must reject object mutation")}, mutationErr
			})}

			if _, err := resolver.ResolveConflict(context.Background(), request); !errors.Is(err, ErrSingleResolutionRejected) || !strings.Contains(err.Error(), "provider session failed") {
				t.Fatalf("object mutation error = %v", err)
			}
			assertObjectDatabasePreserved(t, objectRoot, historicalObject, before, historicalContents)
			if status := strings.TrimSpace(realGitOutput(t, fixture.repoRoot, "status", "--porcelain")); status != "" {
				t.Fatalf("object-mutation rejection left dirty worktree: %q", status)
			}
			if got := strings.TrimSpace(realGitOutput(t, fixture.repoRoot, "rev-parse", fixture.defaultBranch)); got != request.Intent.DefaultParent {
				t.Fatalf("object-mutation rejection moved default ref: %s", got)
			}
			if got := strings.TrimSpace(realGitOutput(t, fixture.repoRoot, "rev-parse", fixture.planBranch)); got != request.Intent.SourceHead {
				t.Fatalf("object-mutation rejection moved source ref: %s", got)
			}
		})
	}
}

type objectDatabaseMutationTestCase struct {
	name   string
	script string
}

func objectDatabaseMutationTestCases() []objectDatabaseMutationTestCase {
	const writeObject = `chmod -R u+w "$1" 2>/dev/null
printf 'forbidden provider object\n' | git -C "$2" hash-object -w --stdin >/dev/null 2>&1
result=$?
printf ran
exit "$result"`
	const deleteHistoricalObject = `chmod -R u+w "$1" 2>/dev/null
rm "$4" 2>/dev/null
result=$?
printf ran
exit "$result"`
	return []objectDatabaseMutationTestCase{
		{name: "write object", script: writeObject},
		{name: "delete historical object", script: deleteHistoricalObject},
	}
}

func (m objectDatabaseMutationTestCase) run(ctx context.Context, root, objectRoot, historicalObject string) error {
	return runConfinedObjectMutation(ctx, root, objectRoot, historicalObject, m.script)
}

func requireGitObjectConfinement(t *testing.T, integrationRoot, objectRoot string) {
	t.Helper()
	runtimeRoot := t.TempDir()
	for _, dir := range []string{"cache", "state"} {
		if err := os.Mkdir(filepath.Join(runtimeRoot, dir), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	name, args, err := singleMergeFilesystemConfinementCommand(singleMergeFilesystemConfinement{
		protectedPaths: []string{objectRoot}, integrationRoot: integrationRoot, allowEdits: true,
	}, runtimeRoot, "/bin/sh", []string{"-c", "true"})
	if err != nil {
		t.Skipf("OS-enforced Git object confinement unavailable: %v", err)
	}
	if output, err := exec.Command(name, args...).CombinedOutput(); err != nil { //nolint:gosec // fixed test shell probes the generated confinement boundary.
		t.Skipf("OS-enforced Git object confinement cannot start: %v: %s", err, output)
	}
}

func runConfinedObjectMutation(ctx context.Context, root, objectRoot, historicalObject, script string) error {
	ctx = context.WithValue(ctx, singleMergeFilesystemConfinementContextKey{}, singleMergeFilesystemConfinement{
		protectedPaths: []string{objectRoot}, integrationRoot: root, allowEdits: true,
	})
	process, err := singleMergeFilesystemConfiningProcessStarter(agent.DefaultProcessStarter, exec.LookPath)(ctx, root, "/bin/sh", []string{"-c", script, "sh", objectRoot, root, "", historicalObject})
	if err != nil {
		return fmt.Errorf("start confined object mutation: %w", err)
	}
	_ = process.Stdin().Close()
	var stdout, stderr []byte
	var stdoutErr, stderrErr error
	done := make(chan struct{}, 2)
	go func() { stdout, stdoutErr = io.ReadAll(process.Stdout()); done <- struct{}{} }()
	go func() { stderr, stderrErr = io.ReadAll(process.Stderr()); done <- struct{}{} }()
	waitErr := process.Wait()
	<-done
	<-done
	if !strings.Contains(string(stdout), "ran") {
		return fmt.Errorf("confined mutation did not execute: %s%s", stdout, stderr)
	}
	if stdoutErr != nil || stderrErr != nil {
		return errors.Join(stdoutErr, stderrErr)
	}
	if waitErr == nil {
		return errors.New("provider unexpectedly mutated the protected object database")
	}
	return fmt.Errorf("object database mutation denied: %w: %s%s", waitErr, stdout, stderr)
}

func historicalObjectPath(t *testing.T, root, head string) (string, string) {
	t.Helper()
	objectRoot, err := gitBoundaryPath(context.Background(), root, "--path-format=absolute", "--git-common-dir")
	if err != nil {
		t.Fatal(err)
	}
	objectRoot = filepath.Join(objectRoot, "objects")
	objectID := strings.TrimSpace(realGitOutput(t, root, "rev-parse", head+"^:README.md"))
	if len(objectID) < 3 {
		t.Fatalf("historical object ID = %q", objectID)
	}
	path := filepath.Join(objectRoot, objectID[:2], objectID[2:])
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("historical loose object %s is unavailable: %v", objectID, err)
	}
	return objectRoot, path
}

func objectDatabaseFingerprint(t *testing.T, root string) string {
	t.Helper()
	hash := sha256.New()
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		_, _ = fmt.Fprintf(hash, "%s\\x00%s\\x00", filepath.ToSlash(rel), info.Mode())
		if info.Mode().IsRegular() {
			contents, err := os.ReadFile(path) //nolint:gosec // fixture-owned object contents are test evidence.
			if err != nil {
				return err
			}
			_, _ = hash.Write(contents)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf("%x", hash.Sum(nil))
}

func assertObjectDatabasePreserved(t *testing.T, objectRoot, historicalObject, before string, historicalContents []byte) {
	t.Helper()
	if after := objectDatabaseFingerprint(t, objectRoot); after != before {
		t.Fatalf("object database changed: got %s want %s", after, before)
	}
	contents, err := os.ReadFile(historicalObject) //nolint:gosec // fixture-owned loose Git object is exact preservation evidence.
	if err != nil || !slices.Equal(contents, historicalContents) {
		t.Fatalf("historical object was not preserved exactly: %v", err)
	}
}

func TestGuardedSingleConflictResolverPreservesConcurrentLinkedCheckoutEdits(t *testing.T) {
	fixture, request, git := preparedSingleResolutionFixture(t)
	checkouts := prepareLinkedCheckoutMutationFixture(t, fixture)
	store := &recordingSingleResolutionStore{current: request.Intent}
	resolver := GuardedSingleConflictResolver{Git: git, Recorder: store, Agent: batchResolutionAgentFunc(func(_ context.Context, _ string, _ string) (string, error) {
		done := make(chan error, 1)
		go func() {
			var errs []error
			for i, root := range checkouts.roots {
				errs = append(errs,
					os.WriteFile(filepath.Join(root, "README.md"), []byte(fmt.Sprintf("concurrent tracked edit %d\n", i)), 0o600),
					os.WriteFile(filepath.Join(root, ".linked-checkout-secret"), []byte(fmt.Sprintf("concurrent secret edit %d\n", i)), 0o600),
				)
			}
			done <- errors.Join(errs...)
		}()
		return batchResolutionJSON("observed concurrent checkout edits"), <-done
	})}

	if _, err := resolver.ResolveConflict(context.Background(), request); !errors.Is(err, ErrSingleResolutionRejected) || !strings.Contains(err.Error(), "linked checkout filesystem changed") {
		t.Fatalf("linked-checkout mutation error = %v", err)
	}
	checkouts.assertMutated(t, "concurrent")
}

func TestGitSessionBoundaryResolvesLinkedWorktreeGitdir(t *testing.T) {
	fixture := newRealGitWorktree(t)
	boundary, err := snapshotGitSessionBoundary(context.Background(), fixture.worktreePath)
	if err != nil {
		t.Fatal(err)
	}
	defer boundary.cleanup()
	if boundary.gitDir == filepath.Join(fixture.worktreePath, ".git") || boundary.gitDir == boundary.commonDir {
		t.Fatalf("linked-worktree gitdir was not resolved: gitdir=%q common=%q", boundary.gitDir, boundary.commonDir)
	}
	index := filepath.Join(boundary.gitDir, "index")
	if _, ok := boundary.metadata[index]; !ok {
		t.Fatalf("active linked-worktree index %q is outside metadata boundary", index)
	}
	commonExclude := filepath.Join(boundary.commonDir, "info", "exclude")
	if _, ok := boundary.metadata[commonExclude]; !ok {
		t.Fatalf("common exclude %q is outside metadata boundary", commonExclude)
	}
	worktreeConfig := filepath.Join(boundary.gitDir, "config.worktree")
	if err := os.WriteFile(worktreeConfig, []byte("[tao]\n\tforbidden = true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := compareGitSessionBoundary(context.Background(), boundary); err == nil || !strings.Contains(err.Error(), "metadata changed") {
		t.Fatalf("linked-worktree metadata mutation was accepted: %v", err)
	}
	if contents, err := os.ReadFile(worktreeConfig); err != nil || !strings.Contains(string(contents), "forbidden") { //nolint:gosec // fixture-owned concurrent metadata must be preserved.
		t.Fatalf("concurrent linked-worktree config was overwritten: %q, %v", contents, err)
	}
}

func TestSingleResolutionSharedGuardsHandleDeleteRenameSymlinkAndQuotedPaths(t *testing.T) {
	changes, err := parsePorcelainV1Z("R  renamed name.txt\x00old name.txt\x00D  deleted.txt\x00 M quoted\tname.txt\x00")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"deleted.txt", "old name.txt", "quoted\tname.txt", "renamed name.txt"}
	if !slices.Equal(changes.changedPaths, want) {
		t.Fatalf("concrete changed paths = %q, want %q", changes.changedPaths, want)
	}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "renamed name.txt"), []byte("resolved\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "quoted\tname.txt"), []byte("quoted\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("renamed name.txt", filepath.Join(root, "link.txt")); err != nil {
		t.Fatal(err)
	}
	paths := []string{"deleted.txt", "link.txt", "old name.txt", "quoted\tname.txt", "renamed name.txt"}
	first, err := resolutionContentFingerprint(root, paths)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, "link.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("quoted\tname.txt", filepath.Join(root, "link.txt")); err != nil {
		t.Fatal(err)
	}
	second, err := resolutionContentFingerprint(root, paths)
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("symlink target drift did not change exact content fingerprint")
	}
}

type linkedCheckoutMutationFixture struct {
	roots []string
}

func prepareLinkedCheckoutMutationFixture(t *testing.T, fixture realGitWorktree) linkedCheckoutMutationFixture {
	t.Helper()
	configureSingleAgentIgnoredPaths(t, fixture.repoRoot, ".linked-checkout-secret")
	otherRoot := filepath.Join(filepath.Dir(fixture.repoRoot), "other-worktree")
	runRealGit(t, fixture.repoRoot, "worktree", "add", "-b", "tao/other-checkout", otherRoot, fixture.defaultBranch)
	result := linkedCheckoutMutationFixture{roots: []string{fixture.worktreePath, otherRoot}}
	for i, root := range result.roots {
		secret := []byte(fmt.Sprintf("original secret %d\n", i))
		if err := os.WriteFile(filepath.Join(root, ".linked-checkout-secret"), secret, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return result
}

func (f linkedCheckoutMutationFixture) mutate(t *testing.T) {
	t.Helper()
	for i, root := range f.roots {
		if err := os.WriteFile(filepath.Join(root, "README.md"), []byte(fmt.Sprintf("forbidden tracked edit %d\n", i)), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, ".linked-checkout-secret"), []byte(fmt.Sprintf("forbidden secret edit %d\n", i)), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func (f linkedCheckoutMutationFixture) assertMutated(t *testing.T, marker string) {
	t.Helper()
	for _, root := range f.roots {
		tracked, trackedErr := os.ReadFile(filepath.Join(root, "README.md"))               //nolint:gosec // temporary real-Git fixture path.
		ignored, ignoredErr := os.ReadFile(filepath.Join(root, ".linked-checkout-secret")) //nolint:gosec // temporary real-Git fixture path.
		if trackedErr != nil || !strings.Contains(string(tracked), marker) {
			t.Fatalf("concurrent tracked edit in %s was overwritten: %q, %v", root, tracked, trackedErr)
		}
		if ignoredErr != nil || !strings.Contains(string(ignored), marker) {
			t.Fatalf("concurrent ignored edit in %s was overwritten: %q, %v", root, ignored, ignoredErr)
		}
	}
}

func configureSingleAgentIgnoredPaths(t *testing.T, root string, paths ...string) {
	t.Helper()
	exclude := filepath.Join(root, ".git", "info", "exclude")
	file, err := os.OpenFile(exclude, os.O_APPEND|os.O_WRONLY, 0o600) //nolint:gosec // test path is rooted in a temporary Git fixture.
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString("\n" + strings.Join(paths, "\n") + "\n"); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

func realGitAdminDir(t *testing.T, root string) string {
	t.Helper()
	path, err := gitBoundaryPath(context.Background(), root, "--absolute-git-dir")
	if err != nil {
		t.Fatal(err)
	}
	return path
}

func realGitRefLockPath(t *testing.T, root, branch string) string {
	t.Helper()
	commonDir, err := gitBoundaryPath(context.Background(), root, "--path-format=absolute", "--git-common-dir")
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(commonDir, "refs", "heads", filepath.FromSlash(branch)+".lock")
}

func preparedSingleResolutionFixture(t *testing.T) (realGitWorktree, SingleResolutionRequest, GitClient) {
	t.Helper()
	return preparedSingleResolutionFixtureWithSetup(t, nil)
}

func preparedSingleResolutionFixtureForMarkerSize(t *testing.T, markerSize int) (realGitWorktree, SingleResolutionRequest, GitClient) {
	t.Helper()
	if markerSize == 0 {
		return preparedSingleResolutionFixture(t)
	}
	return preparedSingleResolutionFixtureWithSetup(t, func(fixture realGitWorktree) {
		attributes := []byte(fmt.Sprintf("README.md conflict-marker-size=%d\n", markerSize))
		for _, root := range []string{fixture.repoRoot, fixture.worktreePath} {
			if err := os.WriteFile(filepath.Join(root, ".gitattributes"), attributes, 0o600); err != nil {
				t.Fatal(err)
			}
			runRealGit(t, root, "add", ".gitattributes")
			runRealGit(t, root, "commit", "-m", "configure conflict markers")
		}
	})
}

func preparedSingleResolutionFixtureWithSourceMarkerSize(t *testing.T, markerSize int) (realGitWorktree, SingleResolutionRequest, GitClient) {
	t.Helper()
	fixture, request, git := preparedSingleResolutionFixtureForMarkerSize(t, markerSize)
	request.ChangedFiles = []string{".gitattributes", "README.md"}
	return fixture, request, git
}

func preparedSingleResolutionFixtureWithSetup(t *testing.T, setup func(realGitWorktree)) (realGitWorktree, SingleResolutionRequest, GitClient) {
	t.Helper()
	fixture, _, _, _ := batchAgentConflictFixture(t)
	if setup != nil {
		setup(fixture)
	}
	sourceHead := strings.TrimSpace(realGitOutput(t, fixture.repoRoot, "rev-parse", "refs/heads/"+fixture.planBranch))
	defaultHead := strings.TrimSpace(realGitOutput(t, fixture.repoRoot, "rev-parse", "HEAD"))
	merge := exec.Command("git", "merge", "--squash", fixture.planBranch) //nolint:gosec // fixed git command with fixture-owned branch.
	merge.Dir = fixture.repoRoot
	if err := merge.Run(); err == nil {
		t.Fatal("expected prepared squash conflict")
	}
	message, err := singleMergeCommitMessage(plan.ReviewCommitMessage{
		Subject: "feat(merge): integrate reviewed source",
		Body:    "What:\nIntegrate the reviewed source.\n\nWhy:\nPreserve its approved intent.",
	}, "plan-a", sourceHead)
	if err != nil {
		t.Fatal(err)
	}
	intent := plan.SingleMergeCommitIntent{
		Message: message, PlanID: "plan-a", SourceHead: sourceHead,
		DefaultBranch: fixture.defaultBranch, DefaultParent: defaultHead,
		CreatedAt: time.Now().Add(-time.Minute).UTC(),
	}
	status := realGitOutput(t, fixture.repoRoot, "status", "--porcelain")
	request := SingleResolutionRequest{
		Intent: intent, SourceBranch: fixture.planBranch, IntegrationRoot: fixture.repoRoot,
		PlanTitle: "Resolve README interaction", SourceReview: defaultHead + ".." + sourceHead,
		ChangedFiles: []string{"README.md"}, ConflictFiles: []string{"README.md"},
		ConflictStatus: status, VerifyCommand: "go test ./...",
	}
	return fixture, request, gitops.NewClient(fixture.repoRoot, nil)
}

func assertSingleResolutionMarkerRollback(t *testing.T, fixture realGitWorktree, request SingleResolutionRequest, markerSize int) {
	t.Helper()
	if head := strings.TrimSpace(realGitOutput(t, fixture.repoRoot, "rev-parse", "HEAD")); head != request.Intent.DefaultParent {
		t.Fatalf("marker rollback left HEAD at %s", head)
	}
	if got := strings.TrimSpace(realGitOutput(t, fixture.repoRoot, "rev-parse", fixture.defaultBranch)); got != request.Intent.DefaultParent {
		t.Fatalf("marker rollback moved default ref: %s", got)
	}
	if got := strings.TrimSpace(realGitOutput(t, fixture.repoRoot, "rev-parse", fixture.planBranch)); got != request.Intent.SourceHead {
		t.Fatalf("marker rollback moved source ref: %s", got)
	}
	if status := strings.TrimSpace(realGitOutput(t, fixture.repoRoot, "status", "--porcelain")); status != "" {
		t.Fatalf("marker rollback left dirty worktree: %q", status)
	}
	contents, err := os.ReadFile(filepath.Join(fixture.repoRoot, "README.md"))
	if err != nil || string(contents) != "default\n" {
		t.Fatalf("marker rollback contents = %q, %v", contents, err)
	}
	attributes, err := os.ReadFile(filepath.Join(fixture.repoRoot, ".gitattributes"))
	wantAttributes := fmt.Sprintf("README.md conflict-marker-size=%d\n", markerSize)
	if err != nil || string(attributes) != wantAttributes {
		t.Fatalf("marker rollback attributes = %q, %v; want %q", attributes, err, wantAttributes)
	}
}
