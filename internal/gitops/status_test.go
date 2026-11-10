package gitops

import (
	"slices"
	"testing"
)

func TestStatusPorcelainPathParsing(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		line      string
		wantPath  string
		ambiguous bool
	}{
		{name: "modified", line: " M internal/run/finalize.go", wantPath: "internal/run/finalize.go"},
		{name: "added", line: "A  internal/gitops/status.go", wantPath: "internal/gitops/status.go"},
		{name: "untracked", line: "?? docs/plan.md", wantPath: "docs/plan.md"},
		{name: "quoted spaces", line: ` M "docs/05 - Operations/S3 Buckets.md"`, wantPath: `"docs/05 - Operations/S3 Buckets.md"`},
		{name: "quoted escape", line: ` M "docs/tab\tname.md"`, wantPath: `"docs/tab\tname.md"`},
		{name: "malformed quote ambiguous", line: ` M "docs/missing-end.md`, wantPath: `"docs/missing-end.md`},
		{name: "rename ambiguous", line: "R  old.go -> new.go", ambiguous: true},
		{name: "copy ambiguous", line: "C  old.go -> new.go", ambiguous: true},
		{name: "arrow ambiguous", line: " M old.go -> new.go", wantPath: "old.go -> new.go"},
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

func TestStatusExactNULPaths(t *testing.T) {
	want := []string{" file.go", "file.go ", "old -> new", "tab\tline\n"}
	status := ""
	for _, p := range want {
		status += " M " + p + "\x00"
	}
	paths, ambiguous := PorcelainPaths(status)
	if !slices.Equal(paths, want) || len(ambiguous) != 0 {
		t.Fatalf("paths=%q ambiguous=%q", paths, ambiguous)
	}
	paths, ambiguous = PorcelainPaths(status + " M truncated")
	if len(paths) != 0 || len(ambiguous) == 0 {
		t.Fatalf("partial authority: %q %q", paths, ambiguous)
	}
}

func TestStatusProtectedBranch(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
	status := " M internal/run/run_setup.go\x00?? docs/usage.md\x00"
	paths, ambiguous := PorcelainPaths(status)
	if want := []string{"internal/run/run_setup.go", "docs/usage.md"}; !slices.Equal(paths, want) {
		t.Fatalf("paths = %v, want %v", paths, want)
	}
	if want := []string(nil); !slices.Equal(ambiguous, want) {
		t.Fatalf("ambiguous = %v, want %v", ambiguous, want)
	}
}

func TestFingerprintPorcelainPathsPreserveRenameCopyAndQuotedPaths(t *testing.T) {
	t.Parallel()
	status := "D  old.go\x00A  new.go\x00 M \"path with spaces.go\"\x00 M old -> new.go \x00"
	got := porcelainPaths(status)
	want := []string{"old.go", "new.go", `"path with spaces.go"`, "old -> new.go "}
	if !slices.Equal(got, want) {
		t.Fatalf("porcelainPaths() = %v, want %v", got, want)
	}
}

func TestStatusPorcelainIndexStatus(t *testing.T) {
	t.Parallel()
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
