package plan

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func coupledDetail(root string, files ...string) *PlanDetail {
	return &PlanDetail{State: State{Status: StatusPlanned, Repo: Repo{Root: root}, Plan: PlanState{ID: "example", PendingSlices: []string{"001-a"}}}, Slices: SlicesFile{Slices: []Slice{{ID: "001-a", Status: StatusPending, ExpectedFiles: files, Verification: Verification{Commands: []string{"go version"}}}}}}
}

func coupledTarget(t *testing.T, root, name string) {
	t.Helper()
	full := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte("unchanged"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func coupledFindings(result VerificationValidationResult) []VerificationFinding {
	var found []VerificationFinding
	for _, f := range result.Findings {
		if f.Code == "slice_coupled_file_missing" {
			found = append(found, f)
		}
	}
	return found
}

func TestCoupledFilesTable(t *testing.T) {
	insights := []string{
		"internal/cli/testdata/insights/all-repositories-digest.golden",
		"internal/cli/testdata/insights/all-repositories-report.golden",
		"internal/cli/testdata/insights/all-repositories-scorecard.golden",
		"internal/cli/testdata/insights/all-repositories-trend.golden",
		"internal/cli/testdata/insights/repository-digest.golden",
		"internal/cli/testdata/insights/repository-report.golden",
		"internal/cli/testdata/insights/repository-scorecard.golden",
		"internal/cli/testdata/insights/repository-trend-json.golden",
		"internal/cli/testdata/insights/repository-trend.golden",
	}
	cases := []struct {
		trigger string
		targets []string
	}{
		{"internal/cli/command.go", []string{"internal/cli/testdata/zsh_completion.golden"}},
		{"internal/runtimeconfig/command_options.go", []string{"docs/configuration.md"}},
		{"internal/insights/report.go", insights},
		{"internal/cli/insights.go", append(append([]string{}, insights...), "internal/cli/testdata/zsh_completion.golden")},
		{"internal/plan/lifecycle_models.go", []string{"internal/plan/clone.go"}},
		{"internal/plan/artifact_models.go", []string{"internal/plan/clone.go"}},
		{"internal/plan/artifact_changeset.go", []string{"internal/plan/artifact_files.go"}},
	}
	for _, tc := range cases {
		t.Run(tc.trigger, func(t *testing.T) {
			root := t.TempDir()
			for _, target := range tc.targets {
				coupledTarget(t, root, target)
			}
			detail := coupledDetail(root, tc.trigger, tc.trigger)
			found := coupledFindings(ValidatePlanVerification(detail))
			if len(found) != 1 {
				t.Fatalf("findings: %+v", found)
			}
			for _, target := range tc.targets {
				if strings.Count(found[0].Message, "--allow-file "+target) != 1 {
					t.Fatalf("target %s: %s", target, found[0].Message)
				}
			}
			if strings.Count(found[0].Message, "--allow-file ") != len(tc.targets) {
				t.Fatal(found[0].Message)
			}
		})
	}
}

func TestCoupledFilesSafetyAndAbsence(t *testing.T) {
	target := "internal/cli/testdata/zsh_completion.golden"
	for _, mode := range []string{"missing", "directory", "unreadable", "escaping symlink", "contained symlink", "normal"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			full := filepath.Join(root, target)
			if mode != "missing" {
				if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
					t.Fatal(err)
				}
				switch mode {
				case "directory":
					if err := os.Mkdir(full, 0o700); err != nil {
						t.Fatal(err)
					}
				case "escaping symlink", "contained symlink":
					destRoot := root
					if mode == "escaping symlink" {
						destRoot = t.TempDir()
					}
					coupledTarget(t, destRoot, "destination")
					if err := os.Symlink(filepath.Join(destRoot, "destination"), full); err != nil {
						t.Fatal(err)
					}
				default:
					coupledTarget(t, root, target)
					if mode == "unreadable" {
						if err := os.Chmod(full, 0o000); err != nil {
							t.Fatal(err)
						}
					}
				}
			}
			detail := coupledDetail(root, "internal/cli/a.go")
			got := len(coupledFindings(ValidatePlanVerification(detail)))
			want := 0
			if mode == "normal" || mode == "contained symlink" {
				want = 1
			}
			if got != want {
				t.Fatalf("got %d want %d", got, want)
			}
		})
	}
	root := t.TempDir()
	coupledTarget(t, root, target)
	for _, input := range []string{"../internal/cli/a.go", "/internal/cli/a.go", "internal/cli/*.go", "internal/cli/../cli/a.go", "internal/cli/a\n.go", "internal/cli/a\\b.go", "internal/cli/sub/a.go", "internal/cli/a?.go"} {
		if got := coupledFindings(ValidatePlanVerification(coupledDetail(root, input))); len(got) != 0 {
			t.Fatalf("unsafe input %q: %+v", input, got)
		}
	}
}

