package commit

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

type preparedGitStub struct {
	staged      bool
	stagedErr   error
	commitErr   error
	revParseErr error
	sha         string
	calls       []string
	message     string
}

func (git *preparedGitStub) HasStagedChanges(context.Context) (bool, error) {
	git.calls = append(git.calls, "staged")
	return git.staged, git.stagedErr
}

func (git *preparedGitStub) Commit(_ context.Context, message string) error {
	git.calls = append(git.calls, "commit")
	git.message = message
	return git.commitErr
}

func (git *preparedGitStub) RevParse(_ context.Context, rev string) (string, error) {
	git.calls = append(git.calls, "rev-parse "+rev)
	return git.sha, git.revParseErr
}

func TestCommitPreparedUsesInjectedGitAndExactMessage(t *testing.T) {
	message, err := Format(validProposal())
	if err != nil {
		t.Fatal(err)
	}
	git := &preparedGitStub{staged: true, sha: "abc123"}

	result, err := CommitPrepared(context.Background(), git, message)
	if err != nil {
		t.Fatal(err)
	}
	if result.SHA != "abc123" || result.Subject != "feat(commit): centralize commit messages" {
		t.Fatalf("CommitPrepared() result = %#v", result)
	}
	if git.message != message {
		t.Fatalf("committed message changed\nwant:\n%s\n\ngot:\n%s", message, git.message)
	}
	if want := []string{"staged", "commit", "rev-parse HEAD"}; !reflect.DeepEqual(git.calls, want) {
		t.Fatalf("Git calls = %q, want %q", git.calls, want)
	}
}

func TestCommitPreparedStopsBeforeMutation(t *testing.T) {
	validMessage, err := Format(validProposal())
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name    string
		message string
		git     *preparedGitStub
		want    string
		calls   []string
	}{
		{name: "invalid message", message: "feat: invalid", git: &preparedGitStub{staged: true}, want: "validate", calls: nil},
		{name: "status failure", message: validMessage, git: &preparedGitStub{stagedErr: errors.New("status failed")}, want: "inspect", calls: []string{"staged"}},
		{name: "nothing staged", message: validMessage, git: &preparedGitStub{}, want: "requires staged changes", calls: []string{"staged"}},
		{name: "commit failure", message: validMessage, git: &preparedGitStub{staged: true, commitErr: errors.New("commit failed")}, want: "create", calls: []string{"staged", "commit"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := CommitPrepared(context.Background(), test.git, test.message); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("CommitPrepared() error = %v, want text %q", err, test.want)
			}
			if !reflect.DeepEqual(test.git.calls, test.calls) {
				t.Fatalf("Git calls = %q, want %q", test.git.calls, test.calls)
			}
		})
	}
}
