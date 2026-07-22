package rework

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/iamseth/tao/internal/plan"
)

func TestRoundFromSliceID(t *testing.T) {
	tests := []struct {
		name string
		id   string
		want int
	}{
		{name: "round one", id: "r101-fix", want: 1},
		{name: "historical non-digit index", id: "r1ab-fix", want: 1},
		{name: "round two", id: "r201-fix", want: 2},
		{name: "trimmed round two", id: " r202-fix ", want: 2},
		{name: "bare round prefix", id: "r2", want: 0},
		{name: "ordinary slice", id: "002-feature", want: 0},
		{name: "missing round", id: "r01-fix", want: 0},
		{name: "non-numeric round", id: "rx01-fix", want: 0},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := RoundFromSliceID(test.id); got != test.want {
				t.Fatalf("RoundFromSliceID(%q) = %d, want %d", test.id, got, test.want)
			}
		})
	}
}

func TestGenerateSlicesMapsFindingsToReworkSlices(t *testing.T) {
	updatedAt := time.Date(2026, 6, 28, 23, 0, 0, 0, time.UTC)
	detail := &plan.PlanDetail{
		State: plan.State{UpdatedAt: updatedAt, Repo: plan.Repo{Root: repoFixture(t, map[string]string{
			"Makefile": ".PHONY: build test\nbuild:\n\tgo build ./...\ntest:\n\tgo test ./...\n",
			"go.mod":   "module example.com/project\n",
		})}},
		Slices: plan.SlicesFile{Slices: []plan.Slice{
			{
				ID:            "001-original",
				ExpectedFiles: []string{"internal/rework/generate.go"},
				Verification:  plan.Verification{Commands: []string{"go test ./internal/rework -count=1", "make test"}},
			},
			{
				ID:            "002-other",
				ExpectedFiles: []string{"internal/plan/lifecycle.go"},
				Verification:  plan.Verification{Commands: []string{"go test ./internal/plan"}},
			},
		}},
	}
	findings := []plan.ReviewFinding{
		{
			Severity:   "error",
			File:       "internal/rework/generate.go",
			Line:       42,
			Message:    "Generate deterministic rework slices",
			Suggestion: "- parse review fallback\n- add tests",
		},
		{
			File:       "README.md",
			Message:    "Document the command",
			Suggestion: "Mention rework in workflow docs.",
		},
	}

	got := GenerateSlices(detail, findings, 2)
	if len(got) != 2 {
		t.Fatalf("GenerateSlices returned %d slices, want 2", len(got))
	}

	first := got[0]
	if first.ID != "r201-internal-rework-generate-go" {
		t.Fatalf("first ID = %q", first.ID)
	}
	if first.Status != plan.StatusPending {
		t.Fatalf("first status = %q", first.Status)
	}
	if first.Goal != "Generate deterministic rework slices" {
		t.Fatalf("first goal = %q", first.Goal)
	}
	if !slices.Equal(first.Tasks, []string{"address the review finding", "parse review fallback", "add tests"}) {
		t.Fatalf("first tasks = %#v", first.Tasks)
	}
	if !slices.Equal(first.ExpectedFiles, []string{"internal/rework/generate.go"}) {
		t.Fatalf("first expected files = %#v", first.ExpectedFiles)
	}
	wantCommands := []string{"go test ./internal/rework -count=1", "make test", "go test ./internal/rework"}
	if !slices.Equal(first.Verification.Commands, wantCommands) {
		t.Fatalf("first verification commands = %#v, want %#v", first.Verification.Commands, wantCommands)
	}
	if slices.Contains(first.Verification.Commands, "go test ./internal/rework -run TestGenerateSlicesMapsFindingsToReworkSlices") {
		t.Fatalf("verification command should be package-scoped, got %#v", first.Verification.Commands)
	}
	if !first.Timing.CreatedAt.Equal(updatedAt) || !first.Timing.UpdatedAt.Equal(updatedAt) {
		t.Fatalf("first timing = %#v", first.Timing)
	}

	second := got[1]
	if second.ID != "r202-readme-md" {
		t.Fatalf("second ID = %q", second.ID)
	}
	if !slices.Equal(second.Verification.Commands, []string{"make build", "make test"}) {
		t.Fatalf("second verification commands = %#v", second.Verification.Commands)
	}
}

