package gitops

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestProveRebaseReplayDoesNotRequireGlobalGitIdentity(t *testing.T) {
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	root, oldBase, _, oldHead := commitSeriesRepository(t)
	runGitCommand(t, root, "checkout", "main")
	writeCommitSeriesFile(t, root, "base-advance.txt", "new base\n")
	commitAll(t, root, "advance base")
	newBase := gitOutput(t, root, "rev-parse", "HEAD")

	client := NewClient(root, nil)
	expected, err := client.CommitSeriesRebaseProof(context.Background(), oldBase, newBase, oldBase, oldHead)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.ProveRebaseReplay(context.Background(), oldBase, oldHead, newBase, expected); err != nil {
		t.Fatalf("prove replay with repository-local identity: %v", err)
	}
}

func TestCommitSeriesProofSurvivesConflictFreeRebase(t *testing.T) {
	root, base, first, second := commitSeriesRepository(t)
	client := NewClient(root, nil)
	ctx := context.Background()

	before, err := client.CommitSeriesProof(ctx, base, "feature")
	if err != nil {
		t.Fatal(err)
	}
	if before.Count != 2 || !strings.HasPrefix(before.Fingerprint, CommitSeriesFingerprintVersion) {
		t.Fatalf("proof before rebase = %+v", before)
	}

	runGitCommand(t, root, "checkout", "main")
	writeCommitSeriesFile(t, root, "base-advance.txt", "new base\n")
	commitAll(t, root, "advance base")
	newBase := gitOutput(t, root, "rev-parse", "HEAD")
	runGitCommand(t, root, "checkout", "feature")
	runGitCommand(t, root, "rebase", "main")

	after, err := client.CommitSeriesProof(ctx, newBase, "feature")
	if err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Fatalf("proof changed across ordinary rebase:\nbefore: %+v\n after: %+v", before, after)
	}
	if gitOutput(t, root, "rev-parse", "feature") == second || first == second {
		t.Fatal("fixture did not rewrite the feature commits")
	}
}

func TestCommitSeriesRebaseProofSurvivesConflictFreeUpstreamRename(t *testing.T) {
	root := t.TempDir()
	runGitCommand(t, root, "init", "-b", "main")
	runGitCommand(t, root, "config", "user.name", "Tao Test")
	runGitCommand(t, root, "config", "user.email", "tao@example.invalid")
	writeCommitSeriesFile(t, root, "original.txt", "top\ntarget\nbottom\n")
	commitAll(t, root, "base")
	oldBase := gitOutput(t, root, "rev-parse", "HEAD")

	runGitCommand(t, root, "checkout", "-b", "feature")
	writeCommitSeriesFile(t, root, "original.txt", "top\nchanged\nbottom\n")
	commitAll(t, root, "edit target")
	oldHead := gitOutput(t, root, "rev-parse", "HEAD")

	runGitCommand(t, root, "checkout", "main")
	runGitCommand(t, root, "mv", "original.txt", "renamed.txt")
	commitAll(t, root, "rename upstream file")
	newBase := gitOutput(t, root, "rev-parse", "HEAD")

	client := NewClient(root, nil)
	before, err := client.CommitSeriesRebaseProof(context.Background(), oldBase, newBase, oldBase, oldHead)
	if err != nil {
		t.Fatal(err)
	}
	runGitCommand(t, root, "checkout", "feature")
	runGitCommand(t, root, "rebase", "main")
	if _, err := os.Stat(filepath.Join(root, "renamed.txt")); err != nil {
		t.Fatalf("feature edit was not replayed under upstream destination: %v", err)
	}
	after, err := client.CommitSeriesRebaseProof(context.Background(), oldBase, newBase, newBase, "feature")
	if err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Fatalf("proof changed across upstream rename rebase:\nbefore: %+v\n after: %+v", before, after)
	}
}

func TestCommitSeriesProofSurvivesConflictFreeLineShift(t *testing.T) {
	assertCommitSeriesProofSurvivesRebase(t, rebaseProofFixture{
		filename:    "lines.txt",
		baseContent: "top\ntarget\nbottom\n",
		feature:     "top\nchanged\nbottom\n",
		newBase:     "new base line\ntop\ntarget\nbottom\n",
	})
}

