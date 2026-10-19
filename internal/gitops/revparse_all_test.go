package gitops

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestRevParseAllResolvesInOrderAndFailsWhole(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	runGitCommand(t, root, "init", "-b", "main")
	if err := os.WriteFile(filepath.Join(root, "file"), []byte("one\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGitCommand(t, root, "add", "file")
	runGitCommand(t, root, "commit", "-m", "one")
	runGitCommand(t, root, "branch", "other")
	client := NewClient(root, nil)
	ctx := context.Background()
	want := make([]string, 0, 3)
	for _, rev := range []string{"refs/heads/other", "HEAD", "refs/heads/main"} {
		value, err := client.RevParse(ctx, rev)
		if err != nil {
			t.Fatal(err)
		}
		want = append(want, value)
	}
	got, err := client.RevParseAll(ctx, "refs/heads/other", "HEAD", "refs/heads/main")
	if err != nil || !slices.Equal(got, want) {
		t.Fatalf("RevParseAll = %q, %v; want %q", got, err, want)
	}
	if got, err := client.RevParseAll(ctx, "HEAD", "refs/heads/missing"); err == nil {
		t.Fatalf("missing revision resolved: %q", got)
	}
	if got, err := client.RevParseAll(ctx); got != nil || err != nil {
		t.Fatalf("empty = %q, %v", got, err)
	}
}
