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

const standalonePushMessage = "feat(commit): publish standalone changes\n\nWhat:\nPublish the newly created commit to its upstream.\n\nWhy:\nKeep publication explicit and exact."

func newStandalonePushRepo(t *testing.T) (string, string) {
	t.Helper()
	root := newStandaloneRepo(t)
	remote := t.TempDir()
	runStandaloneGit(t, remote, "init", "--bare")
	runStandaloneGit(t, root, "remote", "add", "publish", remote)
	runStandaloneGit(t, root, "push", "publish", "HEAD:refs/heads/destination")
	runStandaloneGit(t, root, "config", "branch.main.remote", "publish")
	runStandaloneGit(t, root, "config", "branch.main.merge", "refs/heads/destination")
	return root, remote
}

func TestStandalonePushPublishesOnlyNewCommit(t *testing.T) {
	for _, proposalMode := range []bool{false, true} {
		t.Run(map[bool]string{false: "message", true: "proposal"}[proposalMode], func(t *testing.T) {
			root, remote := newStandalonePushRepo(t)
			// These defaults must not broaden or redirect the explicit publication.
			runStandaloneGit(t, root, "config", "remote.publish.mirror", "true")
			runStandaloneGit(t, root, "config", "push.followTags", "true")
			runStandaloneGit(t, root, "config", "push.default", "matching")
			runStandaloneGit(t, root, "config", "remote.publish.push", "+refs/heads/*:refs/heads/*")
			runStandaloneGit(t, root, "config", "branch.main.pushRemote", "nonexistent")
			runStandaloneGit(t, root, "branch", "unintended")
			runStandaloneGit(t, root, "tag", "-a", "unintended-tag", "-m", "not for publication")
			writeStandaloneFile(t, root, "source.go", "package source\n")
			git := gitops.NewClient(root, nil)
			var result StandaloneResult
			var err error
			if proposalMode {
				preflight, contextErr := BuildStandaloneContext(context.Background(), git, root)
				if contextErr != nil {
					t.Fatal(contextErr)
				}
				result, err = FinalizeStandaloneProposalWithOptions(context.Background(), git, root, StandaloneProposal{
					ContextFingerprint: preflight.Fingerprint,
					Proposal:           Proposal{Type: "feat", Scope: "commit", Summary: "publish standalone changes", What: "Publish the new commit.", Why: "Keep publication explicit."},
				}, StandaloneOptions{Push: true})
			} else {
				result, err = FinalizeStandaloneMessageWithOptions(context.Background(), git, root, standalonePushMessage, StandaloneOptions{Push: true})
			}
			if err != nil || result.Pushed == nil {
				t.Fatalf("result = %+v, error = %v", result, err)
			}
			if got := strings.TrimSpace(runStandaloneGit(t, remote, "show-ref")); got != result.SHA+" refs/heads/destination" {
				t.Fatalf("remote refs = %q; expected only exact commit %s", got, result.SHA)
			}
			if result.Pushed.Destination.String() != "publish:refs/heads/destination" {
				t.Fatalf("destination = %+v", result.Pushed)
			}
		})
	}
}

func TestStandalonePushPreflightRefusesBeforeMutation(t *testing.T) {
	for _, scenario := range []string{"missing upstream", "detached", "multiple endpoints"} {
		t.Run(scenario, func(t *testing.T) {
			root, remote := newStandalonePushRepo(t)
			switch scenario {
			case "missing upstream":
				runStandaloneGit(t, root, "config", "--unset", "branch.main.merge")
			case "detached":
				runStandaloneGit(t, root, "checkout", "--detach")
			case "multiple endpoints":
				runStandaloneGit(t, root, "config", "--add", "remote.publish.pushurl", remote)
				runStandaloneGit(t, root, "config", "--add", "remote.publish.pushurl", remote+"-other")
			}
			writeStandaloneFile(t, root, "source.go", "package source\n")
			writeStandaloneFile(t, root, ".env", "PASSWORD=private\n")
			runStandaloneGit(t, root, "add", ".env")
			before := runStandaloneGit(t, root, "status", "--porcelain")
			head := runStandaloneGit(t, root, "rev-parse", "HEAD")
			_, err := FinalizeStandaloneMessageWithOptions(context.Background(), gitops.NewClient(root, nil), root, standalonePushMessage, StandaloneOptions{Push: true})
			if err == nil {
				t.Fatal("expected preflight refusal")
			}
			if got := runStandaloneGit(t, root, "status", "--porcelain"); got != before {
				t.Fatalf("index/worktree changed: %q -> %q", before, got)
			}
			if got := runStandaloneGit(t, root, "rev-parse", "HEAD"); got != head {
				t.Fatal("HEAD changed on refused push")
			}
		})
	}
}