func TestCoupledFilesOrderQuotingAndRoot(t *testing.T) {
	root := t.TempDir()
	for _, target := range []string{"docs/configuration.md", "internal/cli/testdata/zsh_completion.golden", "internal/plan/clone.go"} {
		coupledTarget(t, root, target)
	}
	detail := coupledDetail(t.TempDir(), "internal/plan/artifact_models.go", "internal/cli/a.go", "internal/runtimeconfig/a.go", "internal/plan/lifecycle_models.go")
	detail.State.Plan.ID = "plan';$(touch bad)"
	detail.Slices.Slices[0].ID = "slice; echo bad"
	detail.State.Plan.PendingSlices = []string{detail.Slices.Slices[0].ID}
	if len(coupledFindings(ValidatePlanVerification(detail))) != 0 {
		t.Fatal("non-Tao root suggested targets")
	}
	result := ValidateSelectedSliceVerificationAtRoot(detail, root)
	found := coupledFindings(result)
	if result.HasErrors() || len(found) != 1 {
		t.Fatalf("findings: %+v", result)
	}
	message := found[0].Message
	command := "tao edit amend " + shellQuoteActionArg(detail.State.Plan.ID) + " " + shellQuoteActionArg(detail.Slices.Slices[0].ID) + " --allow-file docs/configuration.md --allow-file internal/cli/testdata/zsh_completion.golden --allow-file internal/plan/clone.go --mechanical 'Synchronize known coupled files'"
	if !strings.HasSuffix(message, command) {
		t.Fatal(message)
	}
	if len(coupledFindings(ValidateSelectedSliceVerificationAtRoot(detail, ""))) != 0 {
		t.Fatal("override leaked")
	}
}

func TestCoupledFilesAllowances(t *testing.T) {
	for _, mode := range []string{"listed", "dependency", "serial", "unrelated"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			target := "internal/cli/testdata/zsh_completion.golden"
			coupledTarget(t, root, target)
			detail := coupledDetail(root, "internal/cli/a.go")
			earlier := Slice{ID: "earlier", Status: StatusCompleted, ExpectedFiles: []string{target}, Verification: Verification{Commands: []string{"go version"}}}
			switch mode {
			case "listed":
				detail.Slices.Slices[0].ExpectedFiles = append(detail.Slices.Slices[0].ExpectedFiles, target)
			case "dependency":
				detail.Slices.Slices[0].DependsOn = []string{"earlier"}
				detail.State.Plan.CompletedSlices = []string{"earlier"}
			case "serial":
				detail.Slices.Execution.Mode = "serial"
				detail.State.Plan.PendingSlices = []string{"earlier", "001-a"}
				detail.State.Plan.CurrentSlice = new("001-a")
			}
			detail.Slices.Slices = append(detail.Slices.Slices, earlier)
			wantPlan, wantSelected := 0, 0
			if mode == "unrelated" {
				wantPlan, wantSelected = 1, 1
			}
			if mode == "serial" {
				wantSelected = 1
			}
			if got := len(coupledFindings(ValidatePlanVerification(detail))); got != wantPlan {
				t.Fatalf("plan got %d want %d", got, wantPlan)
			}
			result := ValidateSelectedSliceVerificationAtRoot(detail, "")
			if result.HasErrors() || len(coupledFindings(result)) != wantSelected {
				t.Fatalf("selected want %d: %+v", wantSelected, result)
			}
		})
	}
}

func TestCoupledFilesPublicValidation(t *testing.T) {
	root := t.TempDir()
	target := "internal/cli/testdata/zsh_completion.golden"
	coupledTarget(t, root, target)
	detail := coupledDetail(root, "internal/cli/example.go")
	before, _ := json.Marshal(detail)
	for _, result := range []VerificationValidationResult{ValidatePlanVerification(detail), ValidateSelectedSliceVerificationAtRoot(detail, "")} {
		found := coupledFindings(result)
		if result.HasErrors() || len(found) != 1 || !strings.Contains(found[0].Message, "tao edit amend example 001-a --allow-file "+target+" --mechanical ") {
			t.Fatalf("findings: %+v", result)
		}
	}
	after, _ := json.Marshal(detail)
	if string(before) != string(after) {
		t.Fatal("mutated detail")
	}
	data, err := os.ReadFile(filepath.Join(root, target)) //nolint:gosec // G304: test-controlled temporary target.
	if err != nil || string(data) != "unchanged" {
		t.Fatal("mutated target", err)
	}
}