func TestCommitSeriesProofSurvivesDuplicatePrependedBeforeEditedOccurrence(t *testing.T) {
	assertCommitSeriesProofSurvivesRebase(t, rebaseProofFixture{
		filename:    "duplicate.txt",
		baseContent: "same\nfirst context\nmiddle\nsame\ntarget context\n",
		feature:     "same\nfirst context\nmiddle\nchanged\ntarget context\n",
		newBase:     "same\nprepended context\nsame\nfirst context\nmiddle\nsame\ntarget context\n",
	})
}

func TestCommitSeriesProofSurvivesUniqueInsertionAmongDistantRepeats(t *testing.T) {
	assertCommitSeriesProofSurvivesRebase(t, rebaseProofFixture{
		filename:    "repeats.txt",
		baseContent: "anchor\nrepeat\nrepeat\nrepeat\nrepeat\nrepeat\ntarget\ntail\n",
		feature:     "anchor\nrepeat\nrepeat\nrepeat\nrepeat\nrepeat\nchanged\ntail\n",
		newBase:     "anchor\nrepeat\nrepeat\nupstream insertion\nrepeat\nrepeat\nrepeat\ntarget\ntail\n",
	})
}

type rebaseProofFixture struct {
	filename    string
	baseContent string
	feature     string
	newBase     string
}

func assertCommitSeriesProofSurvivesRebase(t *testing.T, fixture rebaseProofFixture) {
	t.Helper()
	root := t.TempDir()
	runGitCommand(t, root, "init", "-b", "main")
	runGitCommand(t, root, "config", "user.name", "Tao Test")
	runGitCommand(t, root, "config", "user.email", "tao@example.invalid")
	writeCommitSeriesFile(t, root, fixture.filename, fixture.baseContent)
	commitAll(t, root, "base")
	base := gitOutput(t, root, "rev-parse", "HEAD")

	runGitCommand(t, root, "checkout", "-b", "feature")
	writeCommitSeriesFile(t, root, fixture.filename, fixture.feature)
	commitAll(t, root, "change target")
	client := NewClient(root, nil)
	before, err := client.CommitSeriesProof(context.Background(), base, "feature")
	if err != nil {
		t.Fatal(err)
	}

	runGitCommand(t, root, "checkout", "main")
	writeCommitSeriesFile(t, root, fixture.filename, fixture.newBase)
	commitAll(t, root, "advance base")
	newBase := gitOutput(t, root, "rev-parse", "HEAD")
	runGitCommand(t, root, "checkout", "feature")
	runGitCommand(t, root, "rebase", "main")
	after, err := client.CommitSeriesProof(context.Background(), newBase, "feature")
	if err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Fatalf("proof changed after conflict-free rebase:\nbefore: %+v\n after: %+v", before, after)
	}
}

func TestCommitSeriesProofRetainsFileIdentity(t *testing.T) {
	root := t.TempDir()
	runGitCommand(t, root, "init", "-b", "main")
	runGitCommand(t, root, "config", "user.name", "Tao Test")
	runGitCommand(t, root, "config", "user.email", "tao@example.invalid")
	writeCommitSeriesFile(t, root, "left.txt", "top\ntarget\nbottom\n")
	writeCommitSeriesFile(t, root, "right.txt", "top\ntarget\nbottom\n")
	commitAll(t, root, "base")
	base := gitOutput(t, root, "rev-parse", "HEAD")

	for _, branch := range []string{"left", "right"} {
		runGitCommand(t, root, "checkout", "-b", branch, base)
		writeCommitSeriesFile(t, root, branch+".txt", "top\nchanged\nbottom\n")
		runGitCommand(t, root, "add", branch+".txt")
		runGitCommand(t, root, "commit", "--date=2026-08-06T00:00:00Z", "-m", "identical edit")
	}

	client := NewClient(root, nil)
	left, err := client.CommitSeriesProof(context.Background(), base, "left")
	if err != nil {
		t.Fatal(err)
	}
	right, err := client.CommitSeriesProof(context.Background(), base, "right")
	if err != nil {
		t.Fatal(err)
	}
	if left == right {
		t.Fatalf("same edit on different paths produced the same proof %+v", left)
	}
}

