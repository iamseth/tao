package plan

import (
	"path/filepath"
	"strings"

	verificationimpl "github.com/iamseth/tao/internal/plan/verification"
)

// Verification analysis validates declared commands before run agents execute them.
type VerificationFindingSeverity = verificationimpl.FindingSeverity

const (
	VerificationFindingWarning = verificationimpl.FindingWarning
	VerificationFindingError   = verificationimpl.FindingError
)

type VerificationFinding = verificationimpl.Finding

type VerificationValidationResult struct {
	Findings []VerificationFinding `json:"findings,omitempty"`
}

func (r VerificationValidationResult) HasErrors() bool {
	for _, finding := range r.Findings {
		if finding.Severity == VerificationFindingError {
			return true
		}
	}
	return false
}

// ValidatePlanVerification checks every slice for plan-wide validation commands.
func ValidatePlanVerification(detail *PlanDetail) VerificationValidationResult {
	var result VerificationValidationResult
	analyzer := verificationimpl.NewAnalyzer(detail.State.Repo.Root)
	allowances := futureFileAllowances(detail, true)
	for _, slice := range detail.Slices.Slices {
		result.Findings = append(result.Findings, validateSliceVerificationWithAnalyzer(detail.State.Repo.Root, analyzer, slice, false, allowances[slice.ID])...)
	}
	result.Findings = append(result.Findings, validatePlanGuardrails(detail)...)
	return result
}

// ValidateSelectedSliceVerification is stricter for the runnable slice because it gates a run.
func ValidateSelectedSliceVerification(detail *PlanDetail) VerificationValidationResult {
	return ValidateSelectedSliceVerificationAtRoot(detail, "")
}

// ValidateSelectedSliceVerificationAtRoot validates the runnable slice as it will
// execute from repoRoot. When repoRoot is empty, the plan's recorded repository
// root is used.
func ValidateSelectedSliceVerificationAtRoot(detail *PlanDetail, repoRoot string) VerificationValidationResult {
	lifecycle := AnalyzeLifecycle(detail)
	if lifecycle.RunnableError != nil {
		return VerificationValidationResult{Findings: []VerificationFinding{{
			Severity: VerificationFindingError,
			SliceID:  lifecycle.NextSliceID,
			Code:     "verification_slice_not_runnable",
			Message:  lifecycle.RunnableError.Error(),
		}}}
	}
	if lifecycle.NextSlice == nil {
		return VerificationValidationResult{}
	}
	validationRoot := verificationRoot(detail, repoRoot)
	allowances := futureFileAllowancesAtRoot(detail, false, validationRoot)
	findings := validateSliceVerification(validationRoot, *lifecycle.NextSlice, true, allowances[lifecycle.NextSlice.ID])
	findings = append(findings, validateSelectedSliceGuardrails(detail)...)
	return VerificationValidationResult{Findings: findings}
}

func verificationRoot(detail *PlanDetail, repoRoot string) string {
	if strings.TrimSpace(repoRoot) != "" {
		return repoRoot
	}
	if detail == nil {
		return ""
	}
	return detail.State.Repo.Root
}

func validateSliceVerification(repoRoot string, slice Slice, selected bool, allowedFutureFiles futureFileSet) []VerificationFinding {
	return validateSliceVerificationWithAnalyzer(repoRoot, verificationimpl.NewAnalyzer(repoRoot), slice, selected, allowedFutureFiles)
}

func validateSliceVerificationWithAnalyzer(repoRoot string, analyzer *verificationimpl.Analyzer, slice Slice, selected bool, allowedFutureFiles futureFileSet) []VerificationFinding {
	findings := make([]VerificationFinding, 0)
	for _, command := range slice.Verification.Commands {
		analysis := analyzer.Analyze(command)
		for _, finding := range analysis.Findings {
			finding.SliceID = slice.ID
			if finding.Code == "verification_path_missing" && allowedFutureFiles.contains(repoRoot, finding.Path) {
				finding.Severity = VerificationFindingWarning
				finding.Code = "verification_future_file_missing"
				finding.Message = "verification command references a future file declared in this plan; keep this warning until the file is created"
			}
			if selected && (finding.Code == "verification_path_missing" || finding.Code == "verification_shell_hazard") {
				finding.Severity = VerificationFindingError
			}
			findings = append(findings, finding)
		}
	}
	return findings
}

