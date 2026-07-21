package run

import "testing"

// These characterization tests pin the commit-safety classification used by
// automatic slice commits. They intentionally encode today's substring-based
// secret detection (for example, "credential" anywhere in the path) and glob
// semantics so any classification change is caught.

func TestSuspectedSecretPathClassification(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		// .env base-name prefix (case-insensitive).
		{".env", true},
		{".env.local", true},
		{"config/.ENV.production", true},
		// Substring tokens anywhere in the (lowercased) path.
		{"config/credentials.json", true},
		{"deploy/aws-credential", true},
		{"internal/CREDENTIAL/notes.txt", true},
		{"app/secret.yaml", true},
		{"keys/private_key.pem", true},
		{"home/.ssh/id_rsa", true},
		// Substring matches even mid-token, matching strings.Contains today.
		{"docs/uncredentialed.md", true},
		// Clearly safe paths.
		{"internal/run/run.go", false},
		{"README.md", false},
		{"environment.go", false}, // base does not start with ".env"
	}
	for _, tc := range cases {
		if got := suspectedSecretPath(tc.path); got != tc.want {
			t.Errorf("suspectedSecretPath(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}

func TestGeneratedPathClassification(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{"coverage.out", true},
		{"bin/tao", true},
		{"bin/sub/dir/thing", true},
		{"internal/run/run.go", false},
		{"cmd/bin/tao", false}, // bin/ only matches as a leading prefix
		{"coverage.out.bak", false},
	}
	for _, tc := range cases {
		if got := generatedPath(tc.path); got != tc.want {
			t.Errorf("generatedPath(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}

// TestDefaultCommitSafetyPolicyMatchesFreeFunctions guards that the named
// policy type and the package-level helper functions stay in lockstep.
func TestDefaultCommitSafetyPolicyMatchesFreeFunctions(t *testing.T) {
	paths := []string{".env.local", "config/credentials.json", "coverage.out", "bin/tao", "internal/run/run.go"}
	for _, path := range paths {
		if defaultCommitSafetyPolicy.suspectedSecret(path) != suspectedSecretPath(path) {
			t.Errorf("policy.suspectedSecret(%q) disagrees with suspectedSecretPath", path)
		}
		if defaultCommitSafetyPolicy.generated(path) != generatedPath(path) {
			t.Errorf("policy.generated(%q) disagrees with generatedPath", path)
		}
	}
}

func TestPlanCommitGlobMatchSemantics(t *testing.T) {
	cases := []struct {
		pattern string
		path    string
		want    bool
	}{
		// Single star matches within a path segment, not across "/".
		{"internal/*.go", "internal/run.go", true},
		{"internal/*.go", "internal/run/run.go", false},
		{"internal/*", "internal/run", true},
		{"internal/*", "internal/run/run.go", false},
		// Double star matches across segments.
		{"internal/**", "internal/run/run.go", true},
		{"internal/**/*.go", "internal/run/run.go", true},
		// "**/" optional-directory prefix matches zero or more dirs.
		{"**/run.go", "run.go", true},
		{"**/run.go", "internal/run/run.go", true},
		// "?" matches a single non-slash character.
		{"file?.go", "fileA.go", true},
		{"file?.go", "file/.go", false},
		// Character classes pass through to the regexp.
		{"file[0-9].go", "file3.go", true},
		{"file[0-9].go", "fileX.go", false},
		// Dots are literal, not regexp wildcards.
		{"a.go", "aXgo", false},
	}
	for _, tc := range cases {
		if got := planCommitGlobMatch(tc.pattern, tc.path); got != tc.want {
			t.Errorf("planCommitGlobMatch(%q, %q) = %v, want %v", tc.pattern, tc.path, got, tc.want)
		}
	}
}

func TestExpectedPlanCommitPathSetAllows(t *testing.T) {
	set := expectedPlanCommitPathSet{
		exact: map[string]bool{"internal/run/run.go": true},
		globs: []string{"internal/queue/*.go"},
	}
	if !set.Allows("internal/run/run.go") {
		t.Error("expected exact path to be allowed")
	}
	if !set.Allows("./internal/run/run.go") {
		t.Error("expected path to be normalized before exact match")
	}
	if !set.Allows("internal/queue/handler.go") {
		t.Error("expected glob path to be allowed")
	}
	if set.Allows("internal/queue/sub/handler.go") {
		t.Error("did not expect single-star glob to cross a path segment")
	}
	if set.Allows("internal/agent/agent.go") {
		t.Error("did not expect unrelated path to be allowed")
	}
}
