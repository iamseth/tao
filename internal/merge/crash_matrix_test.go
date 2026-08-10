package merge

import (
	"context"
	"os/exec"
	"strings"
	"testing"

	"github.com/iamseth/tao/internal/durableintent/crashfixture"
	"github.com/iamseth/tao/internal/plan"
)

type defaultTargetCrashPoint struct {
	name         string
	build        func(*testing.T, *crashfixture.Fixture) crashfixture.State
	wantMutation bool
}

func defaultTargetCrashPoints() []defaultTargetCrashPoint {
	return []defaultTargetCrashPoint{
		{name: "before intent", build: func(_ *testing.T, f *crashfixture.Fixture) crashfixture.State { return f.BeforeIntent() }},
		{name: "after intent", build: func(_ *testing.T, f *crashfixture.Fixture) crashfixture.State { return f.AfterIntent() }},
		{name: "after git mutation", build: func(t *testing.T, f *crashfixture.Fixture) crashfixture.State {
			return f.AfterGitMutation(t, crashfixture.DefaultTarget)
		}, wantMutation: true},
		{name: "after settlement", build: func(t *testing.T, f *crashfixture.Fixture) crashfixture.State {
			return f.AfterSettlement(t, crashfixture.DefaultTarget)
		}, wantMutation: true},
	}
}

func TestInspectSingleMergeIntentCrashMatrix(t *testing.T) {
	for _, test := range defaultTargetCrashPoints() {
		t.Run(test.name, func(t *testing.T) {
			fixture := crashfixture.New(t)
			state := test.build(t, fixture)
			detail, intent := singleMergeCrashIntent(fixture)

			integrated, err := inspectSingleMergeIntent(context.Background(), fixture.Git, detail, intent)
			if err != nil {
				t.Fatalf("inspect at %s: %v", state.Point, err)
			}
			if integrated != test.wantMutation {
				t.Fatalf("integrated at %s = %t, want %t", state.Point, integrated, test.wantMutation)
			}
		})
	}
}

func TestInspectSingleMergeIntentRefusesLiveDefaultDrift(t *testing.T) {
	fixture := crashfixture.New(t)
	fixture.AfterGitMutation(t, crashfixture.DefaultTarget)
	detail, intent := singleMergeCrashIntent(fixture)
	intent.Message = "test: a different intended squash"

	integrated, err := inspectSingleMergeIntent(context.Background(), fixture.Git, detail, intent)
	if err == nil || !strings.Contains(err.Error(), "does not contain the exact intended squash") {
		t.Fatalf("mismatched live commit message returned integrated=%t, err=%v", integrated, err)
	}
	if integrated {
		t.Fatal("mismatched live commit message was accepted")
	}
}

func TestRecoverApplyingBatchIntegrationCrashMatrix(t *testing.T) {
	messageBranches := []struct {
		name          string
		commitMessage func(*crashfixture.Fixture) string
		override      bool
	}{
		{
			name: "tao squash trailers",
			commitMessage: func(f *crashfixture.Fixture) string {
				return batchCrashMessage("plan-crash", f.SourceSHA)
			},
		},
		{
			name:          "exact integration commit message override",
			commitMessage: func(*crashfixture.Fixture) string { return crashfixture.MutationMessage },
			override:      true,
		},
	}
	points := defaultTargetCrashPoints()

	for _, branch := range messageBranches {
		t.Run(branch.name, func(t *testing.T) {
			for _, point := range points {
				t.Run(point.name, func(t *testing.T) {
					fixture := crashfixture.New(t)
					state := point.build(t, fixture)
					message := branch.commitMessage(fixture)
					if state.MutationSHA != "" && message != crashfixture.MutationMessage {
						amendCrashCommitMessage(t, fixture, message)
					}
					candidate, integration := batchCrashIntent(fixture, message, branch.override)
					currentHead := crashRev(t, fixture, crashfixture.DefaultBranch)

					base, committed, err := recoverApplyingBatchIntegrationAtRevision(
						context.Background(), fixture.Git, candidate, integration, currentHead, "HEAD",
					)
					if err != nil {
						t.Fatalf("recover at %s: %v", state.Point, err)
					}
					if base != fixture.BaseSHA || committed != point.wantMutation {
						t.Fatalf("recover at %s = (%s, %t), want (%s, %t)", state.Point, base, committed, fixture.BaseSHA, point.wantMutation)
					}
				})
			}
		})
	}
}

