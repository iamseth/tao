package run

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/iamseth/tao/internal/durableintent/crashfixture"
	"github.com/iamseth/tao/internal/plan"
)

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
