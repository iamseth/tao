package verification

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMechanicalCorrection(t *testing.T) {
	repo := t.TempDir()
	pkg := filepath.Join(repo, "packages", "api")
	writeFile(t, filepath.Join(pkg, "internal", "api_test.go"), "package api")
	writeFile(t, filepath.Join(pkg, "internal", "other_test.go"), "package api")
	writeFile(t, filepath.Join(pkg, "existing_test.go"), "package api")
	writeFile(t, filepath.Join(repo, "-P", "api_test.go"), "package api")
	const target = "packages/api/internal/api_test.go"
	const command = "go test " + target
	const inline = "cd packages/api && " + command
	const noTests = "No test files found"
	tests := []struct {
		name    string
		run     Run
		command string
	}{
		{"inline cwd", Run{Command: inline, Details: noTests}, "go test internal/api_test.go"},
		{"inline explicit root", Run{Command: inline, CWD: repo, Details: noTests}, "go test internal/api_test.go"},
		{"structured absolute cwd", Run{Command: command, CWD: pkg, Details: noTests}, "go test internal/api_test.go"},
		{"structured relative cwd", Run{Command: command, CWD: "packages/api", Details: noTests}, "go test internal/api_test.go"},
		{"cd option is not a directory", Run{Command: "cd -P && go test ./-P/api_test.go", Details: noTests}, ""},
		{"nested cd", Run{Command: "cd api && " + command, CWD: "packages", Details: noTests}, "go test internal/api_test.go"},
		{"preserve flags and targets", Run{Command: "go test -race -count=1 -run TestAPI " + target + " existing_test.go -timeout 30s", CWD: pkg, Details: noTests}, "go test -race -count=1 -run TestAPI internal/api_test.go existing_test.go -timeout 30s"},
		{"filter containing target remains unchanged", Run{Command: "go test -run " + target + " " + target, CWD: pkg, Details: noTests}, "go test -run " + target + " internal/api_test.go"},
		{"boolean and filter assignments", Run{Command: "go test -race=false -run=TestAPI -skip TestOther " + target, CWD: pkg, Details: noTests}, "go test -race=false -run=TestAPI -skip TestOther internal/api_test.go"},
		{"explicit relative target", Run{Command: "go test ./" + target, CWD: pkg, Details: noTests}, "go test ./internal/api_test.go"},
		{"directory target", Run{Command: "go test ./packages/api/internal", CWD: pkg, Details: noTests}, "go test ./internal"},
		{"recursive target", Run{Command: "go test ./packages/api/internal/...", CWD: pkg, Details: noTests}, "go test ./internal/..."},
		{"ordinary code failure", Run{Command: inline, Details: "--- FAIL: TestAPI"}, ""},
		{"empty diagnostic", Run{Command: inline}, ""},
		{"missing executable", Run{Command: inline, Details: "go: command not found"}, ""},
		{"missing config", Run{Command: inline, Details: "config file not found"}, ""},
		{"diagnostic replacement true", Run{Command: "go test ./...", CWD: pkg, Details: noTests + "; corrected command: true"}, ""},
		{"diagnostic replacement ignored", Run{Command: inline, Details: noTests + "; corrected command: go test internal/other_test.go"}, "go test internal/api_test.go"},
		{"no explicit package cwd", Run{Command: command, Details: noTests}, ""},
		{"root cwd", Run{Command: command, CWD: repo, Details: noTests}, ""},
		{"missing cwd", Run{Command: command, CWD: filepath.Join(repo, "missing"), Details: noTests}, ""},
		{"missing target", Run{Command: "go test packages/api/internal/missing_test.go", CWD: pkg, Details: noTests}, ""},
		{"different prefix", Run{Command: "go test packages/other/internal/api_test.go", CWD: pkg, Details: noTests}, ""},
		{"two corrections", Run{Command: command + " packages/api/internal/other_test.go", CWD: pkg, Details: noTests}, ""},
		{"duplicate target", Run{Command: command + " " + target, CWD: pkg, Details: noTests}, ""},
		{"filter is not a target", Run{Command: "go test -run " + target + " ./...", CWD: pkg, Details: noTests}, ""},
		{"filter assignment is not a target", Run{Command: "go test -run=" + target + " ./...", CWD: pkg, Details: noTests}, ""},
		{"unknown flag", Run{Command: "go test -unknown " + target, CWD: pkg, Details: noTests}, ""},
		{"empty flag value", Run{Command: command + " -run=", CWD: pkg, Details: noTests}, ""},
		{"missing flag value", Run{Command: command + " -run", CWD: pkg, Details: noTests}, ""},
		{"flag as value", Run{Command: command + " -run -race", CWD: pkg, Details: noTests}, ""},
		{"invalid boolean", Run{Command: command + " -race=yes", CWD: pkg, Details: noTests}, ""},
		{"args passthrough", Run{Command: "go test -args " + target, CWD: pkg, Details: noTests}, ""},
		{"unrecognized tool", Run{Command: "true " + target, CWD: pkg, Details: noTests}, ""},
		{"wrapper", Run{Command: "env go test " + target, CWD: pkg, Details: noTests}, ""},
		{"assignment", Run{Command: "GOFLAGS=-race " + command, CWD: pkg, Details: noTests}, ""},
		{"parent traversal", Run{Command: "go test packages/api/../api/internal/api_test.go", CWD: pkg, Details: noTests}, ""},
		{"noncanonical target", Run{Command: "go test packages//api/internal/api_test.go", CWD: pkg, Details: noTests}, ""},
		{"absolute target", Run{Command: "go test " + filepath.Join(pkg, "internal", "api_test.go"), CWD: pkg, Details: noTests}, ""},
		{"implicit import target", Run{Command: "go test packages/api/internal", CWD: pkg, Details: noTests}, ""},
	}
	for _, suffix := range []string{"; true", " && true", " || true", " | true", " > out", " < in", " &", "\ntrue", " $(true)", " `true`", " # comment", " *", " ${TARGET}", " ~"} {
		tests = append(tests, struct {
			name    string
			run     Run
			command string
		}{"shell " + suffix, Run{Command: inline + suffix, Details: noTests}, ""})
	}
	for _, cmd := range []string{
		"cd 'packages/api' && " + command,
		"cd packages/api extra && " + command,
		"cd packages/api&&" + command,
		"cd packages/api && cd . && " + command,
		"go test \"" + target + "\"",
		"go test packages/api/internal/\\api_test.go",
		"go\ttest " + target,
		"go  test " + target,
		" go test " + target,
		command + " ",
	} {
		tests = append(tests, struct {
			name    string
			run     Run
			command string
		}{"ambiguous " + cmd, Run{Command: cmd, CWD: pkg, Details: noTests}, ""})
	}
	canonicalPkg, err := filepath.EvalSymlinks(pkg)
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := MechanicalCorrection(repo, tt.run)
			if tt.command == "" {
				if ok || got != (Run{}) {
					t.Fatalf("unsafe correction: %#v, %v", got, ok)
				}
				return
			}
			want := Run{Command: tt.command, CWD: canonicalPkg}
			if !ok || got != want {
				t.Fatalf("got %#v, %v; want %#v", got, ok, want)
			}
		})
	}
}

