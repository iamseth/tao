package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	taocommit "github.com/iamseth/tao/internal/commit"
)

func TestCommitContextIsReadOnlyAndFinalizesStructuredProposal(t *testing.T) {
	root := newCLICommitRepo(t)
	writeCLICommitFile(t, root, "new/source.go", "package source\n")
	beforeIndex := runCLICommitGit(t, root, "diff", "--cached")

	var out bytes.Buffer
	app := App{Out: &out, Err: io.Discard}
	if err := app.Run(context.Background(), []string{"commit", "--context", "--repo-root", root}); err != nil {
		t.Fatal(err)
	}
	if afterIndex := runCLICommitGit(t, root, "diff", "--cached"); afterIndex != beforeIndex {
		t.Fatalf("context generation changed index\nbefore: %q\nafter:  %q", beforeIndex, afterIndex)
	}
	var commitContext taocommit.StandaloneContext
	if err := json.Unmarshal(out.Bytes(), &commitContext); err != nil {
		t.Fatalf("decode context: %v\n%s", err, out.String())
	}
	if strings.Join(commitContext.AllowedPaths, ",") != "new/source.go" || commitContext.Fingerprint == "" {
		t.Fatalf("context = %+v", commitContext)
	}

	proposal := taocommit.StandaloneProposal{
		ContextFingerprint: commitContext.Fingerprint,
		Proposal:           taocommit.Proposal{Type: "feat", Scope: "cli", Summary: "add standalone commit command", What: "Expose Tao's safe commit finalization through the CLI.", Why: "Let active agents propose messages while Tao owns Git mutation."},
	}
	proposalBytes, err := json.Marshal(proposal)
	if err != nil {
		t.Fatal(err)
	}
	proposalPath := filepath.Join(t.TempDir(), "proposal.json")
	if err := os.WriteFile(proposalPath, proposalBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := app.Run(context.Background(), []string{"commit", "--proposal-file", proposalPath, "--repo-root", root}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Created local commit ") || !strings.Contains(out.String(), "feat(cli): add standalone commit command") {
		t.Fatalf("output = %q", out.String())
	}
	if got := strings.TrimSpace(runCLICommitGit(t, root, "show", "--name-only", "--format=", "HEAD")); got != "new/source.go" {
		t.Fatalf("committed paths = %q", got)
	}
}

func TestCommitMessageOverrideUsesCentralValidationAndReportsSafeNoOp(t *testing.T) {
	root := newCLICommitRepo(t)
	writeCLICommitFile(t, root, ".env.example", "TOKEN=replace-me\n")
	message := "chore(cli): check standalone commit safety\n\nWhat:\nApply live safety filtering to explicit commit messages.\n\nWhy:\nCompatibility overrides must not bypass Tao's path authority."
	var out bytes.Buffer
	app := App{Out: &out, Err: io.Discard}
	if err := app.Run(context.Background(), []string{"commit", "--message", message, "--repo-root", root}); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); got != "Nothing to commit: no allowed changes.\n" {
		t.Fatalf("output = %q", got)
	}
	if got := strings.TrimSpace(runCLICommitGit(t, root, "rev-list", "--count", "HEAD")); got != "1" {
		t.Fatalf("commit count = %q, want 1", got)
	}
}

func TestCommitRejectsInvalidProposalBeforeGitCommands(t *testing.T) {
	path := filepath.Join(t.TempDir(), "proposal.json")
	if err := os.WriteFile(path, []byte(`{"context_fingerprint":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","type":"wip","scope":"cli","summary":"add invalid proposal","what":"What.","why":"Why."}`), 0o600); err != nil {
		t.Fatal(err)
	}
	calls := 0
	app := App{
		Out: io.Discard,
		Err: io.Discard,
		CommandRunner: func(context.Context, string, string, []string, io.Writer, io.Writer) error {
			calls++
			return nil
		},
	}
	err := app.Run(context.Background(), []string{"commit", "--proposal-file", path})
	if err == nil || !strings.Contains(err.Error(), "unsupported commit type") {
		t.Fatalf("expected proposal validation error, got %v", err)
	}
	if calls != 0 {
		t.Fatalf("invalid proposal invoked %d Git commands", calls)
	}
}

func newCLICommitRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	runCLICommitGit(t, root, "init", "-b", "main")
	runCLICommitGit(t, root, "config", "user.name", "Tao Test")
	runCLICommitGit(t, root, "config", "user.email", "tao@example.invalid")
	writeCLICommitFile(t, root, "README.md", "initial\n")
	runCLICommitGit(t, root, "add", "README.md")
	runCLICommitGit(t, root, "commit", "-m", "chore(test): initialize repository")
	return root
}

func writeCLICommitFile(t *testing.T, root, name, contents string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}

func runCLICommitGit(t *testing.T, root string, args ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", root}, args...)...) //nolint:gosec // fixed test binary with test-controlled arguments
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, output)
	}
	return string(output)
}
