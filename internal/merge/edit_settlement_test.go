package merge

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/internal/testenv"
)

func TestMergeEditFingerprintCompatibility(t *testing.T) {
	root := t.TempDir()
	for name, content := range map[string]string{"exec": "run\n", "plain": "hello\n"} {
		path := filepath.Join(root, name)
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if name == "exec" {
			if err := os.Chmod(path, 0o700); err != nil { //nolint:gosec // executable bit is part of the persisted fingerprint fixture.
				t.Fatal(err)
			}
		}
	}
	if err := os.Symlink("plain", filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	got, err := aggregateReworkContentFingerprint(root, []string{"plain", "missing", "link", "exec", "plain"})
	if err != nil {
		t.Fatal(err)
	}
	const want = "6364eef673a82dffd0faebcfeb78ee191ef842c684f9328c3943ed35284c756f"
	if got != want {
		t.Fatalf("persisted v2 digest = %s, want %s", got, want)
	}
}

func TestPrepareMergeEditsDoesNotStage(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "file"), []byte("fixed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git := &fakeGitClient{root: root, status: " M file\x00"}
	edits, err := prepareMergeEdits(context.Background(), git)
	if err != nil {
		t.Fatal(err)
	}
	if edits.matchesPaths([]string{"other"}) {
		t.Fatal("accepted different paths")
	}
	for _, fingerprint := range []string{"", "different"} {
		if matches, err := edits.matchesContent(root, fingerprint); err != nil || matches {
			t.Fatalf("content comparison = %t, %v", matches, err)
		}
	}
	for _, call := range git.calls {
		if strings.HasPrefix(call, "add") {
			t.Fatalf("preparation staged: %s", call)
		}
	}
}

func editSettlementTestMessage(t *testing.T, id string) string {
	t.Helper()
	message, err := aggregateProposedResolutionCommitMessage(plan.ReviewCommitMessage{Subject: "fix(batch): resolve findings", Body: "What:\nResolve findings.\n\nWhy:\nKeep integration correct."}, id, 1)
	if err != nil {
		t.Fatal(err)
	}
	return message
}

func TestAggregateFingerprintStagesExactBuckets(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"tracked", "new"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("fixed\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	paths := []string{"deleted", "new", "tracked"}
	fingerprint, err := aggregateReworkContentFingerprint(root, paths)
	if err != nil {
		t.Fatal(err)
	}
	message := editSettlementTestMessage(t, "batch-1")
	state := BatchState{ID: "batch-1", IntegrationHead: "parent", Attempts: BatchAttempts{AggregateRework: 1}, Review: &BatchReview{Status: "applying", CommitMessage: message, ResolutionPaths: paths, ResolutionFingerprint: fingerprint}}
	git := &fakeGitClient{root: root, status: "D  deleted\x00 M tracked\x00?? new\x00", stagedChanges: true, revParse: map[string]string{"HEAD": "committed"}}
	_, err = (BatchAggregateReviewer{Store: &batchReviewTestStore{dir: t.TempDir()}}).finishAggregateRework(context.Background(), git, state, root)
	if err != nil {
		t.Fatal(err)
	}
	var staging []string
	for _, call := range git.calls {
		if strings.HasPrefix(call, "add") {
			staging = append(staging, call)
		}
	}
	if !slices.Equal(staging, []string{"add-tracked tracked", "add new"}) {
		t.Fatalf("staging = %q", staging)
	}
}

func TestAggregateRealGitEditCompatibility(t *testing.T) {
	testenv.SkipIfShort(t)
	for _, scenario := range []string{"staged-deletion", "newly-ignored"} {
		t.Run(scenario, func(t *testing.T) {
			fixture, state, root := batchReviewFixture(t)
			if err := os.MkdirAll(filepath.Join(root, "cache"), 0o750); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, "cache/data"), []byte("old\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			runRealGit(t, root, "add", "cache/data")
			runRealGit(t, root, "commit", "-m", "seed")
			state.IntegrationHead = strings.TrimSpace(realGitOutput(t, root, "rev-parse", "HEAD"))
			if scenario == "staged-deletion" {
				runRealGit(t, root, "rm", "cache/data")
			} else {
				for name, content := range map[string]string{".gitignore": "cache/\n", "cache/data": "fixed\n", "cache/sibling": "ignored\n"} {
					if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o600); err != nil {
						t.Fatal(err)
					}
				}
			}
			client, err := NewService(fixture.repoRoot, nil).gitClientForRoot(root)
			if err != nil {
				t.Fatal(err)
			}
			changes, err := concretePorcelainChanges(context.Background(), client)
			if err != nil {
				t.Fatal(err)
			}
			fingerprint, err := aggregateReworkContentFingerprint(root, changes.changedPaths)
			if err != nil {
				t.Fatal(err)
			}
			message := editSettlementTestMessage(t, state.ID)
			state.Attempts.AggregateRework = 1
			state.Review = &BatchReview{Status: "applying", CommitMessage: message, ResolutionPaths: changes.changedPaths, ResolutionFingerprint: fingerprint}
			// Write-tree supplies the expected complete tree independently of settlement.
			if scenario == "newly-ignored" {
				runRealGit(t, root, "add", "-f", "cache/data")
				runRealGit(t, root, "add", ".gitignore")
			}
			wantTree := strings.TrimSpace(realGitOutput(t, root, "write-tree"))
			if scenario == "newly-ignored" {
				runRealGit(t, root, "reset", "HEAD", "--", "cache/data", ".gitignore")
			}
			got, err := (BatchAggregateReviewer{Store: &batchReviewTestStore{dir: t.TempDir()}}).finishAggregateRework(context.Background(), client, state, root)
			if err != nil {
				t.Fatal(err)
			}
			if tree := strings.TrimSpace(realGitOutput(t, root, "rev-parse", "HEAD^{tree}")); tree != wantTree {
				t.Fatalf("tree %s != %s", tree, wantTree)
			}
			if got.Review.CommitMessage != message || !slices.Contains(got.Review.ResolutionSHAs, got.IntegrationHead) {
				t.Fatalf("lost settlement evidence: %+v", got.Review)
			}
			if state.Review.ResolutionFingerprint != fingerprint || !slices.Equal(state.Review.ResolutionPaths, changes.changedPaths) {
				t.Fatal("mutated original intent evidence")
			}
		})
	}
}
