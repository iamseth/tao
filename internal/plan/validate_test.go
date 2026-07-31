package plan

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidatePlanVerificationFindsEverySliceCommand(t *testing.T) {
	repo := t.TempDir()
	mkdir(t, filepath.Join(repo, "pkg"))
	detail := &PlanDetail{
		State: State{Repo: Repo{Root: repo}},
		Slices: SlicesFile{Slices: []Slice{
			{ID: "001-a", Verification: Verification{Commands: []string{"go test ./pkg"}}},
			{ID: "002-b", Verification: Verification{Commands: []string{"cd missing && go test ./..."}}},
		}},
	}

	result := ValidatePlanVerification(detail)
	if result.HasErrors() {
		t.Fatalf("expected command semantics to remain advisory, got %+v", result.Findings)
	}
	if len(result.Findings) != 1 || result.Findings[0].Severity != VerificationFindingWarning || result.Findings[0].SliceID != "002-b" || result.Findings[0].Code != "verification_cwd_missing" {
		t.Fatalf("unexpected findings: %+v", result.Findings)
	}
}

func TestValidatePlanVerificationKeepsMissingPathsAsWarnings(t *testing.T) {
	repo := t.TempDir()
	detail := &PlanDetail{
		State:  State{Repo: Repo{Root: repo}},
		Slices: SlicesFile{Slices: []Slice{{ID: "001-a", Verification: Verification{Commands: []string{"pnpm exec vitest missing.test.ts"}}}}},
	}

	result := ValidatePlanVerification(detail)
	if result.HasErrors() {
		t.Fatalf("expected warning-only result, got %+v", result.Findings)
	}
	if len(result.Findings) != 1 || result.Findings[0].Severity != VerificationFindingWarning || result.Findings[0].Code != "verification_path_missing" {
		t.Fatalf("unexpected findings: %+v", result.Findings)
	}
}

func TestValidateSelectedSliceVerificationAllowsSameSliceFutureFile(t *testing.T) {
	repo := t.TempDir()
	detail := &PlanDetail{
		State: State{Status: StatusPlanned, Repo: Repo{Root: repo}, Plan: PlanState{ID: "plan", PendingSlices: []string{"001-a"}}},
		Slices: SlicesFile{Slices: []Slice{{
			ID:            "001-a",
			Status:        StatusPending,
			ExpectedFiles: []string{"missing.test.ts"},
			Verification:  Verification{Commands: []string{"pnpm exec vitest missing.test.ts"}},
		}}},
	}

	result := ValidateSelectedSliceVerification(detail)
	if result.HasErrors() {
		t.Fatalf("expected same-slice future file to stay warning-only, got %+v", result.Findings)
	}
	requireFutureFileWarning(t, result.Findings, "001-a")
}

func TestValidateSelectedSliceVerificationAllowsDependencyFutureFile(t *testing.T) {
	repo := t.TempDir()
	detail := &PlanDetail{
		State: State{Status: StatusPlanned, Repo: Repo{Root: repo}, Plan: PlanState{
			ID:              "plan",
			CompletedSlices: []string{"001-a"},
			PendingSlices:   []string{"002-b"},
		}},
		Slices: SlicesFile{Slices: []Slice{
			{ID: "001-a", Status: StatusCompleted, ExpectedFiles: []string{"pkg/generated.test.ts"}},
			{ID: "002-b", Status: StatusPending, DependsOn: []string{"001-a"}, Verification: Verification{Commands: []string{"pnpm exec vitest pkg/generated.test.ts"}}},
		}},
	}

	result := ValidateSelectedSliceVerification(detail)
	if result.HasErrors() {
		t.Fatalf("expected dependency future file to stay warning-only, got %+v", result.Findings)
	}
	requireFutureFileWarning(t, result.Findings, "002-b")
}

func TestValidateSelectedSliceVerificationLeavesExistingFutureFileUnchanged(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, "pkg", "existing.test.ts"), "")
	detail := &PlanDetail{
		State: State{Status: StatusPlanned, Repo: Repo{Root: repo}, Plan: PlanState{ID: "plan", PendingSlices: []string{"001-a"}}},
		Slices: SlicesFile{Slices: []Slice{{
			ID:            "001-a",
			Status:        StatusPending,
			ExpectedFiles: []string{"pkg/existing.test.ts"},
			Verification:  Verification{Commands: []string{"pnpm exec vitest pkg/existing.test.ts"}},
		}}},
	}

	result := ValidateSelectedSliceVerification(detail)
	if len(result.Findings) != 0 {
		t.Fatalf("expected existing file behavior to be unchanged, got %+v", result.Findings)
	}
}

