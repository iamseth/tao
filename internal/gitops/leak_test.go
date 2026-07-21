package gitops

import (
	"context"
	"io"
	"strings"
	"testing"
)

func TestDirtyFingerprintCombinesStatusDiffAndPaths(t *testing.T) {
	runner := &fakeRunner{outputs: map[string]string{
		key("-C", "/repo", "status", "--porcelain"):       " M dirty.go\n?? new.txt\n",
		key("-C", "/repo", "diff", "HEAD"):                "diff --git a/dirty.go b/dirty.go\n-old\n+new\n",
		key("-C", "/repo", "diff", "--name-only", "HEAD"): "dirty.go\n",
	}, stderr: map[string]string{}, failures: map[string]error{}}

	fingerprint, err := NewClient("/repo", runner.run).DirtyFingerprint(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if fingerprint.Hash == "" {
		t.Fatal("expected fingerprint hash")
	}
	if got, want := strings.Join(fingerprint.Paths, ","), "dirty.go,new.txt"; got != want {
		t.Fatalf("paths mismatch: got %q want %q", got, want)
	}
}

func TestDirtyFingerprintChangesForTrackedContentOnlyEdit(t *testing.T) {
	calls := 0
	runner := func(ctx context.Context, cwd string, name string, args []string, stdout io.Writer, stderr io.Writer) error {
		_ = ctx
		_ = cwd
		_ = stderr
		if name != "git" {
			t.Fatalf("unexpected command %q", name)
		}
		switch strings.Join(args, "\x00") {
		case key("-C", "/repo", "status", "--porcelain"):
			_, _ = io.WriteString(stdout, " M dirty.go\n")
		case key("-C", "/repo", "diff", "HEAD"):
			calls++
			if calls == 1 {
				_, _ = io.WriteString(stdout, "diff --git a/dirty.go b/dirty.go\n-old\n+first\n")
			} else {
				_, _ = io.WriteString(stdout, "diff --git a/dirty.go b/dirty.go\n-old\n+second\n")
			}
		case key("-C", "/repo", "diff", "--name-only", "HEAD"):
			_, _ = io.WriteString(stdout, "dirty.go\n")
		default:
			t.Fatalf("unexpected git args %#v", args)
		}
		return nil
	}
	client := NewClient("/repo", runner)

	before, err := client.DirtyFingerprint(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	after, err := client.DirtyFingerprint(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if before.Hash == after.Hash {
		t.Fatal("expected content-only edit to change fingerprint")
	}
}