func TestCommitSeriesProofDistinguishesDuplicateOccurrenceLocations(t *testing.T) {
	root := t.TempDir()
	runGitCommand(t, root, "init", "-b", "main")
	runGitCommand(t, root, "config", "user.name", "Tao Test")
	runGitCommand(t, root, "config", "user.email", "tao@example.invalid")
	writeCommitSeriesFile(t, root, "duplicate.txt", "same\nbetween\nsame\n")
	commitAll(t, root, "base")
	base := gitOutput(t, root, "rev-parse", "HEAD")

	runGitCommand(t, root, "checkout", "-b", "change-first")
	writeCommitSeriesFile(t, root, "duplicate.txt", "changed\nbetween\nsame\n")
	runGitCommand(t, root, "add", "duplicate.txt")
	runGitCommand(t, root, "commit", "--date=2026-08-05T00:00:00Z", "-m", "identical message")

	runGitCommand(t, root, "checkout", "-b", "change-second", base)
	writeCommitSeriesFile(t, root, "duplicate.txt", "same\nbetween\nchanged\n")
	runGitCommand(t, root, "add", "duplicate.txt")
	runGitCommand(t, root, "commit", "--date=2026-08-05T00:00:00Z", "-m", "identical message")

	client := NewClient(root, nil)
	firstMetadata, err := client.commitRebaseStableMetadata(context.Background(), "change-first")
	if err != nil {
		t.Fatal(err)
	}
	secondMetadata, err := client.commitRebaseStableMetadata(context.Background(), "change-second")
	if err != nil {
		t.Fatal(err)
	}
	if string(firstMetadata) != string(secondMetadata) {
		t.Fatalf("fixture commit metadata differs:\nfirst: %q\nsecond: %q", firstMetadata, secondMetadata)
	}
	firstProof, err := client.CommitSeriesProof(context.Background(), base, "change-first")
	if err != nil {
		t.Fatal(err)
	}
	secondProof, err := client.CommitSeriesProof(context.Background(), base, "change-second")
	if err != nil {
		t.Fatal(err)
	}
	if firstProof == secondProof {
		t.Fatalf("different duplicate occurrences produced the same proof %+v", firstProof)
	}
}

func TestCommitSeriesProofExpandsRepeatedAdjacentContext(t *testing.T) {
	locator, err := canonicalEditLocator(
		[]string{"header", "props", "", "", "function", "tail"},
		canonicalEdit{oldPosition: 3},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(locator, "context:sha256:") {
		t.Fatalf("expanded locator = %q", locator)
	}
}

func TestCommitSeriesProofRejectsAmbiguousEditContext(t *testing.T) {
	_, err := canonicalEditLocator(
		[]string{"same", "same", "same", "same"},
		canonicalEdit{oldPosition: 1, deleted: []string{"same"}},
	)
	if err == nil || !strings.Contains(err.Error(), "ambiguous surrounding context") {
		t.Fatalf("ambiguous locator error = %v", err)
	}
}

func TestCommitSeriesProofChangesForSeriesDrift(t *testing.T) {
	root, base, first, second := commitSeriesRepository(t)
	client := NewClient(root, nil)
	ctx := context.Background()
	want, err := client.CommitSeriesProof(ctx, base, "feature")
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name  string
		build func(t *testing.T)
	}{
		{
			name: "added commit",
			build: func(t *testing.T) {
				cherryPick(t, root, first, second)
				writeCommitSeriesFile(t, root, "added.txt", "added\n")
				commitAll(t, root, "added commit")
			},
		},
		{name: "removed commit", build: func(t *testing.T) { cherryPick(t, root, first) }},
		{name: "reordered commits", build: func(t *testing.T) { cherryPick(t, root, second, first) }},
		{
			name: "message edited",
			build: func(t *testing.T) {
				cherryPick(t, root, first, second)
				runGitCommand(t, root, "commit", "--amend", "-m", "edited second message")
			},
		},
		{
			name: "content edited",
			build: func(t *testing.T) {
				cherryPick(t, root, first, second)
				writeCommitSeriesFile(t, root, "second.txt", "changed content\n")
				runGitCommand(t, root, "add", "second.txt")
				runGitCommand(t, root, "commit", "--amend", "--no-edit")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			branch := "drift-" + strings.ReplaceAll(tt.name, " ", "-")
			runGitCommand(t, root, "checkout", "-B", branch, base)
			tt.build(t)
			got, err := client.CommitSeriesProof(ctx, base, "HEAD")
			if err != nil {
				t.Fatal(err)
			}
			if got == want {
				t.Fatalf("drift produced original proof %+v", got)
			}
		})
	}
}

