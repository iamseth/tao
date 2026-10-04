package agentsession

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/iamseth/tao/internal/gitops"

	"github.com/iamseth/tao/internal/agent"
)

func TestRunnerDetectsControlCheckoutLeak(t *testing.T) {
	controlRoot := t.TempDir()
	runGit(t, controlRoot, "init")
	runGit(t, controlRoot, "config", "user.email", "test@example.com")
	runGit(t, controlRoot, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(controlRoot, "tracked.txt"), []byte("base\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, controlRoot, "add", "tracked.txt")
	runGit(t, controlRoot, "commit", "-m", "base")

	calls := 0
	runtime := runtimeFunc(func(context.Context, agent.Session) (agent.SessionResult, error) {
		calls++
		if err := os.WriteFile(filepath.Join(controlRoot, "tracked.txt"), []byte("leaked\n"), 0o600); err != nil {
			return agent.SessionResult{}, err
		}
		return agent.SessionResult{Output: "partial"}, nil
	})
	runner := New(Config{Descriptor: agent.Descriptor{NewRuntime: func(agent.RuntimeDeps) agent.Runtime { return runtime }}})
	result, err := runner.Run(context.Background(), Request{ControlRoot: controlRoot, RepoRoot: t.TempDir()})
	var leak ControlCheckoutLeakError
	if !errors.As(err, &leak) {
		t.Fatalf("error = %v, want ControlCheckoutLeakError", err)
	}
	if calls != 1 || !result.Invoked || result.Output != "partial" || len(leak.Paths) != 1 || leak.Paths[0] != "tracked.txt" {
		t.Fatalf("calls/result/leak = %d, %+v, %+v", calls, result, leak)
	}
}

func TestRunnerSkipsLeakFingerprintForControlCheckoutSession(t *testing.T) {
	calls := 0
	runtime := runtimeFunc(func(context.Context, agent.Session) (agent.SessionResult, error) {
		calls++
		return agent.SessionResult{}, nil
	})
	runner := New(Config{Descriptor: agent.Descriptor{NewRuntime: func(agent.RuntimeDeps) agent.Runtime { return runtime }}, CommandRunner: func(context.Context, string, string, []string, io.Writer, io.Writer) error {
		t.Fatal("same-root session invoked git")
		return nil
	}})
	if _, err := runner.Run(context.Background(), Request{ControlRoot: "/same", RepoRoot: "/same"}); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("provider calls = %d, want 1", calls)
	}
}

func TestRunnerDoesNotReportInvocationForPreSessionGuardFailure(t *testing.T) {
	guardErr := errors.New("fingerprint unavailable")
	runner := New(Config{
		Runtime: runtimeFunc(func(context.Context, agent.Session) (agent.SessionResult, error) {
			t.Fatal("provider invoked despite guard failure")
			return agent.SessionResult{}, nil
		}),
		CommandRunner: func(context.Context, string, string, []string, io.Writer, io.Writer) error { return guardErr },
	})
	result, err := runner.Run(context.Background(), Request{ControlRoot: t.TempDir(), RepoRoot: t.TempDir(), CollectMetrics: true})
	if !errors.Is(err, guardErr) || result.Invoked {
		t.Fatalf("result/error = %+v, %v", result, err)
	}
}

func TestControlCheckoutChangeEvidence(t *testing.T) {
	for _, tc := range []struct {
		name   string
		dirty  bool
		mutate func(*testing.T, string)
		want   []ControlCheckoutPathChange
		head   bool
	}{
		{"tracked modification", true, func(t *testing.T, root string) { writeLeakFile(t, root, "tracked.txt", "changed again\n") }, []ControlCheckoutPathChange{{"tracked.txt", true, "modified"}}, false},
		{"untracked appearance", false, func(t *testing.T, root string) { writeLeakFile(t, root, "new.txt", "new\n") }, []ControlCheckoutPathChange{{"new.txt", false, "appeared"}}, false},
		{"dirty disappearance", true, func(t *testing.T, root string) { runGit(t, root, "restore", "tracked.txt") }, []ControlCheckoutPathChange{{"tracked.txt", true, "disappeared"}}, false},
		{"empty commit head move", false, func(t *testing.T, root string) { runGit(t, root, "commit", "--allow-empty", "-m", "move") }, nil, true},
		{"head move", false, func(t *testing.T, root string) {
			writeLeakFile(t, root, "tracked.txt", "committed\n")
			runGit(t, root, "commit", "-am", "move")
		}, nil, true},
		{"rename", false, func(t *testing.T, root string) { runGit(t, root, "mv", "tracked.txt", "renamed.txt") }, []ControlCheckoutPathChange{{"renamed.txt", true, "appeared"}, {"tracked.txt", true, "appeared"}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			runGit(t, root, "init")
			runGit(t, root, "config", "user.name", "Test")
			runGit(t, root, "config", "user.email", "test@example.invalid")
			writeLeakFile(t, root, "tracked.txt", "base\n")
			runGit(t, root, "add", ".")
			runGit(t, root, "commit", "-m", "base")
			if tc.dirty {
				writeLeakFile(t, root, "tracked.txt", "dirty\n")
			}
			git := gitops.NewClient(root, nil)
			before, err := git.RevParse(context.Background(), "HEAD")
			if err != nil {
				t.Fatal(err)
			}
			_, err = guardControlCheckoutLeaks(context.Background(), nil, root, t.TempDir(), func() (int, error) { tc.mutate(t, root); return 42, nil }, nil)
			var leak ControlCheckoutLeakError
			if !errors.As(err, &leak) {
				t.Fatalf("expected leak, got %v", err)
			}
			if !reflect.DeepEqual(leak.Change.Changes, tc.want) {
				t.Fatalf("changes = %+v, want %+v", leak.Change.Changes, tc.want)
			}
			if leak.Change.ControlRoot != root || !reflect.DeepEqual(leak.Paths, leak.Change.Paths) {
				t.Fatalf("inconsistent evidence: %+v", leak)
			}
			if strings.Contains(leak.Error(), "(unable to determine paths)") {
				t.Fatal(leak.Error())
			}
			if tc.head {
				after, err := git.RevParse(context.Background(), "HEAD")
				if err != nil {
					t.Fatal(err)
				}
				if leak.Change.HeadBefore != before || leak.Change.HeadAfter != after || before == after {
					t.Fatalf("head evidence = %+v", leak.Change)
				}
				if !strings.Contains(leak.Error(), "; HEAD moved "+before+" -> "+after) {
					t.Fatal(leak.Error())
				}
				t.Log(leak.Error())
			}
		})
	}
}

func TestChangedLeakPathsRetainsPersistentDirtyPaths(t *testing.T) {
	for _, disappearance := range []bool{false, true} {
		before := gitops.DirtyFingerprint{Paths: []string{"owned.go"}}
		after := gitops.DirtyFingerprint{Paths: []string{"owned.go"}}
		kind := "appeared"
		if disappearance {
			before.Paths = append(before.Paths, "unrelated.txt")
			before.Untracked = []string{"unrelated.txt"}
			kind = "disappeared"
		} else {
			after.Paths = append(after.Paths, "unrelated.txt")
			after.Untracked = []string{"unrelated.txt"}
		}
		want := []ControlCheckoutPathChange{{"owned.go", true, "modified"}, {"unrelated.txt", false, kind}}
		if got := changedLeakPaths(before, after); !reflect.DeepEqual(got, want) {
			t.Fatalf("changes = %+v, want %+v", got, want)
		}
	}
}

func TestControlCheckoutLeakMessage(t *testing.T) {
	legacy := ControlCheckoutLeakError{ControlRoot: "/control", Paths: []string{"a"}}
	if !strings.Contains(legacy.Error(), "a (tracked, modified)") {
		t.Fatal(legacy.Error())
	}
	empty := ControlCheckoutLeakError{ControlRoot: "/control"}
	if !strings.Contains(empty.Error(), "index or staged content changed without working-tree path differences") {
		t.Fatal(empty.Error())
	}
	change := ControlCheckoutChange{ControlRoot: "/control"}
	for range 23 {
		change.Changes = append(change.Changes, ControlCheckoutPathChange{strings.Repeat("界", 100), false, "appeared"})
	}
	message := (ControlCheckoutLeakError{Change: change}).Error()
	if utf8.RuneCountInString(strings.SplitN(message, "\n", 2)[0]) >= 512 || !strings.Contains(message, "and 3 more") {
		t.Fatal(message)
	}
}

func writeLeakFile(t *testing.T, root, path, contents string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, path), []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}

func runGit(t *testing.T, cwd string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...) // #nosec G204 -- test helper invokes fixed git with test-owned arguments.
	cmd.Dir = cwd
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, output)
	}
}
