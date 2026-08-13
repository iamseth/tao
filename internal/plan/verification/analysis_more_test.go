package verification

import (
	"path/filepath"
	"testing"
)

func TestAnalyzeCommandFindsCWDPathChecksAndShellHazards(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, "package.json"), `{"name":"root"}`)
	writeFile(t, filepath.Join(repo, "packages", "api", "package.json"), `{"name":"@repo/api"}`)
	writeFile(t, filepath.Join(repo, "packages", "api", "src", "api.test.ts"), "test")

	analysis := AnalyzeCommand(repo, "pnpm --filter @repo/api test --run src/api.test.ts")
	if analysis.WorkingDir != filepath.Join(repo, "packages", "api") {
		t.Fatalf("WorkingDir = %q", analysis.WorkingDir)
	}
	if len(analysis.PathChecks) != 1 || !analysis.PathChecks[0].Exists {
		t.Fatalf("expected existing path check, got %#v", analysis.PathChecks)
	}

	analysis = AnalyzeCommand(repo, "go test ./... -run Test(Foo)")
	if len(analysis.Findings) == 0 || analysis.Findings[0].Code != "verification_shell_hazard" || analysis.Findings[0].Suggestion != "'Test(Foo)'" {
		t.Fatalf("expected shell hazard warning, got %#v", analysis.Findings)
	}
}

func TestAnalyzeCommandWarnsForMissingFilterMissingPathAndMissingCWD(t *testing.T) {
	repo := t.TempDir()
	analysis := AnalyzeCommand(repo, "cd missing && pnpm --filter @repo/missing test missing.test.ts")
	codes := map[string]bool{}
	for _, finding := range analysis.Findings {
		codes[finding.Code] = true
	}
	for _, want := range []string{"verification_cwd_missing", "verification_path_missing"} {
		if !codes[want] {
			t.Fatalf("expected finding %q in %#v", want, analysis.Findings)
		}
	}
}

func TestAnalyzeNilAnalyzerAndEmptyCommand(t *testing.T) {
	var analyzer *Analyzer
	analysis := analyzer.Analyze("")
	if analysis.Command != "" || analysis.WorkingDir != "" || analysis.Executable != "" || len(analysis.Findings) != 0 {
		t.Fatalf("unexpected empty analysis: %#v", analysis)
	}
}

func TestAnalyzeCommandHandlesCompactSeparatorAndSingleQuotedBackslashes(t *testing.T) {
	repo := t.TempDir()
	commandDir := filepath.Join(repo, `pkg\name`)
	mkdir(t, commandDir)

	analysis := AnalyzeCommand(repo, `cd 'pkg\name'&&go test ./... -run 'Test\(Foo\)'`)
	if analysis.WorkingDir != commandDir {
		t.Fatalf("WorkingDir = %q, want %q", analysis.WorkingDir, commandDir)
	}
	if analysis.Executable != "go" {
		t.Fatalf("Executable = %q, want go", analysis.Executable)
	}
	if len(analysis.Findings) != 0 {
		t.Fatalf("expected quoted run pattern to remain safe, got %#v", analysis.Findings)
	}
}

func TestCommandFieldsPreserveQuotedValues(t *testing.T) {
	got := CommandFields(`go test "./pkg with space" -run 'TestExample'`)
	want := []string{"go", "test", "./pkg with space", "-run", "TestExample"}
	if len(got) != len(want) {
		t.Fatalf("CommandFields length = %d, want %d: %#v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("CommandFields[%d] = %q, want %q (all %#v)", i, got[i], want[i], got)
		}
	}
}
