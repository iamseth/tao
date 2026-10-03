package plan

import (
	"context"
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestValidatePlanVerificationSliceTiming(t *testing.T) {
	for _, evidence := range []bool{false, true} {
		t.Run(map[bool]string{false: "without evidence", true: "matching evidence"}[evidence], func(t *testing.T) {
			current := "001-a"
			detail := &PlanDetail{
				State: State{Status: StatusInProgress, Repo: Repo{Root: t.TempDir()}, Plan: PlanState{ID: "plan", CurrentSlice: &current, PendingSlices: []string{current, "002-b"}}},
				Slices: SlicesFile{PlanID: "plan", Slices: []Slice{
					{ID: current, Status: StatusInProgress, Verification: Verification{Commands: []string{"go version"}}},
					{ID: "002-b", Status: StatusInProgress, Verification: Verification{Commands: []string{"go version"}}},
					{ID: "003-pending", Status: StatusPending, Verification: Verification{Commands: []string{"go version"}}},
				}},
			}
			if evidence {
				detail.Events = []Event{{Type: EventTypeSliceStarted, PlanID: "plan", SliceID: current, Timestamp: time.Date(2026, 10, 1, 3, 0, 0, 0, time.UTC)}}
			}
			before, err := json.Marshal(detail)
			if err != nil {
				t.Fatal(err)
			}
			result := ValidatePlanVerification(detail)
			var ids []string
			for _, finding := range result.Findings {
				if finding.Code == "slice_started_at_missing" {
					ids = append(ids, finding.SliceID)
					if finding.Severity != VerificationFindingError || !strings.Contains(finding.Message, "slice_started") {
						t.Fatalf("unexpected timing finding: %+v", finding)
					}
				}
			}
			if !result.HasErrors() || !reflect.DeepEqual(ids, []string{current, "002-b"}) {
				t.Fatalf("unexpected findings: %+v", result.Findings)
			}
			selected := ValidateSelectedSliceVerificationAtRoot(detail, "")
			if selected.HasErrors() || containsFindingCode(selected.Findings, "slice_started_at_missing") {
				t.Fatalf("selected validation changed: %+v", selected.Findings)
			}
			after, err := json.Marshal(detail)
			if err != nil {
				t.Fatal(err)
			}
			if string(before) != string(after) {
				t.Fatal("validation mutated detail")
			}
			for i := range detail.Slices.Slices {
				detail.Slices.Slices[i].Status = StatusPending
			}
			if result := ValidatePlanVerification(detail); result.HasErrors() {
				t.Fatalf("pending defaults invalid: %+v", result.Findings)
			}
			started := time.Now()
			detail.Slices.Slices[0].Status = StatusInProgress
			detail.Slices.Slices[0].Timing.StartedAt = &started
			if result := ValidatePlanVerification(detail); result.HasErrors() {
				t.Fatalf("present start invalid: %+v", result.Findings)
			}
		})
	}
}

func TestValidatePlanVerificationApprovalContractBoundary(t *testing.T) {
	root := t.TempDir()
	writeEditPlan(t, root)
	repo := NewFileRepository(root)
	detail, err := repo.ResolvePlan(context.Background(), "edit")
	if err != nil {
		t.Fatal(err)
	}
	beforeWarnings := append([]string(nil), detail.Warnings...)
	slice := &detail.Slices.Slices[0]
	slice.Goal = "The user's mGBA check is supplied through this slice's approval."
	slice.Approval = &Approval{Required: true, Approved: true}
	slice.Verification.Commands = []string{"go version"}
	if err := repo.writeSlices(detail.Dir, detail.Slices); err != nil {
		t.Fatal(err)
	}
	// Historical artifacts remain loadable, without new load-time warnings.
	detail, err = repo.ResolvePlan(context.Background(), "edit")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(beforeWarnings, detail.Warnings) {
		t.Fatalf("load warnings changed: before=%v after=%v", beforeWarnings, detail.Warnings)
	}
	result := ValidatePlanVerification(detail)
	finding := findFindingByCode(result.Findings, "approval_factual_payload")
	if !result.HasErrors() || finding == nil {
		t.Fatalf("expected plan-wide approval contract error, got %+v", result.Findings)
	}
	if finding.Severity != VerificationFindingError || finding.SliceID != slice.ID || !strings.Contains(finding.Message, "goal") {
		t.Fatalf("unexpected owned finding: %+v", finding)
	}
	selected := ValidateSelectedSliceVerificationAtRoot(detail, root)
	if selected.HasErrors() || containsFindingCode(selected.Findings, "approval_factual_payload") {
		t.Fatalf("selected-slice runtime validation changed: %+v", selected.Findings)
	}
}

func TestValidatePlanVerificationCheckoutConfinementBoundary(t *testing.T) {
	root := t.TempDir()
	writeEditPlan(t, root)
	repo := NewFileRepository(root)
	detail, err := repo.ResolvePlan(context.Background(), "edit")
	if err != nil {
		t.Fatal(err)
	}
	slice := &detail.Slices.Slices[0]
	slice.Tasks = []string{"Run git branch -d in the owning checkout"}
	slice.Verification.Commands = []string{"go version"}
	result := ValidatePlanVerification(detail)
	finding := findFindingByCode(result.Findings, "slice_foreign_checkout_reference")
	if result.HasErrors() || finding == nil {
		t.Fatalf("expected plan-wide checkout warning, got %+v", result.Findings)
	}
	if finding.Severity != VerificationFindingWarning || finding.SliceID != slice.ID || !strings.Contains(finding.Message, "tasks[0]") {
		t.Fatalf("unexpected owned finding: %+v", finding)
	}
	selected := ValidateSelectedSliceVerificationAtRoot(detail, root)
	if selected.HasErrors() || containsFindingCode(selected.Findings, "slice_foreign_checkout_reference") {
		t.Fatalf("selected-slice runtime validation changed: %+v", selected.Findings)
	}
}

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

	result := ValidateSelectedSliceVerificationAtRoot(detail, "")
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

	result := ValidateSelectedSliceVerificationAtRoot(detail, "")
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

	result := ValidateSelectedSliceVerificationAtRoot(detail, "")
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

	result := ValidateSelectedSliceVerificationAtRoot(detail, "")
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

	result := ValidateSelectedSliceVerificationAtRoot(detail, "")
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

	result := ValidateSelectedSliceVerificationAtRoot(detail, "")
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

	result := ValidateSelectedSliceVerificationAtRoot(detail, "")
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

	result := ValidateSelectedSliceVerificationAtRoot(detail, "")
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

func TestValidatePlanVerificationGateParity(t *testing.T) {
	for _, tt := range []struct {
		name        string
		files       []string
		lint        string
		gate        string
		makefile    string
		missingRoot bool
		wantWarning bool
	}{
		{name: "dedupe helpers", gate: "make verify", wantWarning: true},
		{name: "golangci lint", gate: "make verify", lint: "golangci-lint run --allow-parallel-runners ./internal/a/..."},
		{name: "make lint", gate: "make verify", lint: "make lint"},
		{name: "go vet", gate: "make verify", lint: "go vet ./internal/a/..."},
		{name: "no gate", gate: "go test ./internal/a"},
		{name: "non Go", files: []string{"README.md", "web/app.ts"}, gate: "make verify"},
		{name: "no expected files", files: []string{}, gate: "make verify"},
		{name: "lint gate", gate: "make lint", wantWarning: true},
		{name: "compound gate", gate: "go version && make verify", wantWarning: true},
		{name: "literal tokens", gate: "make verify-extra"},
		{name: "detected gate", gate: "make build && make test", makefile: "build:\n\ntest:\n", wantWarning: true},
		{name: "unavailable root", gate: "make verify", missingRoot: true, wantWarning: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			if tt.makefile != "" {
				writeFile(t, filepath.Join(root, "Makefile"), tt.makefile)
			}
			if tt.missingRoot {
				root = filepath.Join(root, "missing")
			}
			files := tt.files
			if files == nil {
				files = []string{"internal/z/helper.go", "internal/a/helper_test.go", "internal/a/helper.go"}
			}
			commands := []string{"go test ./internal/a"}
			if tt.lint != "" {
				commands = append(commands, tt.lint)
			}
			detail := &PlanDetail{
				State: State{Repo: Repo{Root: root}, Plan: PlanState{PendingSlices: []string{"001-helpers", "002-gate"}}},
				Slices: SlicesFile{Slices: []Slice{
					{ID: "001-helpers", ExpectedFiles: files, Verification: Verification{Commands: commands}},
					{ID: "002-gate", Verification: Verification{Commands: []string{tt.gate}}},
				}},
			}
			result := ValidatePlanVerification(detail)
			if result.HasErrors() {
				t.Fatalf("gate parity must remain advisory: %+v", result.Findings)
			}
			var parity []VerificationFinding
			for _, finding := range result.Findings {
				if finding.Code == "gate_parity" {
					parity = append(parity, finding)
				}
			}
			if !tt.wantWarning {
				if len(parity) != 0 {
					t.Fatalf("unexpected gate parity findings: %+v", parity)
				}
				return
			}
			want := "slice 001-helpers changes Go files in internal/a, internal/z without a lint command while slice 002-gate declares the repository gate " + tt.gate
			if len(parity) != 1 || parity[0].Severity != VerificationFindingWarning || parity[0].SliceID != "001-helpers" || parity[0].Message != want {
				t.Fatalf("expected one advisory %q, got %+v", want, parity)
			}
		})
	}
}