func TestValidatePlanVerificationAllowsSerialEarlierFutureFile(t *testing.T) {
	repo := t.TempDir()
	detail := &PlanDetail{
		State: State{Repo: Repo{Root: repo}, Plan: PlanState{ID: "plan", PendingSlices: []string{"001-a", "002-b"}}},
		Slices: SlicesFile{Execution: Execution{Mode: "serial"}, Slices: []Slice{
			{ID: "001-a", Status: StatusPending, ExpectedFiles: []string{"shared/future.test.ts"}, Verification: Verification{Commands: []string{"go test ."}}},
			{ID: "002-b", Status: StatusPending, Verification: Verification{Commands: []string{"pnpm exec vitest shared/future.test.ts"}}},
		}},
	}

	result := ValidatePlanVerification(detail)
	if result.HasErrors() {
		t.Fatalf("expected serial earlier future file to stay warning-only, got %+v", result.Findings)
	}
	requireFutureFileWarning(t, result.Findings, "002-b")
}

func TestValidateSelectedSliceVerificationRequiresExactFutureFile(t *testing.T) {
	repo := t.TempDir()
	detail := &PlanDetail{
		State: State{Status: StatusPlanned, Repo: Repo{Root: repo}, Plan: PlanState{ID: "plan", PendingSlices: []string{"001-a"}}},
		Slices: SlicesFile{Slices: []Slice{{
			ID:            "001-a",
			Status:        StatusPending,
			ExpectedFiles: []string{"tests/*.test.ts", "tests/", "tests/..."},
			Verification:  Verification{Commands: []string{"pnpm exec vitest tests/new.test.ts"}},
		}}},
	}

	result := ValidateSelectedSliceVerification(detail)
	if result.HasErrors() {
		t.Fatalf("expected command-derived missing path to remain advisory, got %+v", result.Findings)
	}
	finding := findFindingByCode(result.Findings, "verification_path_missing")
	if finding == nil || finding.Severity != VerificationFindingWarning {
		t.Fatalf("expected missing path warning, got %+v", result.Findings)
	}
	if containsFindingCode(result.Findings, "verification_future_file_missing") {
		t.Fatalf("did not expect future-file allowance for glob or vague expected files, got %+v", result.Findings)
	}
}

func TestValidatePlanVerificationKeepsShellHazardsAsWarnings(t *testing.T) {
	repo := t.TempDir()
	detail := &PlanDetail{
		State:  State{Repo: Repo{Root: repo}},
		Slices: SlicesFile{Slices: []Slice{{ID: "001-a", Verification: Verification{Commands: []string{"go test ./internal/plan -run Test.*Verification"}}}}},
	}

	result := ValidatePlanVerification(detail)
	if result.HasErrors() {
		t.Fatalf("expected warning-only result, got %+v", result.Findings)
	}
	if len(result.Findings) != 1 || result.Findings[0].Severity != VerificationFindingWarning || result.Findings[0].Code != "verification_shell_hazard" {
		t.Fatalf("unexpected findings: %+v", result.Findings)
	}
}

func TestValidateSelectedSliceVerificationKeepsMissingCommandPathAdvisory(t *testing.T) {
	repo := t.TempDir()
	detail := &PlanDetail{
		State:  State{Status: StatusPlanned, Repo: Repo{Root: repo}, Plan: PlanState{ID: "plan", PendingSlices: []string{"001-a"}}},
		Slices: SlicesFile{Slices: []Slice{{ID: "001-a", Status: StatusPending, Verification: Verification{Commands: []string{"pnpm exec vitest missing.test.ts"}}}}},
	}

	result := ValidateSelectedSliceVerification(detail)
	if result.HasErrors() {
		t.Fatalf("expected selected missing command path to remain advisory, got %+v", result.Findings)
	}
	if len(result.Findings) != 1 || result.Findings[0].Severity != VerificationFindingWarning || result.Findings[0].SliceID != "001-a" || result.Findings[0].Code != "verification_path_missing" {
		t.Fatalf("unexpected findings: %+v", result.Findings)
	}
}

func TestValidateSelectedSliceVerificationAtRootUsesOverride(t *testing.T) {
	repo := t.TempDir()
	workspace := t.TempDir()
	writeFile(t, filepath.Join(workspace, "generated_test.go"), "")
	detail := &PlanDetail{
		State:  State{Status: StatusPlanned, Repo: Repo{Root: repo}, Plan: PlanState{ID: "plan", PendingSlices: []string{"001-a"}}},
		Slices: SlicesFile{Slices: []Slice{{ID: "001-a", Status: StatusPending, Verification: Verification{Commands: []string{"gofmt -w generated_test.go"}}}}},
	}

	result := ValidateSelectedSliceVerificationAtRoot(detail, workspace)
	if result.HasErrors() || len(result.Findings) != 0 {
		t.Fatalf("expected override root to satisfy path check, got %+v", result.Findings)
	}
}

