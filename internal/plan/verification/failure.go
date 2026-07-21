package verification

import "strings"

// Run is the narrow subset of a recorded verification run needed for classification.
type Run struct {
	Command string
	CWD     string
	Details string
}

// ClassifyRun identifies invalid-command failures from a recorded verification run.
func ClassifyRun(repoRoot string, run Run) FailureClassification {
	lookup := newLookup(repoRoot)
	if run.CWD != "" && !lookup.dirExists(run.CWD) {
		return FailureClassification{
			Invalid: true,
			Code:    "verification_cwd_missing",
			Message: "verification command working directory does not exist",
			Command: run.Command,
		}
	}
	return ClassifyFailure(repoRoot, run.Command, run.Details)
}

// ClassifyFailure identifies output patterns caused by invalid verification commands.
func ClassifyFailure(repoRoot string, command string, output string) FailureClassification {
	details := strings.ToLower(output)
	classification := FailureClassification{Command: command}
	if strings.TrimSpace(details) == "" {
		return classification
	}

	if hasCommandNotFound(details) {
		classification.Invalid = true
		classification.Code = "verification_command_not_found"
		classification.Message = "verification command executable was not found"
		return classification
	}
	if hasInvalidConfigPath(details) {
		classification.Invalid = true
		classification.Code = "verification_config_missing"
		classification.Message = "verification command references a missing or invalid config path"
		return classification
	}
	if strings.Contains(details, "no test files found") {
		classification.Invalid = true
		classification.Code = "verification_no_test_files"
		classification.Message = "verification command failed before loading tests because no test files matched"
		if corrected, ok := correctedCommandForPathMismatch(repoRoot, command); ok {
			classification.Code = "verification_path_cwd_mismatch"
			classification.Message = "verification command appears to use a path from the wrong working directory"
			classification.CorrectedCommand = corrected
		}
		return classification
	}
	if hasMissingCWD(details) {
		classification.Invalid = true
		classification.Code = "verification_cwd_missing"
		classification.Message = "verification command working directory does not exist"
		return classification
	}
	return classification
}

func correctedCommandForPathMismatch(repoRoot string, command string) (string, bool) {
	analysis := AnalyzeCommand(repoRoot, command)
	for _, check := range analysis.PathChecks {
		if check.Exists || check.SuggestedValue == "" {
			continue
		}
		return replaceCommandToken(command, check.Argument, check.SuggestedValue), true
	}
	return "", false
}

func hasCommandNotFound(details string) bool {
	return strings.Contains(details, "command not found") ||
		strings.Contains(details, "executable file not found") ||
		strings.Contains(details, "not recognized as an internal or external command") ||
		strings.Contains(details, "no such file or directory") && strings.Contains(details, "exec")
}

func hasInvalidConfigPath(details string) bool {
	if !strings.Contains(details, "config") {
		return false
	}
	return strings.Contains(details, "not found") ||
		strings.Contains(details, "does not exist") ||
		strings.Contains(details, "could not resolve") ||
		strings.Contains(details, "failed to load") ||
		strings.Contains(details, "cannot find")
}

func hasMissingCWD(details string) bool {
	return strings.Contains(details, "chdir") && (strings.Contains(details, "no such file or directory") || strings.Contains(details, "not a directory"))
}