func TestValidatePlanVerificationGateParityOrder(t *testing.T) {
	for _, tt := range []struct {
		name    string
		pending []string
		want    []string
		gate    string
	}{
		{name: "pending order", pending: []string{"004-work", "003-work", "002-gate", "001-gate"}, want: []string{"004-work", "003-work"}, gate: "002-gate"},
		{name: "ID fallback", want: []string{"003-work", "004-work"}, gate: "005-gate"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			detail := &PlanDetail{
				State: State{Repo: Repo{Root: t.TempDir()}, Plan: PlanState{PendingSlices: tt.pending}},
				Slices: SlicesFile{Slices: []Slice{
					{ID: "006-work", ExpectedFiles: []string{"later.go"}, Verification: Verification{Commands: []string{"go version"}}},
					{ID: "005-gate", ExpectedFiles: []string{"gate.go"}, Verification: Verification{Commands: []string{"make verify"}}},
					{ID: "004-work", ExpectedFiles: []string{"root_test.go"}, Verification: Verification{Commands: []string{"go version"}}},
					{ID: "003-work", ExpectedFiles: []string{"root.go"}, Verification: Verification{Commands: []string{"go version"}}},
				}},
			}
			if len(tt.pending) > 0 {
				for _, id := range []string{"001-gate", "002-gate"} {
					detail.Slices.Slices = append(detail.Slices.Slices, Slice{ID: id, Verification: Verification{Commands: []string{"make verify"}}})
				}
			}
			var got []string
			for _, finding := range ValidatePlanVerification(detail).Findings {
				if finding.Code != "gate_parity" {
					continue
				}
				got = append(got, finding.SliceID)
				want := "slice " + finding.SliceID + " changes Go files in . without a lint command while slice " + tt.gate + " declares the repository gate make verify"
				if finding.Message != want {
					t.Fatalf("expected %q, got %q", want, finding.Message)
				}
			}
			if strings.Join(got, ",") != strings.Join(tt.want, ",") {
				t.Fatalf("expected findings in order %v, got %v", tt.want, got)
			}
		})
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

	result := ValidateSelectedSliceVerificationAtRoot(detail, "")
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

	result := ValidateSelectedSliceVerificationAtRoot(detail, "")
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
		result := ValidateSelectedSliceVerificationAtRoot(detail, "")
		if !result.HasErrors() || findFindingByCode(result.Findings, "slice_verification_missing") == nil {
			t.Fatalf("expected blank verification structure to block, commands=%q findings=%+v", commands, result.Findings)
		}
	}
}