type futureFileSet map[string]struct{}

func (s futureFileSet) contains(repoRoot string, path string) bool {
	if len(s) == 0 {
		return false
	}
	normalized, ok := normalizePathUnderRepo(repoRoot, path)
	if !ok {
		return false
	}
	_, ok = s[normalized]
	return ok
}

func futureFileAllowances(detail *PlanDetail, includeSerialEarlier bool) map[string]futureFileSet {
	return futureFileAllowancesAtRoot(detail, includeSerialEarlier, "")
}

func futureFileAllowancesAtRoot(detail *PlanDetail, includeSerialEarlier bool, repoRoot string) map[string]futureFileSet {
	allowances := make(map[string]futureFileSet)
	if detail == nil {
		return allowances
	}
	repoRoot = verificationRoot(detail, repoRoot)

	byID := make(map[string]*Slice, len(detail.Slices.Slices))
	for i := range detail.Slices.Slices {
		byID[detail.Slices.Slices[i].ID] = &detail.Slices.Slices[i]
	}

	pendingPosition := make(map[string]int, len(detail.State.Plan.PendingSlices))
	for i, id := range detail.State.Plan.PendingSlices {
		pendingPosition[id] = i
	}

	for i := range detail.Slices.Slices {
		slice := &detail.Slices.Slices[i]
		if !futureFileAllowanceEligible(*slice, pendingPosition) {
			continue
		}
		allowed := make(futureFileSet)
		addExpectedFutureFiles(allowed, repoRoot, slice.ExpectedFiles)
		for _, dependencyID := range slice.DependsOn {
			if dependency := byID[dependencyID]; dependency != nil {
				addExpectedFutureFiles(allowed, repoRoot, dependency.ExpectedFiles)
			}
		}
		if includeSerialEarlier && detail.Slices.Execution.Mode == "serial" {
			if position, ok := pendingPosition[slice.ID]; ok {
				for _, earlierID := range detail.State.Plan.PendingSlices[:position] {
					if earlier := byID[earlierID]; earlier != nil {
						addExpectedFutureFiles(allowed, repoRoot, earlier.ExpectedFiles)
					}
				}
			}
		}
		if len(allowed) > 0 {
			allowances[slice.ID] = allowed
		}
	}
	return allowances
}

func futureFileAllowanceEligible(slice Slice, pendingPosition map[string]int) bool {
	if _, ok := pendingPosition[slice.ID]; ok {
		return true
	}
	return slice.Status == StatusPending || slice.Status == StatusInProgress || slice.Status == StatusBlocked
}

func addExpectedFutureFiles(allowed futureFileSet, repoRoot string, expectedFiles []string) {
	for _, expectedFile := range expectedFiles {
		normalized, ok := normalizeExpectedFutureFile(repoRoot, expectedFile)
		if ok {
			allowed[normalized] = struct{}{}
		}
	}
}

func normalizeExpectedFutureFile(repoRoot string, path string) (string, bool) {
	trimmed := strings.TrimSpace(path)
	if !concreteExpectedFutureFile(trimmed) {
		return "", false
	}
	return normalizePathUnderRepo(repoRoot, trimmed)
}

func concreteExpectedFutureFile(path string) bool {
	if path == "" || vagueExpectedFile(path) {
		return false
	}
	return !strings.ContainsAny(path, "*?[")
}

func normalizePathUnderRepo(repoRoot string, path string) (string, bool) {
	root := strings.TrimSpace(repoRoot)
	candidate := strings.TrimSpace(path)
	if root == "" || candidate == "" {
		return "", false
	}

	cleanRoot := filepath.Clean(root)
	cleanPath := candidate
	if !filepath.IsAbs(cleanPath) {
		cleanPath = filepath.Join(cleanRoot, cleanPath)
	}
	cleanPath = filepath.Clean(cleanPath)

	relative, err := filepath.Rel(cleanRoot, cleanPath)
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", false
	}
	return cleanPath, true
}
