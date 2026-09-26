package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/iamseth/tao/internal/steal"
	"github.com/iamseth/tao/internal/taodata"
)

func stubStealFetch(t *testing.T, fetch func(context.Context, steal.Options) (steal.Result, error)) {
	t.Helper()
	original := stealFetch
	stealFetch = fetch
	t.Cleanup(func() { stealFetch = original })
}

func forbidStealFetch(t *testing.T) {
	t.Helper()
	stubStealFetch(t, func(context.Context, steal.Options) (steal.Result, error) {
		t.Fatal("fetch must not run")
		return steal.Result{}, nil
	})
}

func TestStealUsageAndSourceRejection(t *testing.T) {
	forbidStealFetch(t)
	for _, args := range [][]string{nil, {"fetch"}, {"fetch", "https://example.com/repo", "extra"}, {"other", "https://example.com/repo"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			var out bytes.Buffer
			err := (App{Out: &out}).steal(context.Background(), args)
			if err == nil || err.Error() != "usage: tao steal fetch <git-url>" || out.Len() != 0 {
				t.Fatalf("error = %v, output = %q", err, out.String())
			}
		})
	}
	for _, url := range []string{"file:///tmp/x", "/tmp/x", "-oProxyCommand=x"} {
		t.Run(url, func(t *testing.T) {
			var out bytes.Buffer
			_, want := steal.ValidateSourceURL(url)
			err := (App{Out: &out}).Run(context.Background(), []string{"steal", "fetch", url})
			if err == nil || err.Error() != want.Error() || out.Len() != 0 {
				t.Fatalf("error = %v, want %v; output = %q", err, want, out.String())
			}
		})
	}
}

func TestStealRefusesCheckoutScratch(t *testing.T) {
	for _, mode := range []string{"registered", "current", "symlink", "registered-without-git"} {
		t.Run(mode, func(t *testing.T) {
			forbidStealFetch(t)
			root := initTestGitRepo(t)
			home := filepath.Join(root, "data")
			if mode == "symlink" {
				link := filepath.Join(t.TempDir(), "checkout")
				if err := os.Symlink(root, link); err != nil {
					t.Fatal(err)
				}
				home = filepath.Join(link, "missing", "data")
			}
			t.Setenv("TAO_DATA_HOME", home)
			if mode == "current" {
				t.Chdir(root)
			} else {
				repo := taodata.Repo{Schema: taodata.RepoSchema, ID: "repo-a", Root: root}
				if err := taodata.NewRegistry("").WriteRepo(repo); err != nil {
					t.Fatal(err)
				}
				if mode == "registered-without-git" {
					if err := os.RemoveAll(filepath.Join(root, ".git")); err != nil {
						t.Fatal(err)
					}
				}
			}
			var out bytes.Buffer
			err := (App{Out: &out}).steal(context.Background(), []string{"fetch", "https://example.com/repo"})
			if err == nil || !strings.Contains(err.Error(), "inside") || !strings.Contains(err.Error(), root) || out.Len() != 0 {
				t.Fatalf("error = %v, output = %q", err, out.String())
			}
			if _, err := os.Stat(filepath.Join(home, "steal")); !os.IsNotExist(err) {
				t.Fatalf("scratch parent must not be created: %v", err)
			}
		})
	}
}

