package commit

import (
	"slices"
	"strings"
	"testing"
)

func TestMetadataPathCompatibility(t *testing.T) {
	for _, path := range []string{".tao", "./.tao/state.json", "sub/../.tao/state.json"} {
		if !IsTaoMetadataPath(path) {
			t.Errorf("metadata rejected: %q", path)
		}
	}
	for _, path := range []string{".git/config", ".tao/../source.go", "sub/.tao", ".tao-other"} {
		if IsTaoMetadataPath(path) {
			t.Errorf("non-metadata rejected: %q", path)
		}
	}
}

func TestClassifyStatusPreservesAutomaticSliceSafety(t *testing.T) {
	status := strings.Join([]string{
		" M internal/run/run.go",
		"M  .tao/state.json",
		"?? .tao/local.json",
		"R  .tao/old.json -> .tao/new.json",
		"R  old.go -> new.go",
		"?? .env.example",
	}, "\n")
	classification := ClassifyStatus(status, StartingDirtyPredicate([]string{"./internal/run/run.go"}))

	if want := []string{"internal/run/run.go", ".env.example"}; !slices.Equal(classification.CommitCandidates, want) {
		t.Fatalf("CommitCandidates = %q, want %q", classification.CommitCandidates, want)
	}
	if want := []string{".tao/state.json", ".tao/old.json", ".tao/new.json"}; !slices.Equal(classification.TaoStagedPaths, want) {
		t.Fatalf("TaoStagedPaths = %q, want %q", classification.TaoStagedPaths, want)
	}
	if want := []string{"R  old.go -> new.go"}; !slices.Equal(classification.AmbiguousLines, want) {
		t.Fatalf("AmbiguousLines = %q, want %q", classification.AmbiguousLines, want)
	}
	if want := []string{"internal/run/run.go"}; !slices.Equal(classification.StartingDirtyPaths, want) {
		t.Fatalf("StartingDirtyPaths = %q, want %q", classification.StartingDirtyPaths, want)
	}
}

func TestSafetyPathClassification(t *testing.T) {
	tests := []struct {
		path      string
		secret    bool
		generated bool
	}{
		{path: ".env", secret: true},
		{path: ".env.local", secret: true},
		{path: ".env.example"},
		{path: "credentials/.env.example", secret: true},
		{path: "docs/uncredentialed.md", secret: true},
		{path: "coverage.out", generated: true},
		{path: "bin/tao", generated: true},
		{path: "cmd/bin/tao"},
	}
	for _, test := range tests {
		t.Run(test.path, func(t *testing.T) {
			if got := SuspectedSecretPath(test.path); got != test.secret {
				t.Errorf("SuspectedSecretPath() = %t, want %t", got, test.secret)
			}
			if got := GeneratedPath(test.path); got != test.generated {
				t.Errorf("GeneratedPath() = %t, want %t", got, test.generated)
			}
		})
	}
}

func TestSafetyErrorReportsSortedUniqueRefusals(t *testing.T) {
	err := SafetyError(
		[]string{"bin/tao", ".env.local", "./bin/tao", ".env.example"},
		[]string{"R  z -> a", "R  z -> a"},
	)
	if err == nil {
		t.Fatal("SafetyError() unexpectedly succeeded")
	}
	for _, want := range []string{
		"ambiguous git status entry: R  z -> a",
		"suspected secret path: .env.local",
		"generated artifact path: bin/tao",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("SafetyError() = %q, want text %q", err, want)
		}
	}
	if err := SafetyError([]string{".env.example", "internal/commit/safety.go"}, nil); err != nil {
		t.Fatalf("SafetyError() rejected safe paths: %v", err)
	}
}

func TestExpectedPathsRemainAdvisory(t *testing.T) {
	expected := NewExpectedPaths("internal/commit/*.go", "README.md")
	if !expected.Allows("./README.md") || !expected.Allows("internal/commit/message.go") {
		t.Fatal("ExpectedPaths did not allow exact and glob paths")
	}
	if expected.Allows("internal/commit/sub/message.go") {
		t.Fatal("single-star glob crossed a path segment")
	}
	unexpected := UnexpectedPaths([]string{"README.md", "extra.go"}, expected)
	if want := []string{"extra.go"}; !slices.Equal(unexpected, want) {
		t.Fatalf("UnexpectedPaths() = %q, want %q", unexpected, want)
	}
	if err := SafetyError(unexpected, nil); err != nil {
		t.Fatalf("advisory unexpected path became unsafe: %v", err)
	}
}

func TestClassifyStatusSplitsStagingBuckets(t *testing.T) {
	tests := []struct {
		name       string
		status     string
		candidates []string
		stagedOnly []string
		tracked    []string
		untracked  []string
		taoStaged  []string
	}{
		{name: "git rm deletion", status: "D  gone.go", candidates: []string{"gone.go"}, stagedOnly: []string{"gone.go"}},
		{name: "worktree deletion", status: " D gone.go", candidates: []string{"gone.go"}, tracked: []string{"gone.go"}},
		{name: "worktree modification", status: " M edited.go", candidates: []string{"edited.go"}, tracked: []string{"edited.go"}},
		{name: "staged and modified", status: "MM edited.go", candidates: []string{"edited.go"}, tracked: []string{"edited.go"}},
		{name: "staged addition", status: "A  new.go", candidates: []string{"new.go"}, stagedOnly: []string{"new.go"}},
		{name: "staged addition modified", status: "AM new.go", candidates: []string{"new.go"}, tracked: []string{"new.go"}},
		{name: "type change", status: "T  link.go", candidates: []string{"link.go"}, stagedOnly: []string{"link.go"}},
		{name: "untracked", status: "?? fresh.go", candidates: []string{"fresh.go"}, untracked: []string{"fresh.go"}},
		{name: "collapsed untracked directory", status: "?? dir/", candidates: []string{"dir"}, untracked: []string{"dir"}},
		{
			name:       "deleted then recreated untracked",
			status:     "D  swap.go\n?? swap.go",
			candidates: []string{"swap.go", "swap.go"},
			stagedOnly: []string{"swap.go"},
			untracked:  []string{"swap.go"},
		},
		{
			name:      "tao metadata lines",
			status:    "M  .tao/state.json\n D .tao/slices.json\n?? .tao/local.json",
			taoStaged: []string{".tao/state.json"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			classification := ClassifyStatus(test.status, nil)
			if !slices.Equal(classification.CommitCandidates, test.candidates) {
				t.Errorf("CommitCandidates = %q, want %q", classification.CommitCandidates, test.candidates)
			}
			if !slices.Equal(classification.StagedOnlyPaths, test.stagedOnly) {
				t.Errorf("StagedOnlyPaths = %q, want %q", classification.StagedOnlyPaths, test.stagedOnly)
			}
			if !slices.Equal(classification.TrackedStagePaths, test.tracked) {
				t.Errorf("TrackedStagePaths = %q, want %q", classification.TrackedStagePaths, test.tracked)
			}
			if !slices.Equal(classification.UntrackedStagePaths, test.untracked) {
				t.Errorf("UntrackedStagePaths = %q, want %q", classification.UntrackedStagePaths, test.untracked)
			}
			if !slices.Equal(classification.TaoStagedPaths, test.taoStaged) {
				t.Errorf("TaoStagedPaths = %q, want %q", classification.TaoStagedPaths, test.taoStaged)
			}
			if len(classification.AmbiguousLines) != 0 {
				t.Errorf("AmbiguousLines = %q, want none", classification.AmbiguousLines)
			}
		})
	}
}
