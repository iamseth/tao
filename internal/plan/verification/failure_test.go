package verification

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestClassifyRunDetectsMissingCWD(t *testing.T) {
	repo := t.TempDir()
	got := ClassifyRun(repo, Run{Command: "go test ./...", CWD: filepath.Join(repo, "missing")})
	if !got.Invalid || got.Code != "verification_cwd_missing" || got.Command != "go test ./..." {
		t.Fatalf("unexpected classification: %#v", got)
	}
}

func TestClassifyFailureInvalidCommandPatterns(t *testing.T) {
	tests := []struct {
		name    string
		output  string
		want    string
		message string
	}{
		{name: "command not found", output: "sh: eslint: command not found", want: "verification_command_not_found"},
		{name: "exec missing", output: "exec: no such file or directory", want: "verification_command_not_found"},
		{name: "config missing", output: "Config file could not resolve ./missing.config.js", want: "verification_config_missing"},
		{name: "no tests", output: "No test files found", want: "verification_no_test_files"},
		{name: "chdir missing", output: "chdir ./service: not a directory", want: "verification_cwd_missing"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ClassifyFailure(t.TempDir(), "npm test", tt.output)
			if !got.Invalid || got.Code != tt.want || got.Command != "npm test" {
				t.Fatalf("unexpected classification: %#v", got)
			}
		})
	}
}

func TestClassifyFailureSuggestsPathCWDMismatchCorrection(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, "packages", "api", "internal", "api_test.go"), "package api")
	got := ClassifyFailure(repo, "cd packages/api && go test packages/api/internal/api_test.go", "No test files found")
	if !got.Invalid || got.Code != "verification_path_cwd_mismatch" {
		t.Fatalf("expected path mismatch, got %#v", got)
	}
	if !strings.Contains(got.CorrectedCommand, "go test internal/api_test.go") {
		t.Fatalf("expected corrected command to use package-relative path, got %q", got.CorrectedCommand)
	}
}

func TestClassifyFailureLeavesTestFailuresValid(t *testing.T) {
	got := ClassifyFailure(t.TempDir(), "go test ./...", "--- FAIL: TestExample")
	if got.Invalid || got.Code != "" {
		t.Fatalf("expected valid code failure, got %#v", got)
	}
}
