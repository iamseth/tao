package gitops

import (
	"slices"
	"testing"
)

func TestStatusPorcelainPathParsing(t *testing.T) {
	tests := []struct {
		name      string
		line      string
		wantPath  string
		ambiguous bool
	}{
		{name: "modified", line: " M internal/run/finalize.go", wantPath: "internal/run/finalize.go"},
		{name: "added", line: "A  internal/gitops/status.go", wantPath: "internal/gitops/status.go"},
		{name: "untracked", line: "?? docs/plan.md", wantPath: "docs/plan.md"},
		{name: "rename ambiguous", line: "R  old.go -> new.go", ambiguous: true},
		{name: "copy ambiguous", line: "C  old.go -> new.go", ambiguous: true},
		{name: "arrow ambiguous", line: " M old.go -> new.go", ambiguous: true},
		{name: "empty path ambiguous", line: " M ", ambiguous: true},
		{name: "malformed ambiguous", line: " M", ambiguous: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, ambiguous := PorcelainPath(test.line)
			if ambiguous != test.ambiguous || got != test.wantPath {
				t.Fatalf("PorcelainPath(%q) = %q, %v; want %q, %v", test.line, got, ambiguous, test.wantPath, test.ambiguous)
			}
		})
	}
}

func TestStatusProtectedBranch(t *testing.T) {
	tests := []struct {
		branch string
		want   bool
	}{
		{branch: "main", want: true},
		{branch: "master", want: true},
		{branch: "tao/plan-a", want: false},
		{branch: "", want: false},
	}
	for _, test := range tests {
		if got := ProtectedBranch(test.branch); got != test.want {
			t.Fatalf("ProtectedBranch(%q) = %v, want %v", test.branch, got, test.want)
		}
	}
}

func TestStatusPorcelainPaths(t *testing.T) {
	status := "\n M internal/run/run_setup.go\n\t\n?? docs/usage.md\nR  old.go -> new.go\nC  copied.go -> clone.go\n"
	paths, ambiguous := PorcelainPaths(status)
	if want := []string{"internal/run/run_setup.go", "docs/usage.md"}; !slices.Equal(paths, want) {
		t.Fatalf("paths = %v, want %v", paths, want)
	}
	if want := []string{"R  old.go -> new.go", "C  copied.go -> clone.go"}; !slices.Equal(ambiguous, want) {
		t.Fatalf("ambiguous = %v, want %v", ambiguous, want)
	}
}

func TestFingerprintPorcelainPathsPreserveRenameCopyAndQuotedPaths(t *testing.T) {
	status := "R  old.go -> new.go\nC  source.go -> copied.go\n M \"path with spaces.go\"\nR  \"old name.go\" -> \"new name.go\"\r\n"
	got := porcelainPaths(status)
	want := []string{"new.go", "copied.go", "path with spaces.go", "new name.go"}
	if !slices.Equal(got, want) {
		t.Fatalf("porcelainPaths() = %v, want %v", got, want)
	}
}

func TestStatusPorcelainIndexStatus(t *testing.T) {
	tests := []struct {
		line string
		want bool
	}{
		{line: "M  a.go", want: true},
		{line: "A  a.go", want: true},
		{line: " M a.go", want: false},
		{line: "?? a.go", want: false},
		{line: "", want: false},
	}
	for _, test := range tests {
		if got := PorcelainIndexStatus(test.line); got != test.want {
			t.Fatalf("PorcelainIndexStatus(%q) = %v, want %v", test.line, got, test.want)
		}
	}
}
