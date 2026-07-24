package commit

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iamseth/tao/internal/gitops"
)

func TestReadStandaloneProposalValidatesBoundedStrictJSON(t *testing.T) {
	valid := StandaloneProposal{
		ContextFingerprint: strings.Repeat("a", 64),
		Proposal:           Proposal{Type: "feat", Scope: "commit", Summary: "add standalone boundary", What: "Centralize staging and commit creation.", Why: "Keep untrusted agents outside Git mutation ownership."},
	}
	encoded, err := json.Marshal(valid)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "proposal.json")
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := ReadStandaloneProposal(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != valid {
		t.Fatalf("proposal = %+v, want %+v", got, valid)
	}

	if err := os.WriteFile(path, append(encoded[:len(encoded)-1], []byte(`,"unexpected":true}`)...), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadStandaloneProposal(path); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("expected unknown-field error, got %v", err)
	}
}

func TestFinalizeStandaloneProposalRefusesDriftBeforeStaging(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*standaloneGitStub)
	}{
		{name: "head", mutate: func(git *standaloneGitStub) { git.head = "head-b" }},
		{name: "status", mutate: func(git *standaloneGitStub) { git.status = "M  source.go\n" }},
		{name: "allowed paths", mutate: func(git *standaloneGitStub) {
			git.status += " M other.go\n"
			git.diffs["other.go"] = "+other\n"
		}},
		{name: "allowed diff", mutate: func(git *standaloneGitStub) { git.diffs["source.go"] = "+after\n" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			git := &standaloneGitStub{contextGitStub: contextGitStub{
				head: "head-a", status: " M source.go\n", diffs: map[string]string{"source.go": "+before\n"},
			}}
			preflight, err := BuildStandaloneContext(context.Background(), git, root)
			if err != nil {
				t.Fatal(err)
			}
			test.mutate(git)
			proposal := StandaloneProposal{
				ContextFingerprint: preflight.Fingerprint,
				Proposal:           Proposal{Type: "fix", Scope: "commit", Summary: "prevent stale standalone commits", What: "Recheck the exact allowed diff before staging.", Why: "Avoid committing content the proposing agent did not inspect."},
			}
			_, err = FinalizeStandaloneProposal(context.Background(), git, root, proposal)
			if err == nil || !strings.Contains(err.Error(), "context is stale") {
				t.Fatalf("expected stale context error, got %v", err)
			}
			if git.addCalls != 0 || git.restoreCalls != 0 || git.commitCalls != 0 {
				t.Fatalf("stale finalization mutated Git: add=%d restore=%d commit=%d", git.addCalls, git.restoreCalls, git.commitCalls)
			}
		})
	}
}

type standaloneGitStub struct {
	contextGitStub
	addCalls     int
	restoreCalls int
	commitCalls  int
}

func (g *standaloneGitStub) Add(context.Context, ...string) error {
	g.addCalls++
	return nil
}
func (g *standaloneGitStub) RestoreStaged(context.Context, ...string) error {
	g.restoreCalls++
	return nil
}
func (g *standaloneGitStub) HasStagedChanges(context.Context) (bool, error) { return true, nil }
func (g *standaloneGitStub) Commit(context.Context, string) error {
	g.commitCalls++
	return nil
}

func TestFinalizeStandaloneProposalCommitsOnlyAllowedPathsWithRealGit(t *testing.T) {
	root := newStandaloneRepo(t)
	writeStandaloneFile(t, root, "source.go", "package source\n")
	writeStandaloneFile(t, root, ".env", "PASSWORD=do-not-commit\n")
	runStandaloneGit(t, root, "add", "-f", ".env")

	git := gitops.NewClient(root, nil)
	preflight, err := BuildStandaloneContext(context.Background(), git, root)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(preflight.AllowedPaths, ","); got != "source.go" {
		t.Fatalf("allowed paths = %q, want source.go", got)
	}
	result, err := FinalizeStandaloneProposal(context.Background(), git, root, StandaloneProposal{
		ContextFingerprint: preflight.Fingerprint,
		Proposal:           Proposal{Type: "feat", Scope: "commit", Summary: "add standalone commit boundary", What: "Commit the allowed source path through Tao's prepared transaction.", Why: "Keep excluded credential files out of model context and commit ownership."},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.SHA) != 40 || result.Subject != "feat(commit): add standalone commit boundary" {
		t.Fatalf("result = %+v", result)
	}
	if got := strings.TrimSpace(runStandaloneGit(t, root, "show", "--name-only", "--format=", "HEAD")); got != "source.go" {
		t.Fatalf("committed paths = %q", got)
	}
	if got := strings.TrimSpace(runStandaloneGit(t, root, "diff", "--cached", "--name-only")); got != "" {
		t.Fatalf("excluded staged paths remained staged: %q", got)
	}
	message := strings.TrimSpace(runStandaloneGit(t, root, "show", "-s", "--format=%B", "HEAD"))
	if err := ValidateMessage(message); err != nil {
		t.Fatalf("committed message is not centrally valid: %v\n%s", err, message)
	}
}

func TestFinalizeStandaloneMessageReportsNoAllowedChanges(t *testing.T) {
	root := newStandaloneRepo(t)
	writeStandaloneFile(t, root, ".env", "PASSWORD=do-not-commit\n")
	runStandaloneGit(t, root, "add", "-f", ".env")
	git := gitops.NewClient(root, nil)
	message := "chore(commit): check standalone boundary\n\nWhat:\nRun live safety without creating an empty commit.\n\nWhy:\nNo-op commit requests should leave repository history unchanged."
	_, err := FinalizeStandaloneMessage(context.Background(), git, root, message)
	if !errors.Is(err, ErrNoAllowedChanges) {
		t.Fatalf("expected ErrNoAllowedChanges, got %v", err)
	}
	if got := strings.TrimSpace(runStandaloneGit(t, root, "rev-list", "--count", "HEAD")); got != "1" {
		t.Fatalf("commit count = %q, want 1", got)
	}
	if got := strings.TrimSpace(runStandaloneGit(t, root, "diff", "--cached", "--name-only")); got != "" {
		t.Fatalf("rejected path remained staged: %q", got)
	}
}

func newStandaloneRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	runStandaloneGit(t, root, "init", "-b", "main")
	runStandaloneGit(t, root, "config", "user.name", "Tao Test")
	runStandaloneGit(t, root, "config", "user.email", "tao@example.invalid")
	writeStandaloneFile(t, root, "README.md", "initial\n")
	runStandaloneGit(t, root, "add", "README.md")
	runStandaloneGit(t, root, "commit", "-m", "chore(test): initialize repository")
	return root
}

func writeStandaloneFile(t *testing.T, root, name, contents string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}

func runStandaloneGit(t *testing.T, root string, args ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", root}, args...)...) //nolint:gosec // fixed test binary with test-controlled arguments
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, output)
	}
	return string(output)
}