func TestStandalonePushRejectionPreservesLocalCommit(t *testing.T) {
	root, remote := newStandalonePushRepo(t)
	// Advance the remote independently so the new local commit is not a fast-forward.
	runStandaloneGit(t, root, "commit", "--allow-empty", "-m", "remote-only change")
	remoteHead := strings.TrimSpace(runStandaloneGit(t, root, "rev-parse", "HEAD"))
	runStandaloneGit(t, root, "push", "publish", "HEAD:refs/heads/destination")
	runStandaloneGit(t, root, "reset", "--hard", "HEAD^")
	writeStandaloneFile(t, root, "source.go", "package source\n")
	result, err := FinalizeStandaloneMessageWithOptions(context.Background(), gitops.NewClient(root, nil), root, standalonePushMessage, StandaloneOptions{Push: true})
	var partial *StandalonePushError
	if !errors.As(err, &partial) || result.SHA == "" || result.Pushed != nil {
		t.Fatalf("result = %+v, error = %v", result, err)
	}
	for _, text := range []string{result.SHA, "local commit", "remains", "publish:refs/heads/destination", "manually push", "without force"} {
		if !strings.Contains(err.Error(), text) {
			t.Fatalf("error missing %q: %v", text, err)
		}
	}
	if got := strings.TrimSpace(runStandaloneGit(t, root, "rev-parse", "HEAD")); got != result.SHA {
		t.Fatalf("local HEAD = %s, want %s", got, result.SHA)
	}
	if got := strings.TrimSpace(runStandaloneGit(t, remote, "show-ref")); got != remoteHead+" refs/heads/destination" {
		t.Fatalf("remote mutated on rejection: %s", got)
	}
}

type standalonePublisherStub struct {
	standaloneGitStub
	branch      string
	destination gitops.TrackingDestination
	afterCommit func(*standalonePublisherStub)
	headReads   int
	pushes      int
}

func (g *standalonePublisherStub) CurrentBranch(context.Context) (string, error) {
	return g.branch, nil
}
func (g *standalonePublisherStub) TrackingDestination(context.Context, string) (gitops.TrackingDestination, error) {
	return g.destination, nil
}
func (g *standalonePublisherStub) PushExact(context.Context, gitops.TrackingDestination, string) error {
	g.pushes++
	return nil
}
func (g *standalonePublisherStub) Commit(context.Context, string) error {
	g.commitCalls++
	g.head = strings.Repeat("b", 40)
	return nil
}
func (g *standalonePublisherStub) RevParse(context.Context, string) (string, error) {
	head := g.head
	if g.commitCalls > 0 {
		g.headReads++
		if g.headReads == 1 && g.afterCommit != nil {
			g.afterCommit(g)
		}
	}
	return head, nil
}

func TestStandalonePushRefusesPostCommitDrift(t *testing.T) {
	for _, scenario := range []string{"branch", "head", "upstream", "endpoint"} {
		t.Run(scenario, func(t *testing.T) {
			git := &standalonePublisherStub{
				standaloneGitStub: standaloneGitStub{contextGitStub: contextGitStub{head: strings.Repeat("a", 40), status: " M source.go\n", diffs: map[string]string{"source.go": "+change\n"}}},
				branch:            "main", destination: gitops.TrackingDestination{Remote: "origin", Ref: "refs/heads/main", PushURL: "original"},
				afterCommit: func(g *standalonePublisherStub) {
					switch scenario {
					case "branch":
						g.branch = "other"
					case "head":
						g.head = strings.Repeat("c", 40)
					case "upstream":
						g.destination.Ref = "refs/heads/other"
					case "endpoint":
						g.destination.PushURL = "other"
					}
				},
			}
			result, err := FinalizeStandaloneMessageWithOptions(context.Background(), git, t.TempDir(), standalonePushMessage, StandaloneOptions{Push: true})
			var partial *StandalonePushError
			if !errors.As(err, &partial) || result.SHA != strings.Repeat("b", 40) || git.pushes != 0 || git.commitCalls != 1 {
				t.Fatalf("result=%+v, error=%v, commits=%d pushes=%d", result, err, git.commitCalls, git.pushes)
			}
		})
	}
}

func TestStandaloneNoOpNeverPushes(t *testing.T) {
	git := &standalonePublisherStub{
		standaloneGitStub: standaloneGitStub{contextGitStub: contextGitStub{head: strings.Repeat("a", 40)}},
		branch:            "main", destination: gitops.TrackingDestination{Remote: "origin", Ref: "refs/heads/main", PushURL: "remote"},
	}
	_, err := FinalizeStandaloneMessageWithOptions(context.Background(), git, t.TempDir(), standalonePushMessage, StandaloneOptions{Push: true})
	if !errors.Is(err, ErrNoAllowedChanges) || git.pushes != 0 || git.commitCalls != 0 {
		t.Fatalf("error=%v, commits=%d pushes=%d", err, git.commitCalls, git.pushes)
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
