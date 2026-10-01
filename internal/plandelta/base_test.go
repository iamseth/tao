package plandelta

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/iamseth/tao/internal/plan"
)

type baseGit struct {
	defaultBranch, base string
	defaultErr, baseErr error
	calls               []string
}

func (g *baseGit) DefaultBranch(context.Context) (string, error) {
	g.calls = append(g.calls, "default")
	return g.defaultBranch, g.defaultErr
}
func (g *baseGit) MergeBase(_ context.Context, a, b string) (string, error) {
	g.calls = append(g.calls, a+" "+b)
	return g.base, g.baseErr
}

func TestResolveBase(t *testing.T) {
	for _, tt := range []struct {
		name  string
		ws    *plan.Workspace
		repo  string
		git   baseGit
		want  Base
		calls []string
	}{
		{"live trimmed", &plan.Workspace{Branch: " topic ", BaseSHA: " old "}, "repo", baseGit{defaultBranch: " main ", base: " live "}, Base{"live", BaseSourceLiveMergeBase, "main", "topic"}, []string{"default", "main topic"}},
		{"default error fallback", &plan.Workspace{Branch: "topic", BaseBranch: " main "}, "", baseGit{defaultBranch: "ignored", defaultErr: errors.New("unavailable"), base: "live"}, Base{"live", BaseSourceLiveMergeBase, "main", "topic"}, []string{"default", "main topic"}},
		{"empty default fallback", &plan.Workspace{Branch: "topic", BaseBranch: "main"}, "", baseGit{base: "live"}, Base{"live", BaseSourceLiveMergeBase, "main", "topic"}, []string{"default", "main topic"}},
		{"same branch", &plan.Workspace{Branch: "main", BaseSHA: " old "}, "repo", baseGit{defaultBranch: "main"}, Base{"old", BaseSourceWorkspace, "main", "main"}, []string{"default"}},
		{"merge failure", &plan.Workspace{Branch: "topic", BaseSHA: "old"}, "repo", baseGit{defaultBranch: "main", baseErr: errors.New("unavailable")}, Base{"old", BaseSourceWorkspace, "main", "topic"}, []string{"default", "main topic"}},
		{"nil workspace", nil, " repo ", baseGit{}, Base{"repo", BaseSourceRepo, "", ""}, nil},
		{"empty branch", &plan.Workspace{Branch: " ", BaseSHA: "old"}, "", baseGit{}, Base{"old", BaseSourceWorkspace, "", ""}, nil},
		{"no default", &plan.Workspace{Branch: "topic"}, "repo", baseGit{}, Base{"repo", BaseSourceRepo, "", "topic"}, []string{"default"}},
		{"empty merge", &plan.Workspace{Branch: "topic"}, "repo", baseGit{defaultBranch: "main", base: " "}, Base{"repo", BaseSourceRepo, "main", "topic"}, []string{"default", "main topic"}},
		{"absent", nil, "", baseGit{}, Base{Source: BaseSourceNone}, nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			state := plan.State{Workspace: tt.ws}
			state.Repo.BaseCommit = tt.repo
			got := ResolveBase(context.Background(), &tt.git, state)
			if got != tt.want || !reflect.DeepEqual(tt.git.calls, tt.calls) {
				t.Fatalf("got %+v calls %v; want %+v calls %v", got, tt.git.calls, tt.want, tt.calls)
			}
			tt.git.calls = nil
			live := LiveMergeBase(context.Background(), &tt.git, state)
			if (tt.want.Source == BaseSourceLiveMergeBase && live != tt.want.SHA) || (tt.want.Source != BaseSourceLiveMergeBase && live != "") {
				t.Fatalf("live base = %q", live)
			}
		})
	}
}