func TestValidateSelectedSliceVerificationKeepsShellHazardsAdvisory(t *testing.T) {
	repo := t.TempDir()
	detail := &PlanDetail{
		State:  State{Status: StatusPlanned, Repo: Repo{Root: repo}, Plan: PlanState{ID: "plan", PendingSlices: []string{"001-a"}}},
		Slices: SlicesFile{Slices: []Slice{{ID: "001-a", Status: StatusPending, Verification: Verification{Commands: []string{"go test ./internal/plan -run Test.*Verification"}}}}},
	}

	result := ValidateSelectedSliceVerification(detail)
	if result.HasErrors() {
		t.Fatalf("expected selected shell hazard to remain advisory, got %+v", result.Findings)
	}
	if len(result.Findings) != 1 || result.Findings[0].Severity != VerificationFindingWarning || result.Findings[0].Code != "verification_shell_hazard" {
		t.Fatalf("unexpected findings: %+v", result.Findings)
	}
}

func TestValidateSelectedSliceVerificationSuggestsPackageRelativePath(t *testing.T) {
	repo := t.TempDir()
	serviceDir := filepath.Join(repo, "services", "api")
	writeFile(t, filepath.Join(serviceDir, "package.json"), `{"name":"@repo/api"}`)
	writeFile(t, filepath.Join(serviceDir, "index.test.ts"), "")
	detail := &PlanDetail{
		State:  State{Status: StatusPlanned, Repo: Repo{Root: repo}, Plan: PlanState{ID: "plan", PendingSlices: []string{"001-a"}}},
		Slices: SlicesFile{Slices: []Slice{{ID: "001-a", Status: StatusPending, Verification: Verification{Commands: []string{"pnpm --filter @repo/api exec vitest services/api/index.test.ts"}}}}},
	}

	result := ValidateSelectedSliceVerification(detail)
	if result.HasErrors() {
		t.Fatalf("expected selected package-cwd path mismatch to remain advisory, got %+v", result.Findings)
	}
	if len(result.Findings) != 1 || result.Findings[0].Severity != VerificationFindingWarning || result.Findings[0].Suggestion != "index.test.ts" {
		t.Fatalf("expected package-relative suggestion, got %+v", result.Findings)
	}
}

func TestValidateSelectedSliceVerificationReportsRunnableError(t *testing.T) {
	repo := t.TempDir()
	detail := &PlanDetail{
		State:  State{Status: StatusPlanned, Repo: Repo{Root: repo}, Plan: PlanState{ID: "plan", PendingSlices: []string{"002-b"}}},
		Slices: SlicesFile{Slices: []Slice{{ID: "002-b", Status: StatusPending, DependsOn: []string{"001-a"}}}},
	}

	result := ValidateSelectedSliceVerification(detail)
	if !result.HasErrors() {
		t.Fatalf("expected dependency to block selected validation, got %+v", result.Findings)
	}
	if len(result.Findings) != 1 || result.Findings[0].Code != "verification_slice_not_runnable" {
		t.Fatalf("unexpected findings: %+v", result.Findings)
	}
}

func TestValidatePlanVerificationWarnsForOversizedSliceGuardrails(t *testing.T) {
	detail := &PlanDetail{
		State: State{Repo: Repo{Root: t.TempDir()}},
		Slices: SlicesFile{Slices: []Slice{{
			ID:            "001-a",
			Tasks:         []string{"1", "2", "3", "4", "5", "6", "7", "8", "9"},
			ExpectedFiles: []string{"internal/..."},
			Verification:  Verification{Commands: []string{"go test ./...", "pnpm run test"}},
		}}},
	}

	result := ValidatePlanVerification(detail)
	if result.HasErrors() {
		t.Fatalf("expected guardrails to be warning-only, got %+v", result.Findings)
	}
	for _, want := range []string{"slice_task_count", "slice_expected_file_vague", "slice_verification_broad"} {
		if !containsFindingCode(result.Findings, want) {
			t.Fatalf("expected finding %q, got %+v", want, result.Findings)
		}
	}
}