func TestCommitSeriesProofCoversEmptyRangeAndRejectsUnsupportedHistory(t *testing.T) {
	root, base, first, second := commitSeriesRepository(t)
	client := NewClient(root, nil)
	ctx := context.Background()

	empty, err := client.CommitSeriesProof(ctx, base, base)
	if err != nil {
		t.Fatal(err)
	}
	if empty.Count != 0 || !strings.HasPrefix(empty.Fingerprint, CommitSeriesFingerprintVersion) {
		t.Fatalf("empty proof = %+v", empty)
	}
	if _, err := client.CommitSeriesProof(ctx, "feature", "main"); err == nil || !strings.Contains(err.Error(), "not an ancestor") {
		t.Fatalf("non-ancestor error = %v", err)
	}

	// A merge in base..head is ambiguous because rebase handling of merge
	// topology depends on invocation options. Refuse rather than flatten it.
	runGitCommand(t, root, "checkout", "-B", "side", first)
	writeCommitSeriesFile(t, root, "side.txt", "side\n")
	commitAll(t, root, "side commit")
	runGitCommand(t, root, "checkout", "-B", "merged", second)
	runGitCommand(t, root, "merge", "--no-ff", "side", "-m", "merge side")
	if _, err := client.CommitSeriesProof(ctx, base, "merged"); err == nil || !strings.Contains(err.Error(), "single linear") {
		t.Fatalf("merge topology error = %v", err)
	}
}

func TestCommitSeriesTextV5Fingerprint(t *testing.T) {
	root := newBinaryProofRepository(t)
	writeCommitSeriesFile(t, root, "edit.txt", "top\nold\nbottom\n")
	writeCommitSeriesFile(t, root, "delete.txt", "old\n")
	commitAll(t, root, "base")
	base := gitOutput(t, root, "rev-parse", "HEAD")
	writeCommitSeriesFile(t, root, "edit.txt", "top\nnew\nbottom\n")
	writeCommitSeriesFile(t, root, "add.txt", "new\n")
	runGitCommand(t, root, "rm", "delete.txt")
	commitAll(t, root, "text changes")
	proof := requireSeriesProof(t, root, base, "HEAD")
	want := CommitSeriesProof{Count: 1, Fingerprint: "v5:sha256:4955ff091a317bdb948cd6a0e03b814890050e10b9f40b21b4c11047de117b9b"}
	if proof != want {
		t.Fatalf("text proof = %+v, want %+v", proof, want)
	}
}

// Fix identity, author date, and message in drift fixtures so differing evidence
// must come from the content delta rather than incidental commit metadata.
func newBinaryProofRepository(t *testing.T) string {
	t.Helper()
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_AUTHOR_NAME", "Tao Test")
	t.Setenv("GIT_AUTHOR_EMAIL", "tao@example.invalid")
	t.Setenv("GIT_AUTHOR_DATE", "2026-08-06T00:00:00Z")
	t.Setenv("GIT_COMMITTER_DATE", "2026-08-06T00:00:00Z")
	root := t.TempDir()
	runGitCommand(t, root, "init", "-b", "main")
	runGitCommand(t, root, "config", "user.name", "Tao Test")
	runGitCommand(t, root, "config", "user.email", "tao@example.invalid")
	runGitCommand(t, root, "config", "core.filemode", "true")
	return root
}

func requireSeriesProof(t *testing.T, root, base, head string) CommitSeriesProof {
	t.Helper()
	proof, err := NewClient(root, nil).CommitSeriesProof(context.Background(), base, head)
	if err != nil {
		t.Fatal(err)
	}
	return proof
}