func TestMechanicalCorrectionFilesystemBoundaries(t *testing.T) {
	repo := t.TempDir()
	outside := t.TempDir()
	pkg := filepath.Join(repo, "packages", "api")
	writeFile(t, filepath.Join(pkg, "internal", "api_test.go"), "package api")
	writeFile(t, filepath.Join(outside, "api_test.go"), "package api")
	for _, link := range []struct{ path, target string }{
		{filepath.Join(pkg, "escaped_test.go"), filepath.Join(outside, "api_test.go")},
		{filepath.Join(repo, "escaped"), outside},
		{filepath.Join(repo, "alias"), pkg},
		{filepath.Join(pkg, "dangling_test.go"), filepath.Join(outside, "missing")},
	} {
		if err := os.Symlink(link.target, link.path); err != nil {
			t.Fatal(err)
		}
	}
	writeFile(t, filepath.Join(pkg, "packages", "api", "internal", "api_test.go"), "package api")
	for _, run := range []Run{
		{Command: "go test packages/api/escaped_test.go", CWD: pkg},
		{Command: "go test escaped/api_test.go", CWD: "escaped"},
		{Command: "go test packages/api/internal/api_test.go", CWD: outside},
		{Command: "cd " + outside + " && go test packages/api/internal/api_test.go"},
		{Command: "go test alias/internal/api_test.go", CWD: "alias"},
		{Command: "go test packages/api/dangling_test.go", CWD: pkg},
		// Both root-relative and cwd-relative targets exist: no proof of a mismatch.
		{Command: "go test packages/api/internal/api_test.go", CWD: pkg},
	} {
		run.Details = "No test files found"
		if got, ok := MechanicalCorrection(repo, run); ok || got != (Run{}) {
			t.Errorf("%#v: unsafe correction %#v, %v", run, got, ok)
		}
	}
}