func TestGenerateSlicesCarriesOverOverlappingExpectedFiles(t *testing.T) {
	detail := &plan.PlanDetail{
		State: plan.State{Repo: plan.Repo{Root: repoFixture(t, map[string]string{
			"go.mod": "module example.com/project\n",
		})}},
		Slices: plan.SlicesFile{Slices: []plan.Slice{{
			ID:            "001-original",
			ExpectedFiles: []string{"lww_map.go", "lww_map_test.go"},
			Verification:  plan.Verification{Commands: []string{"gofmt -w lww_map.go lww_map_test.go"}},
		}}},
	}

	got := GenerateSlices(detail, []plan.ReviewFinding{{File: "lww_map.go", Message: "Fix map deltas"}}, 1)
	if len(got) != 1 {
		t.Fatalf("GenerateSlices returned %d slices, want 1", len(got))
	}
	if !slices.Equal(got[0].ExpectedFiles, []string{"lww_map.go", "lww_map_test.go"}) {
		t.Fatalf("expected associated test file to carry over, got %#v", got[0].ExpectedFiles)
	}
}

func TestGenerateSlicesNormalizesAndSkipsUnsafeFindingPaths(t *testing.T) {
	detail := &plan.PlanDetail{State: plan.State{Repo: plan.Repo{Root: repoFixture(t, map[string]string{
		"go.mod": "module example.com/project\n",
	})}}}
	findings := []plan.ReviewFinding{
		{File: `./internal\\rework\\generate.go`, Message: "Normalize slashes"},
		{File: "", Message: "empty"},
		{File: "/tmp/outside.go", Message: "absolute"},
		{File: "../outside.go", Message: "parent"},
		{File: "internal/*.go", Message: "wildcard"},
		{File: "C:/outside.go", Message: "windows absolute"},
	}

	got := GenerateSlices(detail, findings, 1)
	if len(got) != 1 {
		t.Fatalf("GenerateSlices returned %d slices, want only the safe normalized path", len(got))
	}
	if got[0].ID != "r101-internal-rework-generate-go" || !slices.Equal(got[0].ExpectedFiles, []string{"internal/rework/generate.go"}) {
		t.Fatalf("unsafe filtering/normalization failed: %#v", got[0])
	}
}

func TestGenerateSlicesUsesDetectedGoRepoLevelVerification(t *testing.T) {
	detail := &plan.PlanDetail{
		State: plan.State{Repo: plan.Repo{Root: repoFixture(t, map[string]string{
			"go.mod": "module example.com/project\n",
		})}},
	}

	got := GenerateSlices(detail, []plan.ReviewFinding{{File: "README.md", Message: "Document it"}}, 1)
	if len(got) != 1 {
		t.Fatalf("GenerateSlices returned %d slices, want 1", len(got))
	}
	wantCommands := []string{"go build ./...", "go test ./..."}
	if !slices.Equal(got[0].Verification.Commands, wantCommands) {
		t.Fatalf("verification commands = %#v, want %#v", got[0].Verification.Commands, wantCommands)
	}
}

func TestGenerateSlicesPrefersInheritedTypeScriptVerification(t *testing.T) {
	detail := &plan.PlanDetail{
		State: plan.State{Repo: plan.Repo{Root: repoFixture(t, map[string]string{
			"package.json": `{"scripts":{"test":"vitest","lint":"eslint ."}}`,
		})}},
		Slices: plan.SlicesFile{Slices: []plan.Slice{
			{
				ID:            "001-rollcall-feature",
				ExpectedFiles: []string{"apps/web/src/rollcall.ts"},
				Verification:  plan.Verification{Commands: []string{"pnpm test", "pnpm lint", "pnpm test"}},
			},
			{
				ID:            "002-rollcall-package",
				ExpectedFiles: []string{"apps/web/"},
				Verification:  plan.Verification{Commands: []string{"pnpm lint", "pnpm typecheck"}},
			},
			{
				ID:            "r101-apps-web-src-rollcall-ts",
				ExpectedFiles: []string{"apps/web/src/rollcall.ts"},
				Verification:  plan.Verification{Commands: []string{"go test ./..."}},
			},
		}},
	}

	got := GenerateSlices(detail, []plan.ReviewFinding{{File: "apps/web/src/rollcall.ts", Message: "Fix rollcall"}}, 1)
	if len(got) != 1 {
		t.Fatalf("GenerateSlices returned %d slices, want 1", len(got))
	}
	wantCommands := []string{"pnpm test", "pnpm lint", "pnpm typecheck"}
	if !slices.Equal(got[0].Verification.Commands, wantCommands) {
		t.Fatalf("verification commands = %#v, want %#v", got[0].Verification.Commands, wantCommands)
	}
	if slices.Contains(got[0].Verification.Commands, "go test ./...") {
		t.Fatalf("TypeScript rework received fabricated Go verification: %#v", got[0].Verification.Commands)
	}
}

