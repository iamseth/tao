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

func TestCommitStagedPreservesTrailerOnlyMessage(t *testing.T) {
	message := "Integrate legacy plan\n\nTao-Plan: legacy-plan\nTao-Source-Head: abc123"
	if err := ValidateMessage(message); err == nil {
		t.Fatal("trailer-only message unexpectedly passed validation")
	}
	git := &preparedGitStub{staged: true, sha: "def456"}
	result, err := CommitStaged(context.Background(), git, message)
	if err != nil {
		t.Fatal(err)
	}
	if want := (Result{SHA: "def456", Subject: "Integrate legacy plan"}); result != want {
		t.Fatalf("CommitStaged() = %#v, want %#v", result, want)
	}
	if git.message != message {
		t.Fatalf("committed message = %q, want %q", git.message, message)
	}
	if want := []string{"staged", "commit", "rev-parse HEAD"}; !reflect.DeepEqual(git.calls, want) {
		t.Fatalf("Git calls = %q, want %q", git.calls, want)
	}
}

func TestCommitPhaseErrors(t *testing.T) {
	message, err := Format(validProposal())
	if err != nil {
		t.Fatal(err)
	}
	underlying := errors.New("git failed")
	for _, helper := range []struct {
		name string
		call func(context.Context, PreparedGit, string) (Result, error)
	}{
		{name: "CommitPrepared", call: CommitPrepared},
		{name: "CommitStaged", call: CommitStaged},
	} {
		t.Run(helper.name, func(t *testing.T) {
			for _, test := range []struct {
				name       string
				git        *preparedGitStub
				want       string
				calls      []string
				noStaged   bool
				created    bool
				underlying bool
			}{
				{name: "status failure", git: &preparedGitStub{stagedErr: underlying}, want: "inspect prepared commit: git failed", calls: []string{"staged"}, underlying: true},
				{name: "empty staging", git: &preparedGitStub{}, want: "prepared commit requires staged changes", calls: []string{"staged"}, noStaged: true},
				{name: "commit failure", git: &preparedGitStub{staged: true, commitErr: underlying}, want: "create prepared commit: git failed", calls: []string{"staged", "commit"}, underlying: true},
				{name: "resolve failure", git: &preparedGitStub{staged: true, revParseErr: underlying}, want: "resolve prepared commit: git failed", calls: []string{"staged", "commit", "rev-parse HEAD"}, created: true, underlying: true},
				{name: "empty HEAD", git: &preparedGitStub{staged: true}, want: "resolve prepared commit: empty HEAD", calls: []string{"staged", "commit", "rev-parse HEAD"}, created: true},
			} {
				t.Run(test.name, func(t *testing.T) {
					result, err := helper.call(context.Background(), test.git, message)
					if err == nil || err.Error() != test.want {
						t.Fatalf("error = %v, want %q", err, test.want)
					}
					if result != (Result{}) {
						t.Fatalf("result = %#v, want zero result", result)
					}
					if errors.Is(err, ErrNoStagedChanges) != test.noStaged || errors.Is(err, ErrCommitCreated) != test.created {
						t.Fatalf("error = %v, want no-staged=%v, created=%v", err, test.noStaged, test.created)
					}
					if errors.Is(err, underlying) != test.underlying {
						t.Fatalf("error = %v, want underlying Git error=%v", err, test.underlying)
					}
					if !reflect.DeepEqual(test.git.calls, test.calls) {
						t.Fatalf("Git calls = %q, want %q", test.git.calls, test.calls)
					}
				})
			}
			t.Run("nil Git", func(t *testing.T) {
				_, err := helper.call(context.Background(), nil, message)
				if err == nil || err.Error() != "prepared commit requires Git" {
					t.Fatalf("error = %v, want nil Git refusal", err)
				}
				if errors.Is(err, ErrCommitCreated) || errors.Is(err, ErrNoStagedChanges) {
					t.Fatalf("nil Git refusal matched a phase sentinel: %v", err)
				}
			})
		})
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