func TestCommitSeriesBinaryReplay(t *testing.T) {
	for _, kind := range []string{"add", "modify", "delete", "mixed", "mode-only", "rename", "attribute"} {
		t.Run(kind, func(t *testing.T) {
			root := newBinaryProofRepository(t)
			writeCommitSeriesFile(t, root, "base.txt", "base\n")
			if kind != "add" {
				writeCommitSeriesFile(t, root, "file.bin", "old\x00blob\n")
			}
			if kind == "attribute" {
				writeCommitSeriesFile(t, root, ".gitattributes", "file.bin binary\n")
				writeCommitSeriesFile(t, root, "file.bin", "old plain text\n")
			}
			commitAll(t, root, "base")
			oldBase := gitOutput(t, root, "rev-parse", "HEAD")
			runGitCommand(t, root, "checkout", "-b", "feature")
			switch kind {
			case "delete":
				runGitCommand(t, root, "rm", "file.bin")
			case "mode-only":
				if err := os.Chmod(filepath.Join(root, "file.bin"), 0o755); err != nil { //nolint:gosec // executable bit is the tested Git mode change
					t.Fatal(err)
				}
			case "attribute":
				writeCommitSeriesFile(t, root, "file.bin", "new plain text\n")
			default:
				writeCommitSeriesFile(t, root, "file.bin", "new\x00blob\n")
			}
			if kind == "mixed" {
				writeCommitSeriesFile(t, root, "text.txt", "ordinary text\n")
				writeCommitSeriesFile(t, root, "added.bin", "added\x00blob\n")
			}
			commitAll(t, root, "feature change")
			oldHead := gitOutput(t, root, "rev-parse", "HEAD")
			plain := requireSeriesProof(t, root, oldBase, oldHead)
			if plain.Count != 1 {
				t.Fatalf("proof = %+v", plain)
			}
			runGitCommand(t, root, "checkout", "main")
			if kind == "rename" {
				runGitCommand(t, root, "mv", "file.bin", "renamed.bin")
			} else {
				writeCommitSeriesFile(t, root, "advance.txt", "upstream\n")
			}
			commitAll(t, root, "advance upstream")
			newBase := gitOutput(t, root, "rev-parse", "HEAD")
			runGitCommand(t, root, "checkout", "feature")
			client := NewClient(root, nil)
			ctx := context.Background()
			before, err := client.CommitSeriesRebaseProof(ctx, oldBase, newBase, oldBase, oldHead)
			if err != nil {
				t.Fatal(err)
			}
			if kind != "rename" && before != plain {
				t.Fatalf("unexpected aliasing: %+v != %+v", before, plain)
			}
			if err := client.ProveRebaseReplay(ctx, oldBase, oldHead, newBase, before); err != nil {
				t.Fatal(err)
			}
			if gitOutput(t, root, "rev-parse", "HEAD") != oldHead || gitOutput(t, root, "status", "--porcelain") != "" {
				t.Fatal("isolated replay changed source")
			}
			runGitCommand(t, root, "rebase", "main")
			after, err := client.CommitSeriesRebaseProof(ctx, oldBase, newBase, newBase, "HEAD")
			if err != nil || after != before {
				t.Fatalf("rebase proof: before %+v, after %+v, error %v", before, after, err)
			}
			if kind == "rename" {
				if _, err := os.Stat(filepath.Join(root, "renamed.bin")); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestCommitSeriesBinaryEvidenceIdentity(t *testing.T) {
	root := newBinaryProofRepository(t)
	for _, name := range []string{"left.bin", "right.bin"} {
		writeCommitSeriesFile(t, root, name, "old\x00blob\n")
	}
	commitAll(t, root, "base")
	base := gitOutput(t, root, "rev-parse", "HEAD")
	seen := make(map[CommitSeriesProof]string)
	var metadata string
	for _, kind := range []string{"original", "path", "mode", "preimage", "postimage"} {
		runGitCommand(t, root, "checkout", "-B", kind, base)
		parent := base
		if kind == "preimage" {
			writeCommitSeriesFile(t, root, "left.bin", "different\x00parent\n")
			commitAll(t, root, "different parent")
			parent = gitOutput(t, root, "rev-parse", "HEAD")
		}
		name := "left.bin"
		if kind == "path" {
			name = "right.bin"
		}
		content := "new\x00blob\n"
		if kind == "postimage" {
			content = "different\x00result\n"
		}
		writeCommitSeriesFile(t, root, name, content)
		if kind == "mode" {
			if err := os.Chmod(filepath.Join(root, name), 0o755); err != nil { //nolint:gosec // executable bit is the tested Git mode change
				t.Fatal(err)
			}
		}
		commitAll(t, root, "identical message")
		gotMetadata, err := NewClient(root, nil).commitRebaseStableMetadata(context.Background(), "HEAD")
		if err != nil {
			t.Fatal(err)
		}
		if metadata != "" && metadata != string(gotMetadata) {
			t.Fatal("fixture metadata differs")
		}
		metadata = string(gotMetadata)
		proof := requireSeriesProof(t, root, parent, "HEAD")
		if previous, ok := seen[proof]; ok {
			t.Fatalf("%s and %s have identical proof %+v", previous, kind, proof)
		}
		seen[proof] = kind
	}
}

func TestProveRebaseReplayBinaryRefusalLeavesSourceUnchanged(t *testing.T) {
	for _, kind := range []string{"conflict", "proof-mismatch"} {
		t.Run(kind, func(t *testing.T) {
			root := newBinaryProofRepository(t)
			writeCommitSeriesFile(t, root, "file.bin", "base\x00blob\n")
			commitAll(t, root, "base")
			oldBase := gitOutput(t, root, "rev-parse", "HEAD")
			runGitCommand(t, root, "checkout", "-b", "feature")
			writeCommitSeriesFile(t, root, "file.bin", "feature\x00blob\n")
			commitAll(t, root, "feature change")
			oldHead := gitOutput(t, root, "rev-parse", "HEAD")
			expected := requireSeriesProof(t, root, oldBase, oldHead)
			runGitCommand(t, root, "checkout", "main")
			if kind == "conflict" {
				writeCommitSeriesFile(t, root, "file.bin", "upstream\x00blob\n")
			} else {
				writeCommitSeriesFile(t, root, "unrelated.txt", "advance\n")
			}
			commitAll(t, root, "advance")
			newBase := gitOutput(t, root, "rev-parse", "HEAD")
			runGitCommand(t, root, "checkout", "feature")
			client := NewClient(root, nil)
			if kind == "proof-mismatch" {
				// The replay itself succeeds; even then its exact proof must match.
				if err := client.ProveRebaseReplay(context.Background(), oldBase, oldHead, newBase, expected); err != nil {
					t.Fatal(err)
				}
				runGitCommand(t, root, "checkout", "-b", "altered", oldBase)
				writeCommitSeriesFile(t, root, "file.bin", "altered\x00blob\n")
				commitAll(t, root, "feature change")
				expected = requireSeriesProof(t, root, oldBase, "HEAD")
				runGitCommand(t, root, "checkout", "feature")
			}
			writeCommitSeriesFile(t, root, "file.bin", "dirty\x00source\n")
			writeCommitSeriesFile(t, root, "untracked.txt", "keep me\n")
			status := gitOutput(t, root, "status", "--porcelain")
			err := client.ProveRebaseReplay(context.Background(), oldBase, oldHead, newBase, expected)
			want := "does not complete without intervention"
			if kind == "proof-mismatch" {
				want = "changes the recorded commit series"
			}
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("refusal = %v, want %q", err, want)
			}
			if gitOutput(t, root, "rev-parse", "HEAD") != oldHead || gitOutput(t, root, "branch", "--show-current") != "feature" || gitOutput(t, root, "status", "--porcelain") != status {
				t.Fatal("refused replay changed source branch or status")
			}
			for name, want := range map[string]string{"file.bin": "dirty\x00source\n", "untracked.txt": "keep me\n"} {
				got, err := os.ReadFile(filepath.Join(root, name)) //nolint:gosec // fixed fixture names in a test-owned temporary repository
				if err != nil || string(got) != want {
					t.Fatalf("source %s = %q, error %v", name, got, err)
				}
			}
		})
	}
}

func TestCanonicalCommitDeltaTextCompatibility(t *testing.T) {
	for _, tt := range []struct{ name, patch, want string }{
		{"edit", "diff --git a/f b/f\nindex abc1234..def5678 100644\n--- a/f\n+++ b/f\n@@ -1,3 +1,3 @@\n top\n-old\n+new\n bottom\n", "@@ tao-location context:sha256:9d8db77a8bd4bf092e2fe4c8baf4a49ecdb67375d95cda6418beba3f6bc5a9e0 @@\n-old\n+new\n\n"},
		{"add", "diff --git a/f b/f\nnew file mode 100644\nindex 0000000..def5678\n--- /dev/null\n+++ b/f\n@@ -0,0 +1 @@\n+new\n", "new file mode 100644\n@@ tao-location context:sha256:6ba485d24229aa87f87910f9b409075cd40b1cfaa435ee40862f9c1eece9784c @@\n+new\n\n"},
		{"delete", "diff --git a/f b/f\ndeleted file mode 100644\nindex abc1234..0000000\n--- a/f\n+++ /dev/null\n@@ -1 +0,0 @@\n-old\n", "deleted file mode 100644\n@@ tao-location context:sha256:6ba485d24229aa87f87910f9b409075cd40b1cfaa435ee40862f9c1eece9784c @@\n-old\n\n"},
		{"mode", "diff --git a/f b/f\nold mode 100644\nnew mode 100755\n", "old mode 100644\nnew mode 100755\n\n"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := canonicalCommitDelta(tt.patch)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tt.want {
				t.Fatalf("canonical bytes = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestCanonicalCommitDeltaBinary(t *testing.T) {
	for _, size := range []int{40, 64} {
		old, next, zero := strings.Repeat("a", size), strings.Repeat("b", size), strings.Repeat("0", size)
		for _, tt := range []struct{ name, mode, old, next string }{
			{"modify", "", old, next},
			{"add", "new file mode 100644\n", zero, next},
			{"delete", "deleted file mode 100644\n", old, zero},
			{"chmod", "old mode 100644\nnew mode 100755\n", old, next},
		} {
			t.Run(fmt.Sprintf("%s/%d", tt.name, size), func(t *testing.T) {
				var previous string
				for _, marker := range []string{"Binary files a/f and b/f differ\n", "GIT binary patch\nliteral 3\nKcmZQzWC8#H2LJ>B\n\nliteral 3\nKcmZQzWC8#H2LJ>B\n"} {
					indexMode := ""
					if tt.mode == "" {
						indexMode = " 100644"
					}
					patch := "diff --git a/f b/f\n" + tt.mode + "index " + tt.old + ".." + tt.next + indexMode + "\n" + marker
					got, err := canonicalCommitDelta(patch)
					if err != nil {
						t.Fatal(err)
					}
					want := tt.mode + "@@ tao-binary " + tt.old + ".." + tt.next + indexMode + " @@\n"
					if string(got) != want || (previous != "" && previous != string(got)) {
						t.Fatalf("canonical = %q, want %q", got, want)
					}
					previous = string(got)
				}
			})
		}
	}
}

func TestCanonicalCommitDeltaTextBinaryPhrases(t *testing.T) {
	for _, phrase := range []string{"GIT binary patch", "Binary files a/f and b/f differ"} {
		for _, patch := range []string{
			"@@ -1 +1 @@\n-" + phrase + "\n+replacement\n",
			"@@ -1 +1 @@\n-old\n+" + phrase + "\n",
			"@@ -1,2 +1,2 @@\n " + phrase + "\n-old\n+new\n",
		} {
			got, err := canonicalCommitDelta("diff --git a/f b/f\nindex abc..def 100644\n" + patch)
			if err != nil || !strings.Contains(string(got), "@@ tao-location ") {
				t.Fatalf("text phrase %q: canonical = %q, error = %v", phrase, got, err)
			}
		}
	}
}

func TestCanonicalCommitDeltaRejectsMalformedBinary(t *testing.T) {
	old, next := strings.Repeat("a", 40), strings.Repeat("b", 40)
	index := "index " + old + ".." + next + " 100644\n"
	for name, evidence := range map[string]string{
		"missing":                "",
		"abbreviated":            "index aaaaaaa..bbbbbbb 100644\n",
		"mixed lengths":          "index " + old + ".." + strings.Repeat("b", 64) + "\n",
		"non hex":                "index " + old + ".." + strings.Repeat("g", 40) + "\n",
		"separator":              "index " + old + "..." + next + "\n",
		"extra fields":           strings.TrimSpace(index) + " extra\n",
		"duplicate":              index + index,
		"conflicting":            index + "index " + next + ".." + old + "\n",
		"both zero":              "index " + strings.Repeat("0", 40) + ".." + strings.Repeat("0", 40) + "\n",
		"addition nonzero":       "new file mode 100644\n" + index,
		"deletion nonzero":       "deleted file mode 100644\n" + index,
		"invalid mode":           "old mode nope\n" + index,
		"index mode":             "index " + old + ".." + next + " 100648\n",
		"short sha256":           "index " + strings.Repeat("a", 63) + ".." + strings.Repeat("b", 63) + "\n",
		"zero without addition":  "index " + strings.Repeat("0", 40) + ".." + next + "\n",
		"zero without deletion":  "index " + old + ".." + strings.Repeat("0", 40) + "\n",
		"both file modes":        "new file mode 100644\ndeleted file mode 100644\n" + index,
		"duplicate modes":        "old mode 100644\nold mode 100644\nnew mode 100755\n" + index,
		"missing new mode":       "old mode 100644\n" + index,
		"conflicting index mode": "old mode 100644\nnew mode 100755\n" + index,
	} {
		t.Run(name, func(t *testing.T) {
			for _, marker := range []string{"Binary files a/f and b/f differ\n", "GIT binary patch\nliteral 0\nHcmV?d00001\n"} {
				if got, err := canonicalCommitDelta("diff --git a/f b/f\n" + evidence + marker); err == nil {
					t.Fatalf("accepted malformed evidence: %q", got)
				}
			}
		})
	}
}

func TestCanonicalCommitDeltaBinaryStructure(t *testing.T) {
	index := "index " + strings.Repeat("a", 40) + ".." + strings.Repeat("b", 40) + " 100644\n"
	prefix := "diff --git a/f b/f\n" + index
	for name, patch := range map[string]string{
		"duplicate marker": prefix + "Binary files a/f and b/f differ\nGIT binary patch\n",
		"bad marker":       prefix + "Binary files a/f and b/f\n",
		"trailing index":   prefix + "GIT binary patch\n" + index,
		"trailing diff":    prefix + "GIT binary patch\nliteral 0\n\ndiff --git a/g b/g\n",
		"trailing mode":    prefix + "GIT binary patch\nnew file mode 100644\n",
		"hunk before":      prefix + "@@ -1 +1 @@\n-old\n+new\nBinary files a/f and b/f differ\n",
		"hunk after":       prefix + "GIT binary patch\n@@ -1 +1 @@\n-old\n+new\n",
	} {
		t.Run(name, func(t *testing.T) {
			if got, err := canonicalCommitDelta(patch); err == nil {
				t.Fatalf("accepted malformed patch: %q", got)
			}
		})
	}
	plain, err := canonicalCommitDelta(prefix + "Binary files a/f and b/f differ\n")
	if err != nil {
		t.Fatal(err)
	}
	executable, err := canonicalCommitDelta(strings.Replace(prefix, "100644", "100755", 1) + "Binary files a/f and b/f differ\n")
	if err != nil || string(plain) == string(executable) {
		t.Fatalf("unchanged mode not bound: %q vs %q, error %v", plain, executable, err)
	}
	// Hex spelling and binary payload encoding are not blob identity.
	upper := "diff --git a/f b/f\n" + strings.ReplaceAll(strings.ReplaceAll(index, "a", "A"), "b", "B")
	for _, patch := range []string{
		upper + "Binary files a/f and b/f differ\n",
		prefix + "GIT binary patch\nliteral 3\nKcmZQzWC8#H2LJ>B\n",
		prefix + "GIT binary patch\ndelta 3\nKcmZQzWC8#H2LJ>B\n",
	} {
		got, err := canonicalCommitDelta(patch)
		if err != nil || string(got) != string(plain) {
			t.Fatalf("encoding changed proof: %q vs %q, error %v", got, plain, err)
		}
	}
}

func commitSeriesRepository(t *testing.T) (root, base, first, second string) {
	t.Helper()
	root = t.TempDir()
	runGitCommand(t, root, "init", "-b", "main")
	runGitCommand(t, root, "config", "user.name", "Tao Test")
	runGitCommand(t, root, "config", "user.email", "tao@example.invalid")
	writeCommitSeriesFile(t, root, "base.txt", "base\n")
	commitAll(t, root, "base")
	base = gitOutput(t, root, "rev-parse", "HEAD")
	runGitCommand(t, root, "checkout", "-b", "feature")
	writeCommitSeriesFile(t, root, "first.txt", "first\n")
	commitAll(t, root, "first message")
	first = gitOutput(t, root, "rev-parse", "HEAD")
	writeCommitSeriesFile(t, root, "second.txt", "second\n")
	commitAll(t, root, "second message")
	second = gitOutput(t, root, "rev-parse", "HEAD")
	return root, base, first, second
}

func cherryPick(t *testing.T, root string, commits ...string) {
	t.Helper()
	args := append([]string{"cherry-pick"}, commits...)
	runGitCommand(t, root, args...)
}

func commitAll(t *testing.T, root, message string) {
	t.Helper()
	runGitCommand(t, root, "add", ".")
	runGitCommand(t, root, "commit", "-m", message)
}

func writeCommitSeriesFile(t *testing.T, root, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func gitOutput(t *testing.T, root string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...) //nolint:gosec // test invokes fixed Git with test-controlled arguments
	cmd.Dir = root
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}
