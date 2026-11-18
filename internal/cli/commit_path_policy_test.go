package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	taocommit "github.com/iamseth/tao/internal/commit"
	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/internal/run"
	"github.com/iamseth/tao/internal/runtimeconfig"
	"github.com/iamseth/tao/internal/settings"
	"github.com/iamseth/tao/internal/taodata"
	"github.com/iamseth/tao/internal/testenv"
)

func TestCommitSecretPathPolicyIntegration(t *testing.T) {
	testenv.SkipIfShort(t)
	for _, mode := range []string{"proposal", "message"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			home := t.TempDir()
			t.Setenv("TAO_DATA_HOME", home)
			root, err := filepath.EvalSymlinks(newCLICommitRepo(t))
			if err != nil {
				t.Fatal(err)
			}
			registry := taodata.NewRegistry(home)
			repo := taodata.Repo{Schema: taodata.RepoSchema, ID: taodata.RepoID(root), Name: "target", Root: root}
			if err := registry.WriteRepo(repo); err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			app := newTestApp(App{Out: &out, Err: io.Discard, SettingsService: settings.NewService(home, runtimeconfig.LoadEnv(nil))})
			configure := func(value string) {
				t.Helper()
				if err := app.config(ctx, []string{"set", "commit_secret_path_allow_prefixes", value, "--repo", repo.ID}); err != nil {
					t.Fatal(err)
				}
			}
			contextFor := func(root string) taocommit.StandaloneContext {
				t.Helper()
				out.Reset()
				if err := app.commit(ctx, []string{"--context", "--repo-root", root}); err != nil {
					t.Fatal(err)
				}
				var result taocommit.StandaloneContext
				if err := json.Unmarshal(out.Bytes(), &result); err != nil {
					t.Fatal(err)
				}
				return result
			}
			writeCLICommitFile(t, root, "packages/secrets/source.go", "package secrets\n")
			configure(`["packages/secrets"]`)
			snapshot := contextFor(root)
			if strings.Join(snapshot.AllowedPaths, ",") != "packages/secrets/source.go" {
				t.Fatalf("context = %+v", snapshot)
			}
			other := newCLICommitRepo(t)
			writeCLICommitFile(t, other, "packages/secrets/source.go", "package secrets\n")
			if got := contextFor(other); len(got.AllowedPaths) != 0 {
				t.Fatalf("policy leaked to unregistered repository: %+v", got)
			}
			otherRoot, err := filepath.EvalSymlinks(other)
			if err != nil {
				t.Fatal(err)
			}
			if err := registry.WriteRepo(taodata.Repo{Schema: taodata.RepoSchema, ID: taodata.RepoID(otherRoot), Name: "other", Root: otherRoot}); err != nil {
				t.Fatal(err)
			}
			if got := contextFor(other); len(got.AllowedPaths) != 0 {
				t.Fatalf("policy leaked to registered repository: %+v", got)
			}
			proposal := taocommit.StandaloneProposal{ContextFingerprint: snapshot.Fingerprint, Proposal: taocommit.Proposal{Type: "feat", Scope: "test", Summary: "add source", What: "Add legitimate source.", Why: "Exercise repository policy."}}
			data, err := json.Marshal(proposal)
			if err != nil {
				t.Fatal(err)
			}
			proposalPath := filepath.Join(t.TempDir(), "proposal.json")
			if err := os.WriteFile(proposalPath, data, 0o600); err != nil {
				t.Fatal(err)
			}
			before := runCLICommitGit(t, root, "rev-parse", "HEAD")
			// A changed policy changes the live allowed-path fingerprint, not just display.
			for _, value := range []string{`[]`, `["elsewhere"]`} {
				configure(value)
				if err := app.commit(ctx, []string{"--proposal-file", proposalPath, "--repo-root", root}); err == nil {
					t.Fatal("stale proposal accepted")
				}
				if got := runCLICommitGit(t, root, "rev-parse", "HEAD"); got != before {
					t.Fatal("stale proposal moved HEAD")
				}
				if got := runCLICommitGit(t, root, "diff", "--cached"); got != "" {
					t.Fatal("stale proposal staged changes")
				}
			}
			configure(`["packages/secrets"]`)
			args := []string{"--proposal-file", proposalPath, "--repo-root", root}
			if mode == "message" {
				args = []string{"--message", "feat(test): add source\n\nWhat:\nAdd legitimate source.\n\nWhy:\nExercise repository policy.", "--repo-root", root}
			}
			if err := app.commit(ctx, args); err != nil {
				t.Fatal(err)
			}
			if got := strings.TrimSpace(runCLICommitGit(t, root, "show", "--name-only", "--format=", "HEAD")); got != "packages/secrets/source.go" {
				t.Fatalf("committed %q", got)
			}
			// Corrupt saved settings must refuse both context and mutation, not fall back.
			writeCLICommitFile(t, root, "ordinary.go", "package ordinary\n")
			metadata := filepath.Join(home, "repos", repo.ID, "repo.json")
			for _, raw := range []string{`{"schema":"tao.repo.v1","id":"` + repo.ID + `","root":` + string(mustPolicyJSON(t, root)) + `,"run_defaults":{"commit_secret_path_allow_prefixes":["../bad"]}}`, `{`} {
				if err := os.WriteFile(metadata, []byte(raw), 0o600); err != nil {
					t.Fatal(err)
				}
				before = runCLICommitGit(t, root, "rev-parse", "HEAD")
				for _, badArgs := range [][]string{{"--context", "--repo-root", root}, args} {
					if err := app.commit(ctx, badArgs); err == nil {
						t.Fatal("invalid settings accepted")
					}
				}
				if got := runCLICommitGit(t, root, "rev-parse", "HEAD"); got != before {
					t.Fatal("invalid settings moved HEAD")
				}
				if got := runCLICommitGit(t, root, "diff", "--cached"); got != "" {
					t.Fatal("invalid settings staged changes")
				}
			}
		})
	}
}