func TestValidatePlanVerificationWarnsForUnsafeExpectedFiles(t *testing.T) {
	detail := &PlanDetail{
		State: State{Repo: Repo{Root: t.TempDir()}},
		Slices: SlicesFile{Slices: []Slice{{
			ID:            "001-a",
			ExpectedFiles: []string{"/tmp/outside.go", "../secret.txt", `C:\\outside.go`},
			Verification:  Verification{Commands: []string{"go test ./internal/plan"}},
		}}},
	}

	result := ValidatePlanVerification(detail)
	if result.HasErrors() {
		t.Fatalf("expected unsafe expected files to be warning-only, got %+v", result.Findings)
	}
	if got := countFindingCode(result.Findings, "slice_expected_file_unsafe"); got != 3 {
		t.Fatalf("expected three unsafe expected file warnings, got %d findings: %+v", got, result.Findings)
	}
}

func TestValidatePlanVerificationWarnsForMissingVerificationCommands(t *testing.T) {
	detail := &PlanDetail{
		State:  State{Repo: Repo{Root: t.TempDir()}},
		Slices: SlicesFile{Slices: []Slice{{ID: "001-a"}}},
	}

	result := ValidatePlanVerification(detail)
	if result.HasErrors() {
		t.Fatalf("expected missing verification to be warning-only for full-plan validation, got %+v", result.Findings)
	}
	if len(result.Findings) != 1 || result.Findings[0].Severity != VerificationFindingWarning || result.Findings[0].Code != "slice_verification_missing" {
		t.Fatalf("unexpected findings: %+v", result.Findings)
	}
}

func TestValidateSelectedSliceVerificationWarnsForSelectedGuardrailsOnly(t *testing.T) {
	detail := &PlanDetail{
		State: State{Status: StatusPlanned, Repo: Repo{Root: t.TempDir()}, Plan: PlanState{ID: "plan", PendingSlices: []string{"001-a", "002-b"}}},
		Slices: SlicesFile{Slices: []Slice{
			{ID: "001-a", Status: StatusPending, ExpectedFiles: []string{"*"}, Verification: Verification{Commands: []string{"go test ./internal/plan"}}},
			{ID: "002-b", Status: StatusPending, ExpectedFiles: []string{"internal/..."}, Verification: Verification{Commands: []string{"go test ./internal/plan"}}},
		}},
	}

	result := ValidateSelectedSliceVerification(detail)
	if result.HasErrors() {
		t.Fatalf("expected selected guardrails to be warning-only, got %+v", result.Findings)
	}
	if len(result.Findings) != 1 || result.Findings[0].SliceID != "001-a" || result.Findings[0].Code != "slice_expected_file_vague" {
		t.Fatalf("expected only selected slice guardrail, got %+v", result.Findings)
	}
}

func TestValidateSelectedSliceVerificationBlocksMissingVerificationCommands(t *testing.T) {
	detail := &PlanDetail{
		State:  State{Status: StatusPlanned, Repo: Repo{Root: t.TempDir()}, Plan: PlanState{ID: "plan", PendingSlices: []string{"001-a"}}},
		Slices: SlicesFile{Slices: []Slice{{ID: "001-a", Status: StatusPending}}},
	}

	result := ValidateSelectedSliceVerification(detail)
	if !result.HasErrors() {
		t.Fatalf("expected selected missing verification to block, got %+v", result.Findings)
	}
	if len(result.Findings) != 1 || result.Findings[0].Severity != VerificationFindingError || result.Findings[0].Code != "slice_verification_missing" {
		t.Fatalf("unexpected findings: %+v", result.Findings)
	}
}

