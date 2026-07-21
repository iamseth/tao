package verification

// FindingSeverity classifies whether a verification analysis finding should block work.
type FindingSeverity string

const (
	FindingWarning FindingSeverity = "warning"
	FindingError   FindingSeverity = "error"
)

// Finding describes a command-analysis issue surfaced by plan validation.
type Finding struct {
	Severity   FindingSeverity `json:"severity"`
	SliceID    string          `json:"slice_id,omitempty"`
	Code       string          `json:"code"`
	Message    string          `json:"message"`
	Command    string          `json:"command,omitempty"`
	Path       string          `json:"path,omitempty"`
	Suggestion string          `json:"suggestion,omitempty"`
}

// FailureClassification identifies verification failures caused by invalid commands.
type FailureClassification struct {
	Invalid          bool   `json:"invalid"`
	Code             string `json:"code,omitempty"`
	Message          string `json:"message,omitempty"`
	Command          string `json:"command,omitempty"`
	CorrectedCommand string `json:"corrected_command,omitempty"`
}

// PathCheck records how an obvious path argument resolves from the inferred cwd.
type PathCheck struct {
	Argument       string `json:"argument"`
	ResolvedPath   string `json:"resolved_path"`
	Exists         bool   `json:"exists"`
	SuggestedValue string `json:"suggested_value,omitempty"`
	SuggestedPath  string `json:"suggested_path,omitempty"`
}

// CommandAnalysis captures inferred execution context and command-shape findings.
type CommandAnalysis struct {
	Command       string      `json:"command"`
	Executable    string      `json:"executable,omitempty"`
	WorkingDir    string      `json:"working_dir"`
	PathChecks    []PathCheck `json:"path_checks,omitempty"`
	Findings      []Finding   `json:"findings,omitempty"`
	SuggestedArgs []string    `json:"suggested_args,omitempty"`
}

// Analyzer analyzes verification commands while reusing filesystem lookup caches.
type Analyzer struct {
	lookup commandLookup
}

// NewAnalyzer returns a command analyzer rooted at repoRoot.
func NewAnalyzer(repoRoot string) *Analyzer {
	return &Analyzer{lookup: newLookup(repoRoot)}
}

// AnalyzeCommand analyzes one verification command using a fresh lookup cache.
func AnalyzeCommand(repoRoot string, command string) CommandAnalysis {
	return NewAnalyzer(repoRoot).Analyze(command)
}

// Analyze infers cwd, path checks, and shell hazards for command.
func (a *Analyzer) Analyze(command string) CommandAnalysis {
	if a == nil || a.lookup == nil {
		return analyzeCommand(newLookup(""), command)
	}
	return analyzeCommand(a.lookup, command)
}

func analyzeCommand(lookup commandLookup, command string) CommandAnalysis {
	analysis := CommandAnalysis{Command: command, WorkingDir: lookup.repoRoot()}
	tokens := splitCommandFields(command)
	if len(tokens) == 0 {
		return analysis
	}

	workingTokens := tokens
	if cwd, remaining, ok := cdCommand(lookup, tokens); ok {
		analysis.WorkingDir = cwd
		workingTokens = remaining
	} else if cwd, ok := pnpmDirCommand(lookup, tokens); ok {
		analysis.WorkingDir = cwd
	} else if cwd, missingFilter, ok := pnpmFilterCommand(lookup, tokens); ok {
		analysis.WorkingDir = cwd
	} else if missingFilter != "" {
		analysis.Findings = append(analysis.Findings, Finding{
			Severity: FindingWarning,
			Code:     "verification_filter_unresolved",
			Message:  "verification command uses a pnpm filter whose package directory could not be inferred",
			Command:  command,
			Path:     missingFilter,
		})
	}

	if len(workingTokens) > 0 {
		analysis.Executable = workingTokens[0]
	}
	if !lookup.dirExists(analysis.WorkingDir) {
		analysis.Findings = append(analysis.Findings, Finding{
			Severity: FindingError,
			Code:     "verification_cwd_missing",
			Message:  "verification command working directory does not exist",
			Command:  command,
			Path:     analysis.WorkingDir,
		})
	}

	for _, arg := range obviousPathArgs(workingTokens) {
		check := PathCheck{Argument: arg, ResolvedPath: lookup.resolve(analysis.WorkingDir, arg)}
		check.Exists = lookup.pathExists(check.ResolvedPath)
		if !check.Exists {
			if suggested, suggestedPath, ok := lookup.packageRelativeSuggestion(analysis.WorkingDir, arg); ok {
				check.SuggestedValue = suggested
				check.SuggestedPath = suggestedPath
				analysis.SuggestedArgs = append(analysis.SuggestedArgs, suggested)
			}
			analysis.Findings = append(analysis.Findings, Finding{
				Severity:   FindingWarning,
				Code:       "verification_path_missing",
				Message:    "verification command references a path that does not exist from its inferred working directory",
				Command:    command,
				Path:       check.ResolvedPath,
				Suggestion: check.SuggestedValue,
			})
		}
		analysis.PathChecks = append(analysis.PathChecks, check)
	}
	analysis.Findings = append(analysis.Findings, shellHazardFindings(command, workingTokens)...)

	return analysis
}

type commandLookup interface {
	repoRoot() string
	resolve(cwd string, value string) string
	dirExists(path string) bool
	pathExists(path string) bool
	findPackageDir(name string) (string, bool)
	packageRelativeSuggestion(cwd string, arg string) (string, string, bool)
}
