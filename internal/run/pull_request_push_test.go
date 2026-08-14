package run

import (
	"context"
	"strings"
	"testing"

	"github.com/iamseth/tao/internal/plan"
)

func TestPullRequestPushBranchPreservesLegacyUntypedPush(t *testing.T) {
	detail := approvedPullRequestDetail("", "head-new")
	var calls []string
	creator := deterministicPullRequestCreator{execution: testRunExecution(ExecutionConfig{}, RunDependencies{
		CommandRunner: pullRequestCommandRunner(t, &calls, nil),
	})}

	err := creator.pushBranch(context.Background(), PullRequestRun{
		Detail:   detail,
		RepoRoot: "/repo",
		Branch:   "legacy/plan-a",
		HeadSHA:  "head-new",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 || calls[0] != "git push --set-upstream origin legacy/plan-a" {
		t.Fatalf("legacy push calls = %#v", calls)
	}
}

func TestPullRequestPushLeasePolicy(t *testing.T) {
	tests := []struct {
		name            string
		branch          string
		head            string
		workspaceBranch string
		recordedHead    string
		remoteHead      string
		remoteFound     bool
		wantLease       string
		wantErr         string
	}{
		{
			name:      "absent remote requires empty expected head",
			branch:    "feature/plan-a",
			head:      "head-new",
			wantLease: "",
		},
		{
			name:        "identical published head is idempotent",
			branch:      "feature/plan-a",
			head:        "head-new",
			remoteHead:  "head-new",
			remoteFound: true,
			wantLease:   "head-new",
		},
		{
			name:            "owned unchanged head retains exact lease",
			branch:          "feature/plan-a",
			head:            "head-old",
			workspaceBranch: "feature/plan-a",
			recordedHead:    "head-old",
			remoteHead:      "head-old",
			remoteFound:     true,
			wantLease:       "head-old",
		},
		{
			name:            "owned rework advances recorded boundary",
			branch:          "feature/plan-a",
			head:            "head-new",
			workspaceBranch: "feature/plan-a",
			recordedHead:    "head-old",
			remoteHead:      "head-old",
			remoteFound:     true,
			wantLease:       "head-old",
		},
		{
			name:            "stale owned remote is rejected",
			branch:          "feature/plan-a",
			head:            "head-new",
			workspaceBranch: "feature/plan-a",
			recordedHead:    "head-old",
			remoteHead:      "head-foreign",
			remoteFound:     true,
			wantErr:         "want recorded Tao head head-old or new reviewed head head-new",
		},
		{
			name:            "missing owned remote is rejected",
			branch:          "feature/plan-a",
			head:            "head-new",
			workspaceBranch: "feature/plan-a",
			recordedHead:    "head-old",
			wantErr:         "remote branch is missing, want recorded Tao head head-old",
		},
		{
			name:        "unowned existing remote is rejected",
			branch:      "feature/plan-a",
			head:        "head-new",
			remoteHead:  "head-foreign",
			remoteFound: true,
			wantErr:     "remote branch already exists at head-foreign",
		},
		{
			name:            "recorded legacy Tao branch can advance",
			branch:          "tao/plan-a",
			head:            "head-new",
			workspaceBranch: "tao/plan-a",
			recordedHead:    "head-old",
			remoteHead:      "head-old",
			remoteFound:     true,
			wantLease:       "head-old",
		},
		{
			name:            "different recorded branch conveys no ownership",
			branch:          "feature/plan-a",
			head:            "head-new",
			workspaceBranch: "feature/other",
			recordedHead:    "head-old",
			remoteHead:      "head-old",
			remoteFound:     true,
			wantErr:         "remote branch already exists at head-old",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			detail := approvedPullRequestDetail(plan.ChangeTypeFeat, tt.head)
			if tt.recordedHead != "" || tt.workspaceBranch != "" {
				detail.State.Workspace = &plan.Workspace{Branch: tt.workspaceBranch, PushedSHA: tt.recordedHead}
			}
			run := PullRequestRun{Detail: detail, Branch: tt.branch, HeadSHA: tt.head}

			lease, err := pullRequestPushLease(run, tt.remoteHead, tt.remoteFound)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("pullRequestPushLease() error = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if lease != tt.wantLease {
				t.Fatalf("pullRequestPushLease() = %q, want %q", lease, tt.wantLease)
			}
		})
	}
}