func TestSliceCompletionSecretPathPolicyIntegration(t *testing.T) {
	testenv.SkipIfShort(t)
	ctx := context.Background()
	record, args := newVerifiedCLICompletion(t, "slice", "true")
	home := t.TempDir()
	t.Setenv("TAO_DATA_HOME", home)
	root, err := filepath.EvalSymlinks(record.Detail().State.Repo.Root)
	if err != nil {
		t.Fatal(err)
	}
	registry := taodata.NewRegistry(home)
	selected := taodata.Repo{Schema: taodata.RepoSchema, ID: taodata.RepoID(root), Root: root, Name: "selected"}
	if err := registry.WriteRepo(selected); err != nil {
		t.Fatal(err)
	}
	other := taodata.Repo{Schema: taodata.RepoSchema, ID: "other", Root: t.TempDir(), Name: "other"}
	if err := registry.WriteRepo(other); err != nil {
		t.Fatal(err)
	}
	app := newTestApp(App{Out: io.Discard, Err: io.Discard, SettingsService: settings.NewService(home, runtimeconfig.LoadEnv(nil)), Registry: func() NoteRegistry {
		return &fakeNoteRegistry{current: other, repos: []taodata.Repo{other, selected}}
	}})
	if err := app.config(ctx, []string{"set", "commit_secret_path_allow_prefixes", `["packages/secrets/"]`, "--repo", selected.ID}); err != nil {
		t.Fatal(err)
	}
	execution := record.Detail().Slices.Slices[0].ExecutionRoot
	// Expand untracked paths so this integration test exercises the file heuristic.
	runCLICommitGit(t, execution, "config", "status.showUntrackedFiles", "all")
	writeCLICommitFile(t, execution, "packages/secrets/source.go", "package source\n")
	old := executeSinglePlan
	t.Cleanup(func() { executeSinglePlan = old })
	started := false
	executeSinglePlan = func(_ run.Service, _ context.Context, _ run.Request) error { started = true; return nil }
	resolver := plan.NewFileRepository("")
	if err := app.validate(ctx, resolver, []string{record.Dir()}); err != nil {
		t.Fatal(err)
	}
	if err := app.run(ctx, resolver, []string{"--no-review", record.Dir()}); err != nil {
		t.Fatal(err)
	}
	if !started {
		t.Fatal("run was not admitted")
	}
	metadata := filepath.Join(home, "repos", selected.ID, "repo.json")
	// Malformed saved fields and documents must refuse all three entry points.
	for _, raw := range []string{`{"schema":"tao.repo.v1","id":"` + selected.ID + `","root":` + string(mustPolicyJSON(t, root)) + `,"run_defaults":{"commit_secret_path_allow_prefixes":["../bad"]}}`, `{`} {
		if err := os.WriteFile(metadata, []byte(raw), 0o600); err != nil {
			t.Fatal(err)
		}
		started = false
		if err := app.validate(ctx, resolver, []string{record.Dir()}); err == nil {
			t.Fatal("validate accepted malformed settings")
		}
		if err := app.run(ctx, resolver, []string{"--no-review", record.Dir()}); err == nil {
			t.Fatal("run accepted malformed settings")
		}
		if err := app.Run(ctx, args); err == nil {
			t.Fatal("completion accepted malformed settings")
		}
		if started {
			t.Fatal("invalid settings started run")
		}
		if got := runCLICommitGit(t, execution, "diff", "--cached"); got != "" {
			t.Fatal("invalid settings staged changes")
		}
		if got := reloadCLICompletion(t, record).Slices.Slices[0]; got.CommitIntent != nil || got.VerificationAttempt != nil {
			t.Fatal("invalid settings persisted execution evidence")
		}
	}
	if err := registry.WriteRepo(selected); err != nil {
		t.Fatal(err)
	}
	if err := app.config(ctx, []string{"set", "commit_secret_path_allow_prefixes", `["packages/secrets/"]`, "--repo", selected.ID}); err != nil {
		t.Fatal(err)
	}
	if err := app.Run(ctx, args); err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(runCLICommitGit(t, execution, "show", "--name-only", "--format=", "HEAD")); got != "packages/secrets/source.go" {
		t.Fatalf("committed %q", got)
	}
}

func mustPolicyJSON(t *testing.T, value string) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
