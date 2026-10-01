package gitops

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestReadOnlyProbes(t *testing.T) {
	root := t.TempDir()
	globals := []string{"-C", root, "--no-optional-locks", "-c", "diff.autoRefreshIndex=false", "-c", "core.fsmonitor=false", "-c", "core.quotePath=false", "--literal-pathspecs"}
	var calls [][]string
	c := NewReadOnlyClient(root, func(_ context.Context, cwd, name string, args []string, out, errout io.Writer) error {
		if cwd != "" || name != "git" {
			t.Fatalf("boundary %q %q", cwd, name)
		}
		if !reflect.DeepEqual(args[:len(globals)], globals) {
			t.Fatalf("globals %q", args)
		}
		calls = append(calls, args[len(globals):])
		return nil
	})
	_, _ = c.DefaultBranch(context.Background())
	want := [][]string{{"symbolic-ref", "--quiet", "--short", "refs/remotes/origin/HEAD"}, {"branch", "--format=%(refname:short)", "--list", "main"}, {"branch", "--format=%(refname:short)", "--list", "master"}}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls %q", calls)
	}
	calls = nil
	_, _ = c.MergeBase(context.Background(), "main", "HEAD")
	_, _ = c.IsAncestor(context.Background(), "main", "HEAD")
	want = [][]string{{"merge-base", "main", "HEAD"}, {"merge-base", "--is-ancestor", "main", "HEAD"}}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("base probes %q", calls)
	}
	for _, args := range [][]string{{"add", "."}, {"branch", "--show-current"}, {"branch", "--delete", "x"}, {"config", "x"}} {
		if err := c.git(context.Background(), args, io.Discard, io.Discard); err == nil {
			t.Fatalf("allowed %q", args)
		}
	}
	c = NewReadOnlyClient("relative", func(context.Context, string, string, []string, io.Writer, io.Writer) error {
		t.Fatal("runner called")
		return nil
	})
	if _, err := c.RevParse(context.Background(), "HEAD"); err == nil {
		t.Fatal("relative root accepted")
	}
}

func TestListsLinkedWorktreeReadOnly(t *testing.T) {
	root := t.TempDir()
	runGitCommand(t, root, "init", "-b", "main")
	disableGitFixtureMaintenance(t, root)
	runGitCommand(t, root, "config", "user.name", "Test")
	runGitCommand(t, root, "config", "user.email", "test@example.com")
	for _, name := range []string{"changed", "identical"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("original\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, ".gitattributes"), []byte("changed diff=sentinel\n"), 0600); err != nil {
		t.Fatal(err)
	}
	runGitCommand(t, root, "add", ".")
	runGitCommand(t, root, "commit", "-m", "initial")
	linked := filepath.Join(t.TempDir(), "linked")
	runGitCommand(t, root, "worktree", "add", "--detach", linked, "HEAD")
	sentinel := filepath.Join(t.TempDir(), "invoked")
	script := filepath.Join(t.TempDir(), "driver")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nprintf called > '"+sentinel+"'\nexit 1\n"), 0700); err != nil { //nolint:gosec // Private executable fixture.
		t.Fatal(err)
	}
	for _, kv := range [][2]string{{"color.ui", "always"}, {"diff.external", script}, {"diff.sentinel.textconv", script}, {"core.fsmonitor", script}, {"status.renames", "copies"}, {"diff.renames", "copies"}} {
		runGitCommand(t, root, "config", kv[0], kv[1])
	}
	if err := os.WriteFile(filepath.Join(linked, "changed"), []byte("changed\n"), 0600); err != nil {
		t.Fatal(err)
	}
	when := time.Now().Add(-time.Hour)
	if err := os.Chtimes(filepath.Join(linked, "identical"), when, when); err != nil {
		t.Fatal(err)
	}
	index := gitOutput(t, linked, "rev-parse", "--path-format=absolute", "--git-path", "index")
	before, err := os.ReadFile(index) //nolint:gosec // Git-resolved fixture index.
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(index+".lock", nil, 0600); err != nil {
		t.Fatal(err)
	}
	c := NewReadOnlyClient(linked, nil)
	names, _, err := c.DiffNameStatusZ(context.Background(), 10000, "HEAD")
	if err != nil || len(names) == 0 || names[0].Path != "changed" {
		t.Fatalf("names %+v %v", names, err)
	}
	nums, _, err := c.DiffNumstatZ(context.Background(), 10000, "HEAD")
	if err != nil || len(nums) != 1 || nums[0].Path != "changed" {
		t.Fatalf("numstat %+v %v", nums, err)
	}
	status, _, err := c.StatusPorcelainV2Z(context.Background(), 10000)
	if err != nil || !strings.Contains(status, "changed\x00") || strings.Contains(status, "\x1b") {
		t.Fatalf("status %q %v", status, err)
	}
	after, err := os.ReadFile(index) //nolint:gosec // Same fixture index.
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("index changed", err)
	}
	if _, err := os.Stat(sentinel); !os.IsNotExist(err) {
		t.Fatalf("driver ran: %v", err)
	}
}

type probeExit int

func (e probeExit) Error() string { return "exit" }
func (e probeExit) ExitCode() int { return int(e) }

func TestVerifyCommit(t *testing.T) {
	for _, tc := range []struct {
		name, out, stderr string
		err               error
		ok, wantErr       bool
	}{
		{"valid", strings.Repeat("a", 40) + "\n", "", nil, true, false},
		{"sha256", strings.Repeat("b", 64) + "\n", "", nil, true, false},
		{"absent", "", "", probeExit(1), false, false},
		{"stderr", "", "bad", probeExit(1), false, true},
		{"other", "", "", probeExit(128), false, true},
		{"cancel", "", "", context.Canceled, false, true},
		{"invalid", "HEAD\n", "", nil, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := NewClient(t.TempDir(), func(_ context.Context, cwd, name string, args []string, out, errout io.Writer) error {
				want := []string{"rev-parse", "--verify", "--quiet", "--end-of-options", "-ref^{commit}"}
				if !reflect.DeepEqual(args[len(args)-5:], want) {
					t.Fatalf("args %q", args)
				}
				_, _ = io.WriteString(out, tc.out)
				_, _ = io.WriteString(errout, tc.stderr)
				return tc.err
			})
			_, ok, err := c.VerifyCommit(context.Background(), "-ref")
			if ok != tc.ok || (err != nil) != tc.wantErr {
				t.Fatalf("ok=%v err=%v", ok, err)
			}
			if errors.Is(tc.err, context.Canceled) && !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
		})
	}
}