func TestRecoverApplyingBatchIntegrationRefusesLiveGitDrift(t *testing.T) {
	tests := []struct {
		name     string
		override bool
		message  func(*crashfixture.Fixture) string
	}{
		{
			name: "tao squash trailers",
			message: func(f *crashfixture.Fixture) string {
				return batchCrashMessage("another-plan", f.SourceSHA)
			},
		},
		{
			name: "exact integration commit message override", override: true,
			message: func(*crashfixture.Fixture) string { return "test: wrong exact message" },
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := crashfixture.New(t)
			fixture.AfterGitMutation(t, crashfixture.DefaultTarget)
			candidate, integration := batchCrashIntent(fixture, crashfixture.MutationMessage, test.override)
			if !test.override {
				integration.CommitMessage = ""
			}
			commitCrashDrift(t, fixture, test.message(fixture))
			currentHead := crashRev(t, fixture, crashfixture.DefaultBranch)

			base, committed, err := recoverApplyingBatchIntegrationAtRevision(
				context.Background(), fixture.Git, candidate, integration, currentHead, "HEAD",
			)
			if err == nil || !strings.Contains(err.Error(), "does not match the intended Tao-owned squash") {
				t.Fatalf("mismatched live Git evidence returned (%q, %t, %v)", base, committed, err)
			}
			if base != "" || committed {
				t.Fatalf("mismatched live Git evidence was accepted: base=%q committed=%t", base, committed)
			}
		})
	}
}

func singleMergeCrashIntent(fixture *crashfixture.Fixture) (*plan.PlanDetail, plan.SingleMergeCommitIntent) {
	const planID = "plan-crash"
	detail := &plan.PlanDetail{State: plan.State{
		Plan: plan.PlanState{ID: planID},
		Workspace: &plan.Workspace{
			Branch: crashfixture.SourceBranch, BaseBranch: crashfixture.DefaultBranch,
		},
	}}
	intent := plan.SingleMergeCommitIntent{
		PlanID: planID, SourceHead: fixture.SourceSHA,
		DefaultBranch: crashfixture.DefaultBranch, DefaultParent: fixture.BaseSHA,
		Message: crashfixture.MutationMessage,
	}
	return detail, intent
}

func batchCrashIntent(fixture *crashfixture.Fixture, message string, override bool) (BatchCandidate, BatchIntegration) {
	candidate := BatchCandidate{PlanID: "plan-crash", SourceTip: fixture.SourceSHA}
	integration := BatchIntegration{PlanID: candidate.PlanID, SourceHead: fixture.SourceSHA, IntegrationBaseSHA: fixture.BaseSHA}
	if override {
		integration.CommitMessage = message
	}
	return candidate, integration
}

func batchCrashMessage(planID, sourceHead string) string {
	return "test: integrate crash fixture\n\nTao-Plan: " + planID + "\nTao-Source-Head: " + sourceHead
}

func amendCrashCommitMessage(t *testing.T, fixture *crashfixture.Fixture, message string) {
	t.Helper()
	runCrashGit(t, fixture, "commit", "--amend", "-m", message)
}

func commitCrashDrift(t *testing.T, fixture *crashfixture.Fixture, message string) {
	t.Helper()
	runCrashGit(t, fixture, "commit", "--allow-empty", "-m", message)
}

func runCrashGit(t *testing.T, fixture *crashfixture.Fixture, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...) //nolint:gosec // Test invokes fixed git with test-owned arguments.
	cmd.Dir = fixture.RepoRoot
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s failed: %v\n%s", strings.Join(args, " "), err, output)
	}
}

func crashRev(t *testing.T, fixture *crashfixture.Fixture, revision string) string {
	t.Helper()
	sha, err := fixture.Git.RevParse(context.Background(), revision)
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(sha)
}
