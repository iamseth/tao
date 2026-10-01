package plandelta

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestUntrackedDiff(t *testing.T) {
	root := t.TempDir()
	write := func(name, data string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, name), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("text", "\ttext\r\n+++payload\nlast")
	write("binary", "a\x00b")
	write("large", strings.Repeat("x", MaxUntrackedBytes+20))
	write("lines", strings.Repeat("x\n", MaxFileDiffLines+10))
	write("empty", "")
	write("exact", strings.Repeat("x", MaxUntrackedBytes))
	write("late-nul", strings.Repeat("x", 8192)+"\x00")
	write("large-binary", "\x00"+strings.Repeat("x", MaxUntrackedBytes))
	c := NewCollector(nil)
	get := func(path string) (FileDiff, error) {
		return c.FileDiff(context.Background(), Snapshot{Target: Target{WorktreePath: root}, Scope: ScopeWorktree, Signature: "sig", Files: []FileChange{{Path: path, Untracked: true}}}, path)
	}
	d, err := get("text")
	if err != nil || d.Signature != "sig" || len(d.Lines) != 4 || d.Lines[1].Text != "    text" || d.Lines[2].Text != "+++payload" || d.Lines[2].Kind != Add {
		t.Fatalf("%+v %v", d, err)
	}
	d, err = get("binary")
	if err != nil || !d.Binary || !strings.Contains(d.Lines[0].Text, "3 bytes") {
		t.Fatalf("%+v %v", d, err)
	}
	d, err = get("large")
	if err != nil || !d.Truncated || d.Lines[len(d.Lines)-1].Text != "[untracked file truncated at 256 KiB]" {
		t.Fatalf("%+v %v", d, err)
	}
	d, err = get("lines")
	if err != nil || !d.Truncated || len(d.Lines) != MaxFileDiffLines+1 || d.Lines[MaxFileDiffLines].Text != "[showing first 4000 lines]" {
		t.Fatalf("lines=%d %v", len(d.Lines), err)
	}
	d, err = get("empty")
	if err != nil || len(d.Lines) != 1 {
		t.Fatalf("%+v %v", d, err)
	}
	d, err = get("exact")
	if err != nil || d.Truncated {
		t.Fatalf("exact byte bound: %+v %v", d, err)
	}
	d, err = get("late-nul")
	if err != nil || d.Binary {
		t.Fatalf("late NUL: %+v %v", d, err)
	}
	d, err = get("large-binary")
	if err != nil || !d.Binary || !d.Truncated || !strings.Contains(d.Lines[0].Text, "262145 bytes") {
		t.Fatalf("%+v %v", d, err)
	}
	if err := os.Mkdir(filepath.Join(root, "dir"), 0700); err != nil {
		t.Fatal(err)
	}
	write("dir/file", "inside")
	if err := os.Symlink("dir", filepath.Join(root, "inside")); err != nil {
		t.Fatal(err)
	}
	d, err = get("inside/file")
	if err != nil || d.Lines[1].Text != "inside" {
		t.Fatalf("%+v %v", d, err)
	}
	if _, err := get("dir"); err == nil {
		t.Fatal("directory read")
	}
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret"), []byte("SECRET"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "parent")); err != nil {
		t.Fatal(err)
	}
	if _, err := get("parent/secret"); err == nil {
		t.Fatal("parent escape read")
	}
	if err := os.Symlink(filepath.Join(outside, "secret")+"\x1b", filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	d, err = get("link")
	if err != nil || len(d.Lines) != 1 || !strings.Contains(d.Lines[0].Text, "Symlink") || strings.Contains(d.Lines[0].Text, "\x1b") {
		t.Fatalf("%+v %v", d, err)
	}
	if _, err := get("../secret"); err == nil {
		t.Fatal("escape read")
	}
	if _, err := get("parent"); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(root, "fifo"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := get("fifo"); err == nil {
		t.Fatal("fifo read")
	}
	if _, err := get("."); err == nil {
		t.Fatal("directory read")
	}
	// Snapshot membership cannot make a replacement symlink authorize target reads.
	if err := os.Remove(filepath.Join(root, "text")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "secret"), filepath.Join(root, "text")); err != nil {
		t.Fatal(err)
	}
	d, err = get("text")
	if err != nil || len(d.Lines) != 1 || !strings.HasPrefix(d.Lines[0].Text, "Symlink") {
		t.Fatalf("%+v %v", d, err)
	}
}
