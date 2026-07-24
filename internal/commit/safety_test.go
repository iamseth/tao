package commit

import (
	"slices"
	"strings"
	"testing"
)

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
