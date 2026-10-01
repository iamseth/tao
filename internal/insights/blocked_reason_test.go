package insights

import (
	"fmt"
	"strings"
	"testing"
)

func TestBlockedClassification(t *testing.T) {
	tests := []struct {
		command      string
		paths        []string
		reason, want string
	}{
		{"golangci-lint run --timeout=5m ./...", nil, "network unavailable", "lint_failure"},
		{" \tgo test -count=1 ./... \t", []string{"./internal/x_test.go", `internal\y_test.go`}, "dependency missing", "test_test_files"},
		{"go build -o bin/tao ./cmd/tao", []string{"x_test.go"}, "", "build_test_files"},
		{"make lint", []string{"x_test.go"}, "", "lint_test_files"},
		{"make test", nil, "", "test_failure"},
		{"make build", nil, "", "build_failure"},
	}
	for _, tt := range tests {
		if got := classifyBlocked(tt.command, tt.paths, tt.reason); got != tt.want {
			t.Errorf("%q: %s != %s", tt.command, got, tt.want)
		}
	}
	for _, command := range []string{"", "make verify", "make lint test", "make -f evil lint", "make lint -f evil", "echo go test", "echo 'golangci-lint run'", "env go test", "go testing", "go test && go build", "go test; touch sentinel", "go test\ngo build", "go test $(touch sentinel)", "'go test'", "go test `id`", "go test | cat", "go test >file"} {
		if got := classifyBlocked(command, []string{"x_test.go"}, "custom reason"); got != "other" {
			t.Errorf("unsupported %q: %s", command, got)
		}
	}
	for _, paths := range [][]string{nil, {}, {""}, {"x_test.go", "x.go"}, {"../x_test.go"}, {"a/../x_test.go"}, {"/x_test.go"}, {`C:\x_test.go`}, {"a//x_test.go"}, {"x_test.go\n"}, {"$(touch sentinel)_test.go"}, {"_test.go"}} {
		if got := classifyBlocked("go test ./...", paths, "outside scope owner-token leak stale cache"); got != "test_failure" {
			t.Errorf("paths %q: %s", paths, got)
		}
	}
	for _, reason := range []string{"", "all verification commands pass", "pnpm dependency precheck", "service is not unreachable", "no dependency missing", "not timed out", "without unrelated failure", "outside scope", "stale cache", "owner-token leak", "external service", "timeout configuration", "dependency check passed"} {
		if got := NormalizeBlockedReason(reason); got != "other" {
			t.Errorf("neutral %q: %s", reason, got)
		}
	}
	for i := 0; i < 1000; i++ {
		if got := NormalizeBlockedReason(fmt.Sprintf("novel reason %d: %s", i, strings.Repeat("x", i))); got != "other" {
			t.Fatal(got)
		}
	}
}