func TestGenerateSlicesUsesNestedGoModulePackageVerification(t *testing.T) {
	detail := &plan.PlanDetail{State: plan.State{Repo: plan.Repo{Root: repoFixture(t, map[string]string{
		"services/api/go.mod": "module example.com/api\n",
	})}}}

	got := GenerateSlices(detail, []plan.ReviewFinding{{File: "services/api/internal/server/server.go", Message: "Fix server"}}, 1)
	if len(got) != 1 {
		t.Fatalf("GenerateSlices returned %d slices, want 1", len(got))
	}
	wantCommands := []string{"cd services/api && go test ./internal/server"}
	if !slices.Equal(got[0].Verification.Commands, wantCommands) {
		t.Fatalf("verification commands = %#v, want %#v", got[0].Verification.Commands, wantCommands)
	}
}

func TestGenerateSlicesUsesRecordedWorkspaceModuleLayout(t *testing.T) {
	controlRoot := repoFixture(t, map[string]string{
		"go.mod": "module example.com/control\n",
	})
	workspaceRoot := repoFixture(t, map[string]string{
		"services/api/go.mod": "module example.com/api\n",
	})
	detail := &plan.PlanDetail{State: plan.State{
		Repo:      plan.Repo{Root: controlRoot},
		Workspace: &plan.Workspace{Strategy: plan.WorkspaceStrategyWorktree, Path: workspaceRoot},
	}}

	got := GenerateSlices(detail, []plan.ReviewFinding{
		{File: "services/api/internal/server/server.go", Message: "Fix server"},
		{File: "README.md", Message: "Document server"},
	}, 1)
	if len(got) != 2 {
		t.Fatalf("GenerateSlices returned %d slices, want 2", len(got))
	}
	if want := []string{"cd services/api && go test ./internal/server"}; !slices.Equal(got[0].Verification.Commands, want) {
		t.Fatalf("workspace package verification = %#v, want %#v", got[0].Verification.Commands, want)
	}
	if want := []string{"git diff --check -- 'README.md'"}; !slices.Equal(got[1].Verification.Commands, want) {
		t.Fatalf("workspace repository verification = %#v, want %#v", got[1].Verification.Commands, want)
	}
}

func TestGenerateSlicesMissingRecordedWorkspaceDoesNotUseControlModuleLayout(t *testing.T) {
	controlRoot := repoFixture(t, map[string]string{
		"go.mod": "module example.com/control\n",
	})
	detail := &plan.PlanDetail{
		State: plan.State{
			Repo: plan.Repo{Root: controlRoot},
			Workspace: &plan.Workspace{
				Strategy: plan.WorkspaceStrategyWorktree,
				Path:     filepath.Join(t.TempDir(), "missing-worktree"),
			},
		},
		Slices: plan.SlicesFile{Slices: []plan.Slice{{
			ID:            "001-docs",
			ExpectedFiles: []string{"docs/guide.md"},
			Verification:  plan.Verification{Commands: []string{"pnpm test"}},
		}}},
	}

	got := GenerateSlices(detail, []plan.ReviewFinding{
		{File: "internal/server/server.go", Message: "Fix server"},
		{File: "docs/guide.md", Message: "Clarify guide"},
	}, 1)
	if len(got) != 2 {
		t.Fatalf("GenerateSlices returned %d slices, want 2", len(got))
	}
	if want := []string{"git diff --check -- 'internal/server/server.go'"}; !slices.Equal(got[0].Verification.Commands, want) {
		t.Fatalf("missing-worktree package verification = %#v, want %#v", got[0].Verification.Commands, want)
	}
	if want := []string{"pnpm test"}; !slices.Equal(got[1].Verification.Commands, want) {
		t.Fatalf("missing-worktree inherited verification = %#v, want %#v", got[1].Verification.Commands, want)
	}
}

