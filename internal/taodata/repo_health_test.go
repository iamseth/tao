package taodata

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRepoHealthCheckerClassifiesRepos(t *testing.T) {
	gitRoot := newGitRepo(t)
	notGit := t.TempDir()
	missing := filepath.Join(t.TempDir(), "missing")

	tests := []struct {
		name string
		repo Repo
		want string
	}{
		{name: "ok", repo: Repo{Root: gitRoot}, want: RepoHealthOK},
		{name: "missing root", repo: Repo{Root: missing}, want: RepoHealthMissingRoot},
		{name: "empty root", repo: Repo{}, want: RepoHealthMissingRoot},
		{name: "not git", repo: Repo{Root: notGit}, want: RepoHealthNotGitRepo},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := (RepoHealthChecker{}).Check(context.Background(), test.repo)
			if got.Status != test.want {
				t.Fatalf("status = %q, want %q; message=%q", got.Status, test.want, got.Message)
			}
			if test.want == RepoHealthOK && got.Error {
				t.Fatalf("ok repo should not be error: %#v", got)
			}
			if test.want != RepoHealthOK && !got.Error {
				t.Fatalf("unhealthy repo should be error: %#v", got)
			}
		})
	}
}

func TestRepoHealthCheckerUsesInjectedProbes(t *testing.T) {
	probeErr := errors.New("no git")
	got := (RepoHealthChecker{
		Stat:      func(string) (os.FileInfo, error) { return fakeFileInfo{dir: true}, nil },
		GitInside: func(context.Context, string) error { return probeErr },
	}).Check(context.Background(), Repo{Root: "/repo"})
	if got.Status != RepoHealthNotGitRepo || !got.Error {
		t.Fatalf("unexpected health: %#v", got)
	}
}

type fakeFileInfo struct{ dir bool }

func (f fakeFileInfo) Name() string { return "repo" }
func (f fakeFileInfo) Size() int64  { return 0 }
func (f fakeFileInfo) Mode() os.FileMode {
	if f.dir {
		return os.ModeDir | 0o700
	}
	return 0o600
}
func (f fakeFileInfo) ModTime() time.Time { return time.Time{} }
func (f fakeFileInfo) IsDir() bool        { return f.dir }
func (f fakeFileInfo) Sys() any           { return nil }
