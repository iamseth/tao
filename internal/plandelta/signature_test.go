package plandelta

import (
	"testing"
	"time"
)

func TestSnapshotSignature(t *testing.T) {
	s := Snapshot{Scope: ScopeBranch, Base: Base{SHA: "base"}, Head: "head", DefaultHead: "default"}
	files := []FileChange{{Path: "a\nb", Status: 'M', Added: 1}}
	want := snapshotSignature(s, files)
	metadata := s
	merged := true
	metadata.Review = ReviewParity{Recorded: true, Verdict: "approve"}
	metadata.ActiveOperation = "rebase"
	metadata.RebaseIntent = true
	metadata.Merged = &merged
	metadata.Warnings = []string{"warning"}
	metadata.CollectedAt = time.Now()
	if snapshotSignature(metadata, files) != want {
		t.Fatal("metadata changed signature")
	}
	for _, mutate := range []func(*Snapshot){
		func(s *Snapshot) { s.Scope = ScopeWorktree }, func(s *Snapshot) { s.Base.SHA = "other" }, func(s *Snapshot) { s.Head = "other" }, func(s *Snapshot) { s.DefaultHead = "other" }, func(s *Snapshot) { s.FilesTruncated = true },
	} {
		other := s
		mutate(&other)
		if snapshotSignature(other, files) == want {
			t.Fatal("identity did not change signature")
		}
	}
	for _, mutate := range []func(*FileChange){func(f *FileChange) { f.Path = "a" }, func(f *FileChange) { f.Status = 'A' }, func(f *FileChange) { f.Added++ }, func(f *FileChange) { f.Binary = true }, func(f *FileChange) { f.Uncommitted = true }, func(f *FileChange) { f.RevertsCommitted = true }} {
		other := append([]FileChange(nil), files...)
		mutate(&other[0])
		if snapshotSignature(s, other) == want {
			t.Fatal("record did not change signature")
		}
	}
	if snapshotSignature(s, files, "ab", "c") == snapshotSignature(s, files, "a", "bc") {
		t.Fatal("ambiguous record framing")
	}
	if snapshotSignature(s, []FileChange{{Path: "\xff"}}) == snapshotSignature(s, []FileChange{{Path: "\xfe"}}) {
		t.Fatal("raw bytes lost")
	}
}