func TestValidateRequiredInputDeclarationsAndKinds(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, "input.txt"), "input")
	mkdir(t, filepath.Join(repo, "input-dir"))

	tests := []struct {
		name  string
		input RequiredInput
		code  string
	}{
		{name: "empty path", input: RequiredInput{Kind: RequiredInputFile, Reason: "needed"}, code: "required_input_path_invalid"},
		{name: "absolute path", input: RequiredInput{Path: "/tmp/input", Kind: RequiredInputFile, Reason: "needed"}, code: "required_input_path_invalid"},
		{name: "windows absolute path", input: RequiredInput{Path: `C:\\input.txt`, Kind: RequiredInputFile, Reason: "needed"}, code: "required_input_path_invalid"},
		{name: "parent traversal", input: RequiredInput{Path: "../input.txt", Kind: RequiredInputFile, Reason: "needed"}, code: "required_input_path_invalid"},
		{name: "wildcard", input: RequiredInput{Path: "*.txt", Kind: RequiredInputFile, Reason: "needed"}, code: "required_input_path_invalid"},
		{name: "vague", input: RequiredInput{Path: "input-dir/", Kind: RequiredInputDirectory, Reason: "needed"}, code: "required_input_path_invalid"},
		{name: "unsafe control character", input: RequiredInput{Path: "input\n.txt", Kind: RequiredInputFile, Reason: "needed"}, code: "required_input_path_invalid"},
		{name: "malformed kind", input: RequiredInput{Path: "input.txt", Kind: "dir", Reason: "needed"}, code: "required_input_kind_invalid"},
		{name: "missing reason", input: RequiredInput{Path: "input.txt", Kind: RequiredInputFile}, code: "required_input_reason_missing"},
		{name: "file declared as directory", input: RequiredInput{Path: "input.txt", Kind: RequiredInputDirectory, Reason: "needed"}, code: "required_input_wrong_kind"},
		{name: "directory declared as file", input: RequiredInput{Path: "input-dir", Kind: RequiredInputFile, Reason: "needed"}, code: "required_input_wrong_kind"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			detail := &PlanDetail{
				State: State{Repo: Repo{Root: repo}},
				Slices: SlicesFile{Slices: []Slice{{
					ID:             "001-a",
					RequiredInputs: []RequiredInput{tt.input},
					Verification:   Verification{Commands: []string{"go test ./internal/plan"}},
				}}},
			}
			result := ValidatePlanVerification(detail)
			if !result.HasErrors() || !containsFindingCode(result.Findings, tt.code) {
				t.Fatalf("expected %s error, got %+v", tt.code, result.Findings)
			}
		})
	}

	detail := &PlanDetail{
		State: State{Repo: Repo{Root: repo}},
		Slices: SlicesFile{Slices: []Slice{{
			ID: "001-a",
			RequiredInputs: []RequiredInput{
				{Path: "input.txt", Kind: RequiredInputFile, Reason: "needed"},
				{Path: "input-dir", Kind: RequiredInputDirectory, Reason: "needed"},
			},
			Verification: Verification{Commands: []string{"go test ./internal/plan"}},
		}}},
	}
	if result := ValidatePlanVerification(detail); result.HasErrors() || len(result.Findings) != 0 {
		t.Fatalf("expected valid existing inputs, got %+v", result.Findings)
	}
}

func TestValidatePlanRequiredInputAllowsOnlyExactDirectProducer(t *testing.T) {
	repo := t.TempDir()
	verification := Verification{Commands: []string{"go test ./internal/plan"}}
	tests := []struct {
		name        string
		execution   Execution
		consumerDep []string
		slices      []Slice
		wantFuture  bool
	}{
		{
			name:        "exact normalized direct dependency",
			consumerDep: []string{"001-source"},
			slices:      []Slice{{ID: "001-source", ExpectedFiles: []string{"./generated/file.txt"}, Verification: verification}},
			wantFuture:  true,
		},
		{
			name:      "serial only",
			execution: Execution{Mode: "serial"},
			slices:    []Slice{{ID: "001-source", ExpectedFiles: []string{"generated/file.txt"}, Verification: verification}},
		},
		{
			name:   "unrelated producer",
			slices: []Slice{{ID: "001-source", ExpectedFiles: []string{"generated/file.txt"}, Verification: verification}},
		},
		{
			name:        "wildcard producer",
			consumerDep: []string{"001-source"},
			slices:      []Slice{{ID: "001-source", ExpectedFiles: []string{"generated/*.txt"}, Verification: verification}},
		},
		{
			name:        "prefix producer",
			consumerDep: []string{"001-source"},
			slices:      []Slice{{ID: "001-source", ExpectedFiles: []string{"generated"}, Verification: verification}},
		},
		{
			name:        "near match producer",
			consumerDep: []string{"001-source"},
			slices:      []Slice{{ID: "001-source", ExpectedFiles: []string{"generated/file.txt.bak"}, Verification: verification}},
		},
		{
			name:        "transitive producer",
			consumerDep: []string{"002-middle"},
			slices: []Slice{
				{ID: "001-source", ExpectedFiles: []string{"generated/file.txt"}, Verification: verification},
				{ID: "002-middle", DependsOn: []string{"001-source"}, Verification: verification},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			slices := append([]Slice(nil), tt.slices...)
			slices = append(slices, Slice{
				ID:             "003-consumer",
				DependsOn:      tt.consumerDep,
				RequiredInputs: []RequiredInput{{Path: "generated//file.txt", Kind: RequiredInputFile, Reason: "generated contract"}},
				Verification:   verification,
			})
			detail := &PlanDetail{State: State{Repo: Repo{Root: repo}}, Slices: SlicesFile{Execution: tt.execution, Slices: slices}}
			result := ValidatePlanVerification(detail)
			future := findFindingByCode(result.Findings, "required_input_future")
			missing := findFindingByCode(result.Findings, "required_input_missing")
			if tt.wantFuture {
				if result.HasErrors() || future == nil || future.Severity != VerificationFindingWarning || missing != nil {
					t.Fatalf("expected exact direct producer warning, got %+v", result.Findings)
				}
				return
			}
			if !result.HasErrors() || missing == nil || missing.Severity != VerificationFindingError || future != nil {
				t.Fatalf("expected missing input error, got %+v", result.Findings)
			}
		})
	}
}

