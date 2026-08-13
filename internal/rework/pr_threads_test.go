package rework

import (
	"context"
	"errors"
	"io"
	"os"
	"slices"
	"strings"
	"testing"
)

func TestPRThreadReaderNormalizesFixture(t *testing.T) {
	fixture, err := os.ReadFile("testdata/pr_threads.json")
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name      string
		scope     PRThreadAuthorScope
		wantIDs   []string
		wantOwner string
	}{
		{
			name:      "owner is the default",
			wantIDs:   []string{"PRRT_owner", "PRRT_outdated", "PRRT_fileless"},
			wantOwner: "tao-owner",
		},
		{
			name:      "all includes non-owner threads",
			scope:     PRThreadAuthorsAll,
			wantIDs:   []string{"PRRT_owner", "PRRT_outdated", "PRRT_non_owner", "PRRT_fileless"},
			wantOwner: "tao-owner",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			calls := 0
			runner := func(ctx context.Context, cwd string, name string, args []string, stdout, _ io.Writer) error {
				calls++
				if err := ctx.Err(); err != nil {
					return err
				}
				if cwd != "/repo" || name != "gh" {
					t.Fatalf("unexpected command location: cwd=%q name=%q", cwd, name)
				}
				joined := strings.Join(args, " ")
				for _, want := range []string{"api graphql", "--paginate", "viewer { login }", "reviewThreads(first: 100", "pullRequestReview { state }", "owner=iamseth", "name=tao", "number=123"} {
					if !strings.Contains(joined, want) {
						t.Fatalf("gh arguments %q do not contain %q", joined, want)
					}
				}
				_, err := stdout.Write(fixture)
				return err
			}

			result, err := (PRThreadReader{CommandRunner: runner}).Read(context.Background(), PRThreadReadRequest{
				RepoRoot:          "/repo",
				RepositoryOwner:   "iamseth",
				RepositoryName:    "tao",
				PullRequestNumber: 123,
				AuthorScope:       tt.scope,
			})
			if err != nil {
				t.Fatal(err)
			}
			if calls != 1 {
				t.Fatalf("gh calls = %d, want 1", calls)
			}
			if result.OwnerLogin != tt.wantOwner {
				t.Fatalf("owner login = %q, want %q", result.OwnerLogin, tt.wantOwner)
			}
			gotIDs := make([]string, 0, len(result.Threads))
			for _, thread := range result.Threads {
				gotIDs = append(gotIDs, thread.NodeID)
				if thread.IsResolved {
					t.Fatalf("resolved thread was retained: %#v", thread)
				}
			}
			if !slices.Equal(gotIDs, tt.wantIDs) {
				t.Fatalf("thread IDs = %v, want %v", gotIDs, tt.wantIDs)
			}

			owner := findPRThread(t, result.Threads, "PRRT_owner")
			if len(owner.Comments) != 3 {
				t.Fatalf("deduplicated owner comments = %d, want 3", len(owner.Comments))
			}
			if got := []string{owner.Comments[0].NodeID, owner.Comments[1].NodeID, owner.Comments[2].NodeID}; !slices.Equal(got, []string{"PRRC_owner_root", "PRRC_owner_reply", "PRRC_owner_reply_2"}) {
				t.Fatalf("ordered comment IDs = %v", got)
			}
			if owner.Comments[1].AuthorLogin != "reviewer" {
				t.Fatalf("reply author = %q, want reviewer", owner.Comments[1].AuthorLogin)
			}
			for _, comment := range owner.Comments {
				if comment.NodeID == "PRRC_owner_pending_reply" {
					t.Fatalf("pending review reply was retained: %#v", comment)
				}
			}

			outdated := findPRThread(t, result.Threads, "PRRT_outdated")
			if !outdated.IsOutdated || outdated.Line != nil {
				t.Fatalf("outdated thread did not retain null line: %#v", outdated)
			}
			fileless := findPRThread(t, result.Threads, "PRRT_fileless")
			if fileless.Path != "" || fileless.Line != nil {
				t.Fatalf("file-less thread location = path %q line %v", fileless.Path, fileless.Line)
			}
		})
	}
}

func TestPRThreadReaderReportsCommandAndGraphQLErrors(t *testing.T) {
	tests := []struct {
		name      string
		runner    func(context.Context, string, string, []string, io.Writer, io.Writer) error
		wantError string
	}{
		{
			name: "command failure includes stderr",
			runner: func(_ context.Context, _ string, _ string, _ []string, _, stderr io.Writer) error {
				_, _ = io.WriteString(stderr, "authentication required")
				return errors.New("exit status 1")
			},
			wantError: "authentication required",
		},
		{
			name: "GraphQL errors are rejected",
			runner: func(_ context.Context, _ string, _ string, _ []string, stdout, _ io.Writer) error {
				_, _ = io.WriteString(stdout, `{"errors":[{"message":"pull request not found"}]}`)
				return nil
			},
			wantError: "pull request not found",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := (PRThreadReader{CommandRunner: tt.runner}).Read(context.Background(), PRThreadReadRequest{
				RepoRoot:          "/repo",
				RepositoryOwner:   "iamseth",
				RepositoryName:    "tao",
				PullRequestNumber: 123,
			})
			if err == nil || !strings.Contains(err.Error(), tt.wantError) {
				t.Fatalf("error = %v, want containing %q", err, tt.wantError)
			}
		})
	}
}

func findPRThread(t *testing.T, threads []PRThread, id string) PRThread {
	t.Helper()
	for _, thread := range threads {
		if thread.NodeID == id {
			return thread
		}
	}
	t.Fatalf("thread %s not found", id)
	return PRThread{}
}
