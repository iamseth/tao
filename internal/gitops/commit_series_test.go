package gitops

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

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