func TestValidateSelectedRequiredInputUsesOverrideAndRequiresExistence(t *testing.T) {
	repo := t.TempDir()
	workspace := t.TempDir()
	writeFile(t, filepath.Join(repo, "generated", "input.txt"), "control checkout only")
	detail := &PlanDetail{
		State: State{Status: StatusPlanned, Repo: Repo{Root: repo}, Plan: PlanState{
			ID:              "plan",
			CompletedSlices: []string{"001-source"},
			PendingSlices:   []string{"002-consumer"},
		}},
		Slices: SlicesFile{Slices: []Slice{
			{ID: "001-source", Status: StatusCompleted, ExpectedFiles: []string{"generated/input.txt"}},
			{
				ID:             "002-consumer",
				Status:         StatusPending,
				DependsOn:      []string{"001-source"},
				RequiredInputs: []RequiredInput{{Path: "generated/input.txt", Kind: RequiredInputFile, Reason: "generated contract"}},
				Verification:   Verification{Commands: []string{"go test ./internal/plan"}},
			},
		}},
	}

	result := ValidateSelectedSliceVerificationAtRoot(detail, workspace)
	if !result.HasErrors() || findFindingByCode(result.Findings, "required_input_missing") == nil || containsFindingCode(result.Findings, "required_input_future") {
		t.Fatalf("expected missing prepared-worktree input to block despite producer promise, got %+v", result.Findings)
	}
	writeFile(t, filepath.Join(workspace, "generated", "input.txt"), "prepared input")
	result = ValidateSelectedSliceVerificationAtRoot(detail, workspace)
	if result.HasErrors() || containsFindingCode(result.Findings, "required_input_missing") {
		t.Fatalf("expected prepared-worktree input to satisfy contract, got %+v", result.Findings)
	}
}

func TestValidateSelectedSliceVerificationBlocksBlankCommandLists(t *testing.T) {
	for _, commands := range [][]string{nil, {}, {"", "  ", "\t"}} {
		detail := &PlanDetail{
			State:  State{Status: StatusPlanned, Repo: Repo{Root: t.TempDir()}, Plan: PlanState{ID: "plan", PendingSlices: []string{"001-a"}}},
			Slices: SlicesFile{Slices: []Slice{{ID: "001-a", Status: StatusPending, Verification: Verification{Commands: commands}}}},
		}
		result := ValidateSelectedSliceVerification(detail)
		if !result.HasErrors() || findFindingByCode(result.Findings, "slice_verification_missing") == nil {
			t.Fatalf("expected blank verification structure to block, commands=%q findings=%+v", commands, result.Findings)
		}
	}
}

func TestValidateDetailAllowsCanonicalCurrentSliceStatuses(t *testing.T) {
	for _, status := range []string{StatusPending, StatusInProgress, StatusBlocked} {
		t.Run(status, func(t *testing.T) {
			current := "001-a"
			detail := &PlanDetail{
				State:         State{Plan: PlanState{ID: "plan", CurrentSlice: &current, PendingSlices: []string{current}}},
				Slices:        SlicesFile{PlanID: "plan", Slices: []Slice{{ID: current, Status: status}}},
				PlanningBrief: PlanningBriefArtifact{Content: completePlanningBriefMarkdown()},
			}

			if warnings := ValidateDetail(detail); len(warnings) != 0 {
				t.Fatalf("expected canonical current %s slice to have no warnings, got %v", status, warnings)
			}
		})
	}
}

func TestValidateDetailWarnsForBlockedCurrentSliceMissingFromPendingQueue(t *testing.T) {
	current := "001-a"
	detail := &PlanDetail{
		State: State{Plan: PlanState{ID: "plan", CurrentSlice: &current, PendingSlices: []string{"002-b"}}},
		Slices: SlicesFile{PlanID: "plan", Slices: []Slice{
			{ID: current, Status: StatusBlocked},
			{ID: "002-b", Status: StatusPending},
		}},
		PlanningBrief: PlanningBriefArtifact{Content: completePlanningBriefMarkdown()},
	}

	warnings := ValidateDetail(detail)
	if !containsWarning(warnings, "current_slice references blocked slice 001-a missing from pending_slices") {
		t.Fatalf("expected missing blocked current slice warning, got %v", warnings)
	}
}