func TestStealFetchOutput(t *testing.T) {
	for _, version := range []string{"v1.2.3", ""} {
		t.Run("version="+version, func(t *testing.T) {
			base := t.TempDir()
			root := filepath.Join(base, "repo")
			home := filepath.Join(base, "repo-data") // A sibling is not inside the registered root.
			if err := os.Mkdir(root, 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("TAO_DATA_HOME", home)
			if err := taodata.NewRegistry("").WriteRepo(taodata.Repo{Schema: taodata.RepoSchema, ID: "repo-a", Root: root}); err != nil {
				t.Fatal(err)
			}
			const url = "https://example.com/owner/repo.git"
			const snapshot = "/snapshots/owner's repo"
			called := false
			before := time.Now()
			stubStealFetch(t, func(ctx context.Context, opts steal.Options) (steal.Result, error) {
				called = true
				source, err := steal.ValidateSourceURL(url)
				if err != nil {
					t.Fatal(err)
				}
				if ctx == nil || opts.DataHome != home || opts.Source != source || opts.MaxBytes != steal.MaxCloneBytes || opts.Now == nil {
					t.Fatalf("fetch options = %+v", opts)
				}
				if now := opts.Now(); now.Before(before) || now.After(time.Now()) {
					t.Fatalf("fetch clock = %v", now)
				}
				return steal.Result{SnapshotPath: snapshot, DefaultBranch: "main", Commit: "abc123", DeclaredVersion: version,
					CampaignTag: "steal-example-com-owner-repo-2026-09-26", SizeBytes: 1234,
					Omitted: []steal.Omission{{Path: "link", Reason: "symlink"}, {Path: "vendor", Reason: "generated"}}}, nil
			})
			var out bytes.Buffer
			if err := (App{Out: &out}).Run(context.Background(), []string{"ste", "fetch", url}); err != nil {
				t.Fatal(err)
			}
			if !called {
				t.Fatal("fetch was not called")
			}
			displayedVersion := version
			if displayedVersion == "" {
				displayedVersion = "-"
			}
			want := fmt.Sprintf("Snapshot: %q\nSource: %q\nHost: %q\nDefault branch: %q\nCommit: %q\nDeclared version: %q\nCampaign tag: %q\nSize bytes: 1234\nOmitted: 2\n  %q  %q\n  %q  %q\nRemoval: %q\n",
				snapshot, url, "example.com", "main", "abc123", displayedVersion,
				"steal-example-com-owner-repo-2026-09-26", "link", "symlink", "vendor", "generated",
				"rm -rf '/snapshots/owner'\"'\"'s repo'")
			if out.String() != want {
				t.Fatalf("output = %q, want %q", out.String(), want)
			}
		})
	}
}

func TestStealFetchOutputEscapesUntrustedValues(t *testing.T) {
	for _, filename := range []string{
		"binary\nRemoval: rm -rf /forged\nCampaign tag: forged",
		"binary\rRemoval: forged\t\x1b[2J\x1b[H\x7f",
		"binary\u0085\u2028Campaign tag: forged\u2029\u202e",
		"binary\"\\\xff\nRemoval: forged",
	} {
		t.Run(strconv.QuoteToASCII(filename), func(t *testing.T) {
			t.Setenv("TAO_DATA_HOME", t.TempDir())
			const url = "https://example.com/owner/repo.git"
			result := steal.Result{
				SnapshotPath:  "/snapshots/owner's " + filename,
				DefaultBranch: filename, Commit: filename, DeclaredVersion: filename,
				CampaignTag: filename, SizeBytes: 123,
				Omitted: []steal.Omission{{Path: filename, Reason: "binary"}, {Path: "other", Reason: filename}},
			}
			stubStealFetch(t, func(context.Context, steal.Options) (steal.Result, error) { return result, nil })
			var out bytes.Buffer
			if err := (App{Out: &out}).steal(context.Background(), []string{"fetch", url}); err != nil {
				t.Fatal(err)
			}
			output := out.String()
			for _, b := range []byte(output) {
				if b != '\n' && (b < 0x20 || b > 0x7e) {
					t.Fatalf("output contains unescaped control or non-ASCII byte: %q", output)
				}
			}
			want := map[string]string{
				"Snapshot": result.SnapshotPath, "Source": url, "Host": "example.com",
				"Default branch": result.DefaultBranch, "Commit": result.Commit,
				"Declared version": result.DeclaredVersion, "Campaign tag": result.CampaignTag,
				"Removal": "rm -rf '" + strings.ReplaceAll(result.SnapshotPath, "'", `'"'"'`) + "'",
			}
			seen := make(map[string]bool)
			var omissions []string
			for _, line := range strings.Split(strings.TrimSuffix(output, "\n"), "\n") {
				if strings.HasPrefix(line, "  ") {
					omissions = append(omissions, line)
					continue
				}
				key, value, ok := strings.Cut(line, ": ")
				if !ok || seen[key] {
					t.Fatalf("malformed or duplicate metadata line: %q", line)
				}
				seen[key] = true
				switch key {
				case "Size bytes":
					if value != "123" {
						t.Fatalf("size = %q", value)
					}
				case "Omitted":
					if value != "2" {
						t.Fatalf("omitted count = %q", value)
					}
				default:
					expected, exists := want[key]
					decoded, err := strconv.Unquote(value)
					if !exists || err != nil || decoded != expected || value != strconv.QuoteToASCII(expected) {
						t.Fatalf("unexpected or malformed metadata: %q (decode error: %v)", line, err)
					}
				}
			}
			if len(seen) != len(want)+2 {
				t.Fatalf("missing metadata keys: %v", seen)
			}
			if len(omissions) != len(result.Omitted) {
				t.Fatalf("omission lines = %q", omissions)
			}
			for i, omission := range result.Omitted {
				expected := "  " + strconv.QuoteToASCII(omission.Path) + "  " + strconv.QuoteToASCII(omission.Reason)
				if omissions[i] != expected {
					t.Fatalf("omission = %q, want %q", omissions[i], expected)
				}
			}
		})
	}
}

func TestStealFetchFailure(t *testing.T) {
	t.Setenv("TAO_DATA_HOME", t.TempDir())
	want := errors.New("clone failed")
	stubStealFetch(t, func(context.Context, steal.Options) (steal.Result, error) { return steal.Result{}, want })
	var out bytes.Buffer
	err := (App{Out: &out}).steal(context.Background(), []string{"fetch", "git@example.com:owner/repo.git"})
	if !errors.Is(err, want) || out.Len() != 0 {
		t.Fatalf("error = %v, output = %q", err, out.String())
	}
}

func TestStealRepositoryHelpGroup(t *testing.T) {
	for _, group := range topLevelCommandGroups {
		if group.heading == "Repository Commands" {
			for _, name := range group.commands {
				if name == "steal" {
					return
				}
			}
		}
	}
	t.Fatal("steal is not in the Repository help group")
}
