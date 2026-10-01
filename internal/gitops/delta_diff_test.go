package gitops

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestDeltaDiffStreams(t *testing.T) {
	payload := strings.Repeat("patch\n", 100)
	for _, configured := range []bool{false, true} {
		for _, chunk := range []int{1, 17, 600} {
			root := t.TempDir()
			runner := func(_ context.Context, cwd, name string, args []string, out, stderr io.Writer) error {
				want := []string{"-C", root, "--no-optional-locks", "-c", "diff.autoRefreshIndex=false", "-c", "core.fsmonitor=false", "-c", "core.quotePath=false", "--literal-pathspecs", "diff", "--no-color", "--no-ext-diff", "--no-textconv", "--no-renames", "--src-prefix=a/", "--dst-prefix=b/", "HEAD", "--", ":(glob)*", "-file"}
				if cwd != "" || name != "git" || !reflect.DeepEqual(args, want) {
					t.Fatalf("boundary %q %q %q", cwd, name, args)
				}
				for start := 0; start < len(payload); start += chunk {
					p := payload[start:min(start+chunk, len(payload))]
					if n, err := io.WriteString(out, p); n != len(p) || err != nil {
						t.Fatalf("drain %d %v", n, err)
					}
				}
				return nil
			}
			c := NewClient(root, runner)
			if configured {
				c = NewReadOnlyClient(root, runner)
			}
			args := []string{"HEAD", "--", ":(glob)*", "-file"}
			got, truncated, err := c.DiffBoundedArgs(context.Background(), 11, args...)
			if got != payload[:11] || !truncated || err != nil {
				t.Fatalf("bounded %q %v %v", got, truncated, err)
			}
			digest, err := c.DiffDigest(context.Background(), args...)
			if digest != fmt.Sprintf("%x", sha256.Sum256([]byte(payload))) || err != nil {
				t.Fatalf("digest %q %v", digest, err)
			}
			for _, limit := range []int{0, -1} {
				if _, _, err := c.DiffBoundedArgs(context.Background(), limit); err == nil {
					t.Fatal("invalid limit")
				}
			}
		}
	}
	c := NewClient(t.TempDir(), func(_ context.Context, _, _ string, _ []string, _, stderr io.Writer) error {
		p := strings.Repeat("x", probeOutputLimit*2)
		if n, err := io.WriteString(stderr, p); n != len(p) || err != nil {
			t.Fatal("stderr not drained")
		}
		return probeExit(2)
	})
	for _, digest := range []bool{false, true} {
		var err error
		if digest {
			_, err = c.DiffDigest(context.Background())
		} else {
			_, _, err = c.DiffBoundedArgs(context.Background(), 4)
		}
		if err == nil || len(err.Error()) > probeOutputLimit+1024 || !strings.Contains(err.Error(), "stderr truncated") {
			t.Fatal("unbounded or missing diagnostic")
		}
	}
}

func TestDeltaDiffLinkedWorktreeReadOnly(t *testing.T) {
	root := t.TempDir()
	runGitCommand(t, root, "init", "-b", "main")
	disableGitFixtureMaintenance(t, root)
	runGitCommand(t, root, "config", "user.name", "Test")
	runGitCommand(t, root, "config", "user.email", "test@example.com")
	write := func(root, path, text string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, path), []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"modified", "staged", "deleted", "binary", "identical", ":(glob)*", "-file"} {
		write(root, name, "original\n")
	}
	write(root, ".gitattributes", "modified diff=sentinel\n")
	runGitCommand(t, root, "add", ".")
	runGitCommand(t, root, "commit", "-m", "initial")
	linked := filepath.Join(t.TempDir(), "linked")
	runGitCommand(t, root, "worktree", "add", "--detach", linked, "HEAD")
	sentinel := filepath.Join(t.TempDir(), "invoked")
	script := filepath.Join(t.TempDir(), "sentinel")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nprintf called >> '"+sentinel+"'\nexit 1\n"), 0700); err != nil { //nolint:gosec // Executable sentinel in a private test fixture.
		t.Fatal(err)
	}
	for _, kv := range [][2]string{{"diff.mnemonicPrefix", "true"}, {"diff.noprefix", "true"}, {"color.ui", "always"}, {"diff.external", script}, {"diff.sentinel.textconv", script}, {"core.fsmonitor", script}} {
		runGitCommand(t, root, "config", kv[0], kv[1])
	}
	write(linked, "staged", "staged change\n")
	runGitCommand(t, linked, "-c", "core.fsmonitor=false", "add", "staged")
	for _, name := range []string{"modified", ":(glob)*", "-file"} {
		write(linked, name, "changed\n")
	}
	write(linked, "binary", "\x00binary\x00")
	write(linked, "identical", "original\n")
	if err := os.Remove(filepath.Join(linked, "deleted")); err != nil {
		t.Fatal(err)
	}
	index := gitOutput(t, linked, "rev-parse", "--path-format=absolute", "--git-path", "index")
	before, err := os.ReadFile(index) //nolint:gosec // Index resolved by Git in our local linked-worktree fixture.
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(index+".lock", nil, 0600); err != nil {
		t.Fatal(err)
	}
	c := NewReadOnlyClient(linked, nil)
	for _, path := range []string{"", "modified", "staged", "deleted", "binary", "identical", ":(glob)*", "-file"} {
		args := []string{"HEAD"}
		if path != "" {
			args = append(args, "--", path)
		}
		patch, _, err := c.DiffBoundedArgs(context.Background(), 100000, args...)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(patch, "\x1b") {
			t.Fatal("color escaped pinning")
		}
		if path != "identical" && (!strings.Contains(patch, "a/") || !strings.Contains(patch, "b/")) {
			t.Fatalf("prefixes %q", patch)
		}
		if path == "identical" && patch != "" {
			t.Fatalf("identical patch %q", patch)
		}
		after, err := os.ReadFile(index) //nolint:gosec // Same fixture index, compared byte-for-byte.
		if err != nil || !bytes.Equal(before, after) {
			t.Fatal("patch changed index", err)
		}
		if _, err := c.DiffDigest(context.Background(), args...); err != nil {
			t.Fatal(err)
		}
		after, err = os.ReadFile(index) //nolint:gosec // Same fixture index, compared byte-for-byte.
		if err != nil || !bytes.Equal(before, after) {
			t.Fatal("digest changed index", err)
		}
	}
	if _, err := os.Stat(sentinel); !os.IsNotExist(err) {
		t.Fatalf("sentinel ran: %v", err)
	}
}