func TestValidateDetailWarnsForNonCurrentActivePendingEntries(t *testing.T) {
	for _, status := range []string{StatusInProgress, StatusBlocked} {
		t.Run(status, func(t *testing.T) {
			current := "001-a"
			detail := &PlanDetail{
				State: State{Plan: PlanState{ID: "plan", CurrentSlice: &current, PendingSlices: []string{current, "002-b"}}},
				Slices: SlicesFile{PlanID: "plan", Slices: []Slice{
					{ID: current, Status: StatusPending},
					{ID: "002-b", Status: status},
				}},
				PlanningBrief: PlanningBriefArtifact{Content: completePlanningBriefMarkdown()},
			}

			warnings := ValidateDetail(detail)
			want := "pending_slices references " + status + " slice 002-b"
			if !containsWarning(warnings, want) {
				t.Fatalf("expected warning %q, got %v", want, warnings)
			}
		})
	}
}

func TestValidateDetailStillWarnsForMalformedQueueWithCurrentBlockedSlice(t *testing.T) {
	current := "001-a"
	detail := &PlanDetail{
		State: State{Plan: PlanState{ID: "plan", CurrentSlice: &current, PendingSlices: []string{current, "002-b", "002-b"}}},
		Slices: SlicesFile{PlanID: "plan", Slices: []Slice{
			{ID: current, Status: StatusBlocked, DependsOn: []string{"002-b"}},
			{ID: "002-b", Status: StatusPending},
		}},
		PlanningBrief: PlanningBriefArtifact{Content: completePlanningBriefMarkdown()},
	}

	warnings := ValidateDetail(detail)
	for _, want := range []string{
		"pending_slices contains duplicate slice 002-b",
		"pending_slices orders slice 001-a before dependency 002-b",
	} {
		if !containsWarning(warnings, want) {
			t.Fatalf("expected warning %q, got %v", want, warnings)
		}
	}
}

func TestValidateDetailWarnsForStaleCompletedCurrentSliceRecovery(t *testing.T) {
	current := "001-a"
	detail := &PlanDetail{
		State: State{Plan: PlanState{ID: "plan", CurrentSlice: &current, PendingSlices: []string{"002-b"}, CompletedSlices: []string{"001-a"}}},
		Slices: SlicesFile{PlanID: "plan", Slices: []Slice{
			{ID: "001-a", Status: StatusCompleted},
			{ID: "002-b", Status: StatusPending},
		}},
		PlanningBrief: PlanningBriefArtifact{Content: completePlanningBriefMarkdown()},
	}

	warnings := ValidateDetail(detail)
	if !containsWarning(warnings, "current_slice references a completed slice") {
		t.Fatalf("expected stale current_slice warning, got %v", warnings)
	}
}

func TestValidateDetailWarnsForActiveEmptyPendingPlan(t *testing.T) {
	current := "001-a"
	detail := &PlanDetail{
		State:         State{Status: StatusInProgress, Plan: PlanState{ID: "plan", CurrentSlice: &current}},
		Slices:        SlicesFile{PlanID: "plan", Slices: []Slice{{ID: "001-a", Status: StatusPending}}},
		PlanningBrief: PlanningBriefArtifact{Content: completePlanningBriefMarkdown()},
	}

	warnings := ValidateDetail(detail)
	if !containsWarning(warnings, "pending_slices is empty") {
		t.Fatalf("expected active empty-pending warning, got %v", warnings)
	}
}

func TestValidateDetailWarnsForEditedQueueInconsistencies(t *testing.T) {
	current := "003-c"
	detail := &PlanDetail{
		State: State{Plan: PlanState{
			ID:              "plan",
			CurrentSlice:    &current,
			PendingSlices:   []string{"001-a", "001-a", "002-b"},
			CompletedSlices: []string{"003-c"},
		}},
		Slices: SlicesFile{PlanID: "plan", Slices: []Slice{
			{ID: "001-a", Status: StatusPending},
			{ID: "002-b", Status: StatusSkipped},
			{ID: "003-c", Status: StatusSkipped},
		}},
		PlanningBrief: PlanningBriefArtifact{Content: completePlanningBriefMarkdown()},
	}

	warnings := ValidateDetail(detail)
	for _, want := range []string{
		"current_slice references skipped slice 003-c",
		"completed_slices references skipped slice 003-c",
		"pending_slices contains duplicate slice 001-a",
		"pending_slices references skipped slice 002-b",
	} {
		if !containsWarning(warnings, want) {
			t.Fatalf("expected warning %q, got %v", want, warnings)
		}
	}
}