func TestValidateDetailAcceptsAbandonedOverrideWithUnfinishedSlices(t *testing.T) {
	at := time.Date(2026, 9, 1, 16, 0, 0, 0, time.UTC)
	detail := &PlanDetail{
		State: State{Status: StatusAbandoned, Plan: PlanState{
			ID: "plan", CurrentSlice: ptrString("001-a"), PendingSlices: []string{"001-a", "002-b"},
		}},
		Slices: SlicesFile{PlanID: "plan", Slices: []Slice{
			{ID: "001-a", Status: StatusInProgress},
			{ID: "002-b", Status: StatusPending},
		}},
		Events: []Event{{Type: EventTypePlanAbandoned, Timestamp: at, Reason: "No longer needed"}},
	}
	warnings := validateDetail(detail)
	for _, unwanted := range []string{"active lifecycle metadata", "current_slice references", "pending_slices references", "plan_abandoned"} {
		if containsWarning(warnings, unwanted) {
			t.Fatalf("abandoned unfinished state warning containing %q: %v", unwanted, warnings)
		}
	}
}

func TestValidateDetailReportsInvalidAbandonmentEvidence(t *testing.T) {
	detail := &PlanDetail{
		State:  State{Status: StatusAbandoned, Plan: PlanState{ID: "plan"}},
		Slices: SlicesFile{PlanID: "plan"},
		Events: []Event{
			{Type: EventTypePlanAbandoned, Reason: " "},
			{Type: EventTypePlanAbandoned, Timestamp: time.Date(2026, 9, 1, 17, 0, 0, 0, time.UTC), Reason: "duplicate"},
		},
	}
	warnings := validateDetail(detail)
	for _, want := range []string{"reason is invalid", "timestamp is required", "multiple plan_abandoned events"} {
		if !containsWarning(warnings, want) {
			t.Fatalf("warnings missing %q: %v", want, warnings)
		}
	}

	detail.Events = nil
	warnings = validateDetail(detail)
	if !containsWarning(warnings, "no plan_abandoned evidence") {
		t.Fatalf("missing-event warnings = %v", warnings)
	}
}

