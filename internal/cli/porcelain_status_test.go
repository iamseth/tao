package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	taocommit "github.com/iamseth/tao/internal/commit"
	"github.com/iamseth/tao/internal/testenv"
)

func TestCommitPorcelainStatusZEndToEnd(t *testing.T) {
	testenv.SkipIfShort(t)
	for _, quotePath := range []string{"true", "false"} {
		t.Run(quotePath, func(t *testing.T) {
			root := newCLICommitRepo(t)
			runCLICommitGit(t, root, "config", "core.quotePath", quotePath)
			names := []string{"quote\".txt", "back\\slash.txt", "café.txt", "tab\tname.txt", "line\nname.txt", "old -> new.txt", " edge.txt", "edge.txt ", "edge.txt", ".tao-like -> .tao.txt"}
			// Mix tracked edits, already staged additions and plain untracked files.
			for _, name := range names[:3] {
				writeCLICommitFile(t, root, name, "before\n")
			}
			runCLICommitGit(t, root, "add", "--", names[0], names[1], names[2])
			runCLICommitGit(t, root, "commit", "-m", "chore(test): seed tracked paths")
			for _, name := range names {
				writeCLICommitFile(t, root, name, "after\n")
			}
			runCLICommitGit(t, root, "add", "--", names[3])
			writeCLICommitFile(t, root, ".tao/local.json", "{}\n")
			runCLICommitGit(t, root, "add", "-f", "--", ".tao/local.json")
			beforeIndex := runCLICommitGit(t, root, "diff", "--cached", "--name-only", "-z")
			var out bytes.Buffer
			app := newTestApp(App{Out: &out, Err: io.Discard})
			if err := app.Run(context.Background(), []string{"commit", "--context", "--repo-root", root}); err != nil {
				t.Fatal(err)
			}
			if got := runCLICommitGit(t, root, "diff", "--cached", "--name-only", "-z"); got != beforeIndex {
				t.Fatal("context mutated index")
			}
			var ctx taocommit.StandaloneContext
			if err := json.Unmarshal(out.Bytes(), &ctx); err != nil {
				t.Fatal(err)
			}
			slices.Sort(names)
			if !slices.Equal(ctx.AllowedPaths, names) {
				t.Fatalf("allowed %q want %q", ctx.AllowedPaths, names)
			}
			proposal := taocommit.StandaloneProposal{ContextFingerprint: ctx.Fingerprint, Proposal: taocommit.Proposal{Type: "fix", Scope: "paths", Summary: "preserve exact filename identity", What: "Commit unusual filenames without rewriting their bytes.", Why: "Ensure machine status parsing is independent of quotePath."}}
			data, err := json.Marshal(proposal)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), "proposal.json")
			if err := os.WriteFile(path, data, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := app.Run(context.Background(), []string{"commit", "--proposal-file", path, "--repo-root", root}); err != nil {
				t.Fatal(err)
			}
			raw := runCLICommitGit(t, root, "diff-tree", "--no-commit-id", "--name-only", "--no-renames", "-r", "-z", "HEAD")
			got := strings.Split(strings.TrimSuffix(raw, "\x00"), "\x00")
			slices.Sort(got)
			if !slices.Equal(got, names) {
				t.Fatalf("committed %q want %q", got, names)
			}
		})
	}
}