func TestSliceTagsRemainOptionalForExistingPlans(t *testing.T) {
	var slices SlicesFile
	if err := json.Unmarshal([]byte(`{"schema":"tao.plan.slices.v1","plan_id":"plan","execution":{"mode":"serial","parallel_safe":false},"slices":[{"id":"001-a","title":"A","status":"pending","depends_on":[],"timing":{"created_at":"2026-05-03T23:00:00Z","started_at":null,"completed_at":null,"updated_at":"2026-05-03T23:00:00Z","last_activity_at":null,"duration_seconds":null},"goal":"","context":"","tasks":[],"expected_files":[],"verification":{"commands":[],"manual_checks":[]}}]}`), &slices); err != nil {
		t.Fatal(err)
	}

	if slices.Slices[0].Tags != nil {
		t.Fatalf("expected omitted tags to remain nil, got %#v", slices.Slices[0].Tags)
	}
	warnings := ValidateDetail(&PlanDetail{
		State:         State{Plan: PlanState{ID: "plan", PendingSlices: []string{"001-a"}}},
		Slices:        slices,
		PlanningBrief: PlanningBriefArtifact{Content: completePlanningBriefMarkdown()},
	})
	if len(warnings) != 0 {
		t.Fatalf("expected no warnings for omitted tags, got %v", warnings)
	}
}

func TestValidateDetailAllowsMissingWorkspaceMetadata(t *testing.T) {
	detail := &PlanDetail{
		State:         State{Plan: PlanState{ID: "plan", PendingSlices: []string{"001-a"}}},
		Slices:        SlicesFile{PlanID: "plan", Slices: []Slice{{ID: "001-a", Status: StatusPending}}},
		PlanningBrief: PlanningBriefArtifact{Content: completePlanningBriefMarkdown()},
	}

	warnings := ValidateDetail(detail)
	if len(warnings) != 0 {
		t.Fatalf("expected no warnings for missing workspace metadata, got %v", warnings)
	}
}

func TestValidateDetailWarnsForInvalidWorkspaceMetadata(t *testing.T) {
	detail := &PlanDetail{
		State: State{
			Plan: PlanState{ID: "plan", PendingSlices: []string{"001-a"}},
			Workspace: &Workspace{
				Strategy:              "shared",
				LifecycleStatus:       "lost",
				DependencyPreparation: "maybe",
				CleanupStatus:         "deleted",
			},
		},
		Slices:        SlicesFile{PlanID: "plan", Slices: []Slice{{ID: "001-a", Status: StatusPending}}},
		PlanningBrief: PlanningBriefArtifact{Content: completePlanningBriefMarkdown()},
	}

	warnings := ValidateDetail(detail)
	for _, want := range []string{"workspace.strategy", "workspace.lifecycle_status", "workspace.dependency_preparation_status", "workspace.cleanup_status"} {
		if !containsWarning(warnings, want) {
			t.Fatalf("expected warning %q, got %v", want, warnings)
		}
	}
}

func containsWarning(warnings []string, want string) bool {
	for _, warning := range warnings {
		if strings.Contains(warning, want) {
			return true
		}
	}
	return false
}

func containsFindingCode(findings []VerificationFinding, want string) bool {
	return findFindingByCode(findings, want) != nil
}

func countFindingCode(findings []VerificationFinding, want string) int {
	count := 0
	for _, finding := range findings {
		if finding.Code == want {
			count++
		}
	}
	return count
}

func findFindingByCode(findings []VerificationFinding, want string) *VerificationFinding {
	for i := range findings {
		if findings[i].Code == want {
			return &findings[i]
		}
	}
	return nil
}

func requireFutureFileWarning(t *testing.T, findings []VerificationFinding, sliceID string) {
	t.Helper()
	finding := findFindingByCode(findings, "verification_future_file_missing")
	if finding == nil {
		t.Fatalf("expected future-file warning, got %+v", findings)
	}
	if finding.Severity != VerificationFindingWarning || finding.SliceID != sliceID || !strings.Contains(finding.Message, "future file") {
		t.Fatalf("unexpected future-file finding: %+v", finding)
	}
}

func completePlanningBriefMarkdown() string {
	return `# User Goal
# Constraints
# Non-goals
# Expected Files/Packages
# Validation Strategy
# Open Questions
`
}