func TestValidateDetailBaselineEvidence(t *testing.T) {
	for _, test := range []struct {
		name     string
		baseline *FinalVerificationBaseline
		invalid  bool
	}{
		{name: "legacy"},
		{name: "valid", baseline: &FinalVerificationBaseline{SHA: "base", Signatures: []string{"TestFlake"}}},
		{name: "empty sha", baseline: &FinalVerificationBaseline{Signatures: []string{"TestFlake"}}, invalid: true},
		{name: "empty signatures", baseline: &FinalVerificationBaseline{SHA: "base"}, invalid: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			detail := &PlanDetail{State: State{Plan: PlanState{ID: "plan", FinalVerification: &FinalVerification{Result: "failed", FailureKind: FinalVerificationFailureKindBaseline, Baseline: test.baseline}}}, Events: []Event{{Type: "final_verification", Result: "failed", FailureKind: FinalVerificationFailureKindBaseline, Baseline: test.baseline}}}
			warnings := validateDetail(detail)
			if containsWarning(warnings, "final_verification.baseline") != test.invalid || containsWarning(warnings, "event 1 baseline") != test.invalid {
				t.Fatalf("warnings = %v", warnings)
			}
		})
	}
}

func TestValidateDetailConstrainsFinalVerificationFailureKind(t *testing.T) {
	validKinds := []FinalVerificationFailureKind{FinalVerificationFailureKindBaseline, "", FinalVerificationFailureKindCode, FinalVerificationFailureKindToolMissing, FinalVerificationFailureKindTimeout, FinalVerificationFailureKindCancelled, FinalVerificationFailureKindInvalidCommand}
	for _, kind := range validKinds {
		detail := &PlanDetail{
			State:         State{Plan: PlanState{ID: "plan", FinalVerification: &FinalVerification{FailureKind: kind}}},
			Slices:        SlicesFile{PlanID: "plan"},
			PlanningBrief: PlanningBriefArtifact{Content: completePlanningBriefMarkdown()},
		}
		if warnings := validateDetail(detail); containsWarning(warnings, "final_verification.failure_kind") {
			t.Fatalf("valid failure kind %q produced warnings: %v", kind, warnings)
		}
	}

	detail := &PlanDetail{
		State:         State{Plan: PlanState{ID: "plan", FinalVerification: &FinalVerification{FailureKind: "network"}}},
		Slices:        SlicesFile{PlanID: "plan"},
		Events:        []Event{{FailureKind: "environment"}},
		PlanningBrief: PlanningBriefArtifact{Content: completePlanningBriefMarkdown()},
	}
	warnings := validateDetail(detail)
	if !containsWarning(warnings, "final_verification.failure_kind is invalid") || !containsWarning(warnings, "event 1 failure_kind is invalid") {
		t.Fatalf("invalid failure kinds were not reported: %v", warnings)
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

			if warnings := validateDetail(detail); len(warnings) != 0 {
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

	warnings := validateDetail(detail)
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

			warnings := validateDetail(detail)
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

	warnings := validateDetail(detail)
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

	warnings := validateDetail(detail)
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

	warnings := validateDetail(detail)
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

	warnings := validateDetail(detail)
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
	warnings := validateDetail(&PlanDetail{
		State:         State{Plan: PlanState{ID: "plan", PendingSlices: []string{"001-a"}}},
		Slices:        slices,
		PlanningBrief: PlanningBriefArtifact{Content: completePlanningBriefMarkdown()},
	})
	if len(warnings) != 0 {
		t.Fatalf("expected no warnings for omitted tags, got %v", warnings)
	}
}

func TestValidateDetailAllowsValidDecisionAndSequenceMetadata(t *testing.T) {
	detail := &PlanDetail{
		State: State{Plan: PlanState{
			ID: "plan", PendingSlices: []string{"001-a"},
			Decision: &Decision{
				Problem: "Plans lack structured rationale.", WhyNow: "The plan overview lacks rationale.", ExpectedBenefit: "Operators can compare work.",
				Readiness: DecisionReadinessReady, SuccessCriteria: []string{"The rationale is visible."},
				Disposition: DecisionDispositionReady, DispositionReason: "The work is bounded.",
				Priority: Priority{Level: PriorityOverallLevelMust, Impact: PriorityLevelHigh, Urgency: PriorityLevelMedium, Effort: PriorityEffortSmall, Risk: PriorityLevelLow, Confidence: PriorityLevelHigh, Rationale: "Benefit outweighs effort."},
			},
			Sequence: &Sequence{Position: 1, Total: 2, Relationships: []PlanRelation{{PlanID: "other", Type: PlanRelationBefore, Reason: "Other consumes this schema."}}},
		}},
		Slices:        SlicesFile{PlanID: "plan", Slices: []Slice{{ID: "001-a", Status: StatusPending}}},
		PlanningBrief: PlanningBriefArtifact{Content: completePlanningBriefMarkdown()},
	}

	if warnings := validateDetail(detail); len(warnings) != 0 {
		t.Fatalf("valid decision metadata produced warnings: %v", warnings)
	}
}

func TestValidateDetailWarnsForMalformedPresentDecisionAndSequence(t *testing.T) {
	detail := &PlanDetail{
		State: State{Plan: PlanState{
			ID: "plan", PendingSlices: []string{"001-a"},
			Decision: &Decision{
				Readiness: "maybe", SuccessCriteria: []string{" "}, Disposition: "soon",
				Priority: Priority{Level: "urgent", Impact: "maximum", Urgency: "now", Effort: "tiny", Risk: "none"},
			},
			Sequence: &Sequence{Position: 3, Total: 2, Relationships: []PlanRelation{
				{PlanID: "plan", Type: "depends_on"},
				{PlanID: "plan", Type: PlanRelationAfter, Reason: "duplicate"},
				{Type: PlanRelationRelated, Reason: "missing target"},
			}},
		}},
		Slices:        SlicesFile{PlanID: "plan", Slices: []Slice{{ID: "001-a", Status: StatusPending}}},
		PlanningBrief: PlanningBriefArtifact{Content: completePlanningBriefMarkdown()},
	}

	warnings := validateDetail(detail)
	for _, want := range []string{
		"plan.decision.problem is required",
		"plan.decision.why_now is required",
		"plan.decision.expected_benefit is required",
		"plan.decision.readiness is invalid",
		"plan.decision.success_criteria[0] is required",
		"plan.decision.disposition is invalid",
		"plan.decision.disposition_reason is required",
		"plan.decision.priority.level is invalid",
		"plan.decision.priority.impact is invalid",
		"plan.decision.priority.confidence is invalid",
		"plan.decision.priority.effort is invalid",
		"plan.decision.priority.rationale is required",
		"plan.sequence.position cannot exceed total",
		"cannot reference its own plan",
		"duplicates relationship to plan plan",
		"relationships[0].type is invalid",
		"relationships[0].reason is required",
		"relationships[2].plan_id is required",
	} {
		if !containsWarning(warnings, want) {
			t.Errorf("expected warning %q, got %v", want, warnings)
		}
	}
}

func TestValidateDetailWarnsForNonPositiveSequenceBounds(t *testing.T) {
	warnings := validateSequence("plan", &Sequence{})
	for _, want := range []string{"position must be at least 1", "total must be at least 1"} {
		if !containsWarning(warnings, want) {
			t.Errorf("expected warning %q, got %v", want, warnings)
		}
	}
}

func TestValidateRuntimePrerequisites(t *testing.T) {
	valid := []RuntimePrerequisite{{PlanID: "other-plan", Reason: "Its merged schema is required."}}
	if warnings := validateRuntimePrerequisites("plan", valid); len(warnings) != 0 {
		t.Fatalf("valid prerequisites produced warnings: %v", warnings)
	}

	tooMany := make([]RuntimePrerequisite, maxRuntimePrerequisites+1)
	warnings := validateRuntimePrerequisites("plan", append([]RuntimePrerequisite{
		{PlanID: "plan", Reason: "self"},
		{PlanID: "other", Reason: ""},
		{PlanID: "other", Reason: "duplicate"},
		{PlanID: "../other", Reason: "path"},
	}, tooMany...))
	for _, want := range []string{"must contain at most", "cannot reference its own plan", "reason is required", "duplicates prerequisite plan other", "must be an exact plan ID"} {
		if !containsWarning(warnings, want) {
			t.Errorf("expected warning %q, got %v", want, warnings)
		}
	}
}

func TestValidateRuntimePrerequisiteCycleFollowsOnlyResolvablePlans(t *testing.T) {
	plans := map[string][]RuntimePrerequisite{
		"plan-b": {{PlanID: "plan-c", Reason: "C first"}},
		"plan-c": {{PlanID: "plan-a", Reason: "A first"}},
	}
	resolve := func(id string) ([]RuntimePrerequisite, bool) {
		prerequisites, ok := plans[id]
		return prerequisites, ok
	}
	warnings := validateRuntimePrerequisiteCycle("plan-a", []RuntimePrerequisite{{PlanID: "plan-b", Reason: "B first"}}, resolve)
	if !containsWarning(warnings, "resolvable cycle") {
		t.Fatalf("expected cycle warning, got %v", warnings)
	}
	if warnings := validateRuntimePrerequisiteCycle("plan-a", []RuntimePrerequisite{{PlanID: "missing", Reason: "missing first"}}, resolve); len(warnings) != 0 {
		t.Fatalf("unresolvable prerequisite should not report a validation cycle: %v", warnings)
	}
}

func TestValidateDetailAllowsSupportedAndLegacyPlanChangeTypes(t *testing.T) {
	changeTypes := append(SupportedChangeTypes(), "")
	for _, changeType := range changeTypes {
		t.Run(string(changeType), func(t *testing.T) {
			detail := &PlanDetail{
				State:         State{Plan: PlanState{ID: "plan", ChangeType: changeType, PendingSlices: []string{"001-a"}}},
				Slices:        SlicesFile{PlanID: "plan", Slices: []Slice{{ID: "001-a", Status: StatusPending}}},
				PlanningBrief: PlanningBriefArtifact{Content: completePlanningBriefMarkdown()},
			}
			if warnings := validateDetail(detail); len(warnings) != 0 {
				t.Fatalf("change type %q produced warnings: %v", changeType, warnings)
			}
		})
	}
}

func TestValidateDetailWarnsForInvalidPlanChangeType(t *testing.T) {
	detail := &PlanDetail{
		State:         State{Plan: PlanState{ID: "plan", ChangeType: "feature", PendingSlices: []string{"001-a"}}},
		Slices:        SlicesFile{PlanID: "plan", Slices: []Slice{{ID: "001-a", Status: StatusPending}}},
		PlanningBrief: PlanningBriefArtifact{Content: completePlanningBriefMarkdown()},
	}

	warnings := validateDetail(detail)
	if !containsWarning(warnings, `plan.change_type is invalid: unsupported plan change type "feature"`) || !containsWarning(warnings, "feat, fix, docs") {
		t.Fatalf("expected useful invalid change type warning, got %v", warnings)
	}
}

func TestValidateDetailWarnsForTypedApprovedProposalMismatch(t *testing.T) {
	base := PlanDetail{
		State: State{Plan: PlanState{
			ID: "plan", ChangeType: ChangeTypeFix, PendingSlices: []string{"001-a"},
			Review: &PlanReview{
				Status: ReviewStatusCompleted, Verdict: ReviewVerdictApprove,
				CommitMessage: &ReviewCommitMessage{Subject: "feat(plan): wrong type", Body: "What:\nChange it.\n\nWhy:\nIt matters."},
			},
		}},
		Slices:        SlicesFile{PlanID: "plan", Slices: []Slice{{ID: "001-a", Status: StatusPending}}},
		PlanningBrief: PlanningBriefArtifact{Content: completePlanningBriefMarkdown()},
	}
	warnings := validateDetail(&base)
	if !containsWarning(warnings, `commit_message type mismatch: expected "fix", observed "feat"`) {
		t.Fatalf("expected typed proposal mismatch warning, got %v", warnings)
	}

	legacy := clonePlanDetail(&base)
	legacy.State.Plan.ChangeType = ""
	if warnings := validateDetail(legacy); containsWarning(warnings, "commit_message type mismatch") {
		t.Fatalf("legacy untyped proposal produced mismatch warning: %v", warnings)
	}
	incomplete := clonePlanDetail(&base)
	incomplete.State.Plan.Review.CommitMessage = nil
	if warnings := validateDetail(incomplete); containsWarning(warnings, "commit_message type mismatch") {
		t.Fatalf("incomplete proposal produced mismatch warning: %v", warnings)
	}
}

func TestValidateDetailWarnsForMalformedHistoricalFinalizationFailure(t *testing.T) {
	detail := &PlanDetail{
		State: State{Plan: PlanState{
			ID: "plan", PendingSlices: []string{"001-a"},
			FinalizationFailure: &FinalizationFailure{Phase: FinalizationFailurePhasePullRequest, Category: "raw output is not a label"},
		}},
		Slices:        SlicesFile{PlanID: "plan", Slices: []Slice{{ID: "001-a", Status: StatusPending}}},
		PlanningBrief: PlanningBriefArtifact{Content: completePlanningBriefMarkdown()},
	}
	warnings := validateDetail(detail)
	if !containsWarning(warnings, "plan.finalization_failure is invalid") {
		t.Fatalf("expected historical failure warning, got %v", warnings)
	}
}

func TestValidateDetailAllowsMissingWorkspaceMetadata(t *testing.T) {
	detail := &PlanDetail{
		State:         State{Plan: PlanState{ID: "plan", PendingSlices: []string{"001-a"}}},
		Slices:        SlicesFile{PlanID: "plan", Slices: []Slice{{ID: "001-a", Status: StatusPending}}},
		PlanningBrief: PlanningBriefArtifact{Content: completePlanningBriefMarkdown()},
	}

	warnings := validateDetail(detail)
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

	warnings := validateDetail(detail)
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
