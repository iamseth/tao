package plan

import "testing"

func TestNormalizeReviewFindingPath(t *testing.T) {
	for _, tt := range []struct{ input, want string }{
		{`./internal\\rework\\generate.go`, "internal/rework/generate.go"},
		{" ././internal//plan/ ", "internal/plan"},
		{"README.md", "README.md"},
		{"", ""}, {".", ""}, {"/tmp/outside.go", ""},
		{"../outside.go", ""}, {"internal/../outside.go", ""},
		{"C:/outside.go", ""}, {"C:outside.go", ""},
		{"internal/*.go", ""}, {"a?", ""}, {"a[b]", ""}, {"a{b}", ""},
		{"...", ""}, {".../a", ""}, {"a/...", ""}, {"a/.../b", ""},
	} {
		t.Run(tt.input, func(t *testing.T) {
			got, ok := NormalizeReviewFindingPath(tt.input)
			if got != tt.want || ok != (tt.want != "") {
				t.Fatalf("NormalizeReviewFindingPath(%q) = %q, %v; want %q", tt.input, got, ok, tt.want)
			}
		})
	}
}

func TestPathsOverlap(t *testing.T) {
	for _, tt := range []struct {
		file, expected string
		want           bool
	}{
		{"./internal/plan/paths.go", `internal\plan\paths.go`, true},
		{"internal/plan/paths.go", "internal/plan", true},
		{"internal/plan/paths.go", "internal/plan/", true},
		{"dir.ext/file", "dir.ext/", true},
		{"dir.ext/file", "dir.ext", false},
		{"internal/planner/file.go", "internal/plan", false},
		{"internal/plan", "internal/plan/paths.go", false},
		{"", "internal/plan", false}, {"a", "", false}, {".", ".", false},
	} {
		t.Run(tt.file+":"+tt.expected, func(t *testing.T) {
			if got := PathsOverlap(tt.file, tt.expected); got != tt.want {
				t.Fatalf("PathsOverlap(%q, %q) = %v, want %v", tt.file, tt.expected, got, tt.want)
			}
		})
	}
}
