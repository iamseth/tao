package commit

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type contextGitStub struct {
	head    string
	status  string
	diffs   map[string]string
	history string
}

func (g *contextGitStub) RevParse(context.Context, string) (string, error) { return g.head, nil }
func (g *contextGitStub) StatusPorcelainAllUntracked(context.Context) (string, error) {
	return g.status, nil
}
func (g *contextGitStub) WorkingDiff(_ context.Context, paths ...string) (string, error) {
	return g.diffs[strings.Join(paths, "\x00")], nil
}
func (g *contextGitStub) RecentLog(context.Context, int) (string, error) { return g.history, nil }

func TestReadRepoExclusionsUsesOnlyKnownLocalOnlyTokens(t *testing.T) {
	root := t.TempDir()
	guidance := "Keep `.tao/` and `planning-session.json` local-only. Ordinary `internal/commit/` source remains tracked.\n"
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte(guidance), 0o600); err != nil {
		t.Fatal(err)
	}
	exclusions, err := ReadRepoExclusions(root)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Join(exclusions.Patterns, ","), ".tao/,planning-session.json"; got != want {
		t.Fatalf("patterns = %q, want %q", got, want)
	}
	if len(exclusions.Sources) != 1 || exclusions.Sources[0] != "AGENTS.md" {
		t.Fatalf("sources = %#v", exclusions.Sources)
	}
}

func TestBuildStandaloneContextFiltersBeforeExposureAndBindsCompleteDiff(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("Keep `.tao/` local-only.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git := &contextGitStub{
		head:   "head-a",
		status: " M internal/commit/context.go\n M .env.example\n M config.go\n M package-lock.json\n M coverage/report.txt\n?? .tao/state.json\n",
		diffs: map[string]string{
			"internal/commit/context.go": "diff --git a/internal/commit/context.go b/internal/commit/context.go\n+safe line\n",
			".env.example":               "diff --git a/.env.example b/.env.example\n+TOKEN=replace-me\n",
			"config.go":                  "diff --git a/config.go b/config.go\n+api_key = '1234567890abcdef1234'\n",
			"package-lock.json":          "diff --git a/package-lock.json b/package-lock.json\n+generated\n",
			"coverage/report.txt":        "diff --git a/coverage/report.txt b/coverage/report.txt\n+generated\n",
		},
		history: "abc feat(commit): add contract\n",
	}

	got, err := BuildStandaloneContext(context.Background(), git, root)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got.AllowedPaths, ",") != "internal/commit/context.go" {
		t.Fatalf("allowed paths = %#v", got.AllowedPaths)
	}
	if !strings.Contains(got.AllowedDiff, "+safe line") || strings.Contains(got.AllowedDiff, "api_key") || strings.Contains(got.AllowedDiff, "TOKEN=") {
		t.Fatalf("allowed diff was not safety filtered: %q", got.AllowedDiff)
	}
	wantReasons := map[string]string{
		".env.example":        "forbidden credential path",
		".tao/state.json":     "local-only path excluded by .tao/",
		"config.go":           "credential-looking diff content",
		"coverage/report.txt": "generated artifact",
		"package-lock.json":   "generated artifact",
	}
	for _, rejected := range got.RejectedPaths {
		if wantReasons[rejected.Path] != rejected.Reason {
			t.Fatalf("rejection = %+v, want reason %q", rejected, wantReasons[rejected.Path])
		}
		delete(wantReasons, rejected.Path)
	}
	if len(wantReasons) != 0 {
		t.Fatalf("missing rejections: %#v", wantReasons)
	}
	if got.Fingerprint == "" || got.Head != "head-a" || got.RecentHistory != git.history {
		t.Fatalf("incomplete context: %+v", got)
	}

	firstFingerprint := got.Fingerprint
	git.diffs["internal/commit/context.go"] += "+content drift\n"
	drifted, err := BuildStandaloneContext(context.Background(), git, root)
	if err != nil {
		t.Fatal(err)
	}
	if drifted.Fingerprint == firstFingerprint {
		t.Fatal("allowed diff drift did not change context fingerprint")
	}
}

func TestBuildStandaloneContextFingerprintsRawUntrackedBytes(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "invalid.txt")
	git := &contextGitStub{head: "head", status: "?? invalid.txt\n"}

	if err := os.WriteFile(path, []byte{0xff}, 0o600); err != nil {
		t.Fatal(err)
	}
	first, err := BuildStandaloneContext(context.Background(), git, root)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte{0xfe}, 0o600); err != nil {
		t.Fatal(err)
	}
	second, err := BuildStandaloneContext(context.Background(), git, root)
	if err != nil {
		t.Fatal(err)
	}

	if first.AllowedDiff != second.AllowedDiff {
		t.Fatalf("sanitized diffs differ: %q != %q", first.AllowedDiff, second.AllowedDiff)
	}
	if first.Fingerprint == second.Fingerprint {
		t.Fatal("distinct invalid UTF-8 bytes produced the same context fingerprint")
	}
}

func TestWriteIdentityLengthFramesFieldBoundaries(t *testing.T) {
	var first bytes.Buffer
	writeIdentity(&first, "first", "a")
	writeIdentity(&first, "second", "b\x00third\x00c")

	var shifted bytes.Buffer
	writeIdentity(&shifted, "first", "a\x00second\x00b")
	writeIdentity(&shifted, "third", "c")

	if bytes.Equal(first.Bytes(), shifted.Bytes()) {
		t.Fatal("different identity fields produced the same framed input")
	}
}

func TestBuildStandaloneContextBoundsDisplayedDiffButFingerprintsTail(t *testing.T) {
	root := t.TempDir()
	longDiff := "diff --git a/large.txt b/large.txt\n+" + strings.Repeat("a", contextDiffLimit+100)
	git := &contextGitStub{head: "head", status: " M large.txt\n", diffs: map[string]string{"large.txt": longDiff}}
	got, err := BuildStandaloneContext(context.Background(), git, root)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.AllowedDiff) != contextDiffLimit || !got.AllowedDiffTruncated {
		t.Fatalf("bounded diff = %d bytes, truncated=%t", len(got.AllowedDiff), got.AllowedDiffTruncated)
	}
	fingerprint := got.Fingerprint
	git.diffs["large.txt"] = longDiff[:len(longDiff)-1] + "b"
	changed, err := BuildStandaloneContext(context.Background(), git, root)
	if err != nil {
		t.Fatal(err)
	}
	if changed.Fingerprint == fingerprint {
		t.Fatal("change beyond displayed diff did not change fingerprint")
	}
}