func TestGenerateSlicesCurrentWorkspaceUsesControlRoot(t *testing.T) {
	controlRoot := repoFixture(t, map[string]string{
		"services/api/go.mod": "module example.com/api\n",
	})
	otherRoot := repoFixture(t, map[string]string{
		"go.mod": "module example.com/other\n",
	})
	detail := &plan.PlanDetail{State: plan.State{
		Repo:      plan.Repo{Root: controlRoot},
		Workspace: &plan.Workspace{Strategy: plan.WorkspaceStrategyCurrent, Path: otherRoot},
	}}

	got := GenerateSlices(detail, []plan.ReviewFinding{{File: "services/api/internal/server/server.go", Message: "Fix server"}}, 1)
	if len(got) != 1 {
		t.Fatalf("GenerateSlices returned %d slices, want 1", len(got))
	}
	want := []string{"cd services/api && go test ./internal/server"}
	if !slices.Equal(got[0].Verification.Commands, want) {
		t.Fatalf("verification commands = %#v, want %#v", got[0].Verification.Commands, want)
	}
}

func TestGenerateSlicesUsesFileScopedGitFallbackForUnknownRepository(t *testing.T) {
	detail := &plan.PlanDetail{State: plan.State{Repo: plan.Repo{Root: repoFixture(t, map[string]string{
		"README.md": "# project\n",
	})}}}

	got := GenerateSlices(detail, []plan.ReviewFinding{{File: "internal/project.go", Message: "Fix unknown project"}}, 1)
	if len(got) != 1 {
		t.Fatalf("GenerateSlices returned %d slices, want 1", len(got))
	}
	wantCommands := []string{"git diff --check -- 'internal/project.go'"}
	if !slices.Equal(got[0].Verification.Commands, wantCommands) {
		t.Fatalf("verification commands = %#v, want %#v", got[0].Verification.Commands, wantCommands)
	}
	if got[0].Verification.Source != "file-scoped Git diff check only; does not provide semantic test coverage" {
		t.Fatalf("verification source = %q", got[0].Verification.Source)
	}
}

func TestGenerateSlicesShellQuotesGitFallbackFindingPath(t *testing.T) {
	detail := &plan.PlanDetail{}
	file := "docs/reviewer's $(note).md"

	got := GenerateSlices(detail, []plan.ReviewFinding{{File: file, Message: "Clarify note"}}, 1)
	if len(got) != 1 {
		t.Fatalf("GenerateSlices returned %d slices, want 1", len(got))
	}
	want := `git diff --check -- 'docs/reviewer'"'"'s $(note).md'`
	if got[0].Verification.Commands[0] != want {
		t.Fatalf("verification command = %q, want %q", got[0].Verification.Commands[0], want)
	}
}

func TestReviewFindingsFallsBackToReviewMarkdown(t *testing.T) {
	detail := &plan.PlanDetail{
		State:  plan.State{Plan: plan.PlanState{Review: &plan.PlanReview{Verdict: plan.ReviewVerdictChangesRequested, FindingsCount: 1}}},
		Review: plan.PlanReviewArtifact{Content: "# Review\n\n```tao-review-json\n{\"verdict\":\"changes_requested\",\"summary\":\"fix it\",\"findings\":[{\"severity\":\"warning\",\"file\":\"internal/run/review.go\",\"line\":17,\"message\":\"keep findings\",\"suggestion\":\"persist them\"}]}\n```\n"},
	}

	got := ReviewFindings(detail)
	if len(got) != 1 {
		t.Fatalf("ReviewFindings returned %d findings, want 1", len(got))
	}
	finding := got[0]
	if finding.Severity != "warning" || finding.File != "internal/run/review.go" || finding.Line != 17 || finding.Message != "keep findings" || finding.Suggestion != "persist them" {
		t.Fatalf("unexpected fallback finding: %#v", finding)
	}
}

func TestGenerateSlicesEmptyFindingsReturnsNone(t *testing.T) {
	if got := GenerateSlices(&plan.PlanDetail{}, nil, 1); len(got) != 0 {
		t.Fatalf("GenerateSlices returned %#v, want no slices", got)
	}
}

func repoFixture(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for name, content := range files {
		file := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(file), 0o750); err != nil {
			t.Fatalf("create fixture directory %s: %v", filepath.Dir(file), err)
		}
		if err := os.WriteFile(file, []byte(content), 0o600); err != nil {
			t.Fatalf("write fixture file %s: %v", file, err)
		}
	}
	return root
}
