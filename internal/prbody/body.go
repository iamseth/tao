package prbody

import (
	"fmt"
	"regexp"
	"strings"

	verificationimpl "github.com/iamseth/tao/internal/plan/verification"
)

const narrativeFallback = "See the commit description for change context."

var (
	taoWordRE        = regexp.MustCompile(`(?i)\btao\b`)
	sliceWordRE      = regexp.MustCompile(`(?i)\bslices?\b`)
	lifecycleRE      = regexp.MustCompile(`(?i)\blifecycle\b`)
	squashAndMergeRE = regexp.MustCompile(`(?i)squash and merge`)
	mergeGuidanceRE  = regexp.MustCompile(`(?i)merge guidance`)
	cleanupDryRunRE  = regexp.MustCompile(`(?i)cleanup --dry-run`)
)

// VerificationResult is the body-safe projection of a recorded verification
// result.
type VerificationResult struct {
	Command string
	Result  string
}

// Input contains only the deterministic data needed to build a pull request
// body. CommitMessageBody is the approved proposal body, when one exists.
type Input struct {
	PlanID              string
	CommitMessageBody   string
	VerificationResults []VerificationResult
	DiffStat            string
}

// ValidationInput describes the immutable portions an agent-authored body must
// preserve from its deterministic draft.
type ValidationInput struct {
	PlanID             string
	DiffStat           string
	DeterministicDraft string
}

// Build constructs a deterministic pull request body.
func Build(input Input) string {
	problem, fix := problemAndFix(input.CommitMessageBody, input.PlanID)
	var b strings.Builder
	fmt.Fprintf(&b, "## Problem\n\n%s\n\n## Fix\n\n%s\n\n## Tests\n\n", problem, fix)
	writeTests(&b, input.VerificationResults)
	b.WriteString("\n## Deploy\n\nNo special deployment steps are required.\n\n## Scope\n\n")
	b.WriteString(scope(input.DiffStat))
	return b.String()
}

// Validate rejects a generated body that changes deterministic headings,
// Scope, or Tests, or reintroduces Tao-specific reviewer narrative.
func Validate(body string, input ValidationInput) error {
	if body == "" {
		return fmt.Errorf("agent returned empty pull request body")
	}
	body = strings.ReplaceAll(body, "\r\n", "\n")
	lines := strings.Split(body, "\n")
	headings, headingLines := levelTwoHeadings(lines)
	requiredHeadings := []string{"## Problem", "## Fix", "## Tests", "## Deploy", "## Scope"}
	if len(headings) != len(requiredHeadings) {
		return fmt.Errorf("agent pull request body must contain exactly the five required level-two headings in order")
	}
	for i, required := range requiredHeadings {
		if headings[i] != required {
			return fmt.Errorf("agent pull request body level-two heading %d must be %s", i+1, required)
		}
	}
	scopeText := strings.TrimSpace(strings.Join(lines[headingLines[len(headingLines)-1]+1:], "\n"))
	expectedScope := strings.ReplaceAll(scope(input.DiffStat), "\r\n", "\n")
	if scopeText != strings.TrimSpace(expectedScope) {
		return fmt.Errorf("agent pull request body must preserve the complete collapsed Changed files block and exact diff stat within Scope")
	}
	tests := strings.TrimSpace(strings.Join(lines[headingLines[2]+1:headingLines[3]], "\n"))
	reviewerNarrative := strings.ToLower(strings.Join([]string{
		strings.Join(lines[headingLines[0]+1:headingLines[1]], "\n"),
		strings.Join(lines[headingLines[1]+1:headingLines[2]], "\n"),
		strings.Join(lines[headingLines[3]+1:headingLines[4]], "\n"),
	}, "\n"))
	if noise := forbiddenNarrativeLanguage(reviewerNarrative); noise != "" {
		return fmt.Errorf("agent pull request body contains forbidden Tao-specific language %q in reviewer narrative", noise)
	}
	if id := strings.ToLower(strings.TrimSpace(input.PlanID)); id != "" && (strings.Contains(reviewerNarrative, id) || strings.Contains(strings.ToLower(tests), id)) {
		return fmt.Errorf("agent pull request body contains the plan ID in reviewer narrative")
	}
	if testsContainTaoCommand(tests) {
		return fmt.Errorf("agent pull request body contains a direct Tao lifecycle command in Tests")
	}
	expectedTests, err := testsSection(input.DeterministicDraft)
	if err != nil {
		return fmt.Errorf("validate deterministic pull request Tests section: %w", err)
	}
	if tests != expectedTests {
		return fmt.Errorf("agent pull request body must preserve Tests exactly as drafted")
	}
	return nil
}

func problemAndFix(commitMessageBody, planID string) (string, string) {
	body := strings.TrimSpace(commitMessageBody)
	whatPrefix := "What:\n"
	whyMarker := "\n\nWhy:\n"
	if !strings.HasPrefix(body, whatPrefix) {
		return narrativeFallback, narrativeFallback
	}
	what, why, ok := strings.Cut(strings.TrimPrefix(body, whatPrefix), whyMarker)
	if !ok || strings.TrimSpace(what) == "" || strings.TrimSpace(why) == "" {
		return narrativeFallback, narrativeFallback
	}
	return sanitize(why, planID), sanitize(what, planID)
}

func sanitize(value, planID string) string {
	if planID = strings.TrimSpace(planID); planID != "" {
		value = regexp.MustCompile(`(?i)`+regexp.QuoteMeta(planID)).ReplaceAllString(value, "")
	}
	value = squashAndMergeRE.ReplaceAllString(value, "combine the changes")
	value = mergeGuidanceRE.ReplaceAllString(value, "integration guidance")
	value = cleanupDryRunRE.ReplaceAllString(value, "cleanup preview")
	value = taoWordRE.ReplaceAllString(value, "the workflow")
	value = sliceWordRE.ReplaceAllString(value, "changes")
	value = lifecycleRE.ReplaceAllString(value, "workflow state")
	value = strings.TrimSpace(value)
	if value == "" || forbiddenNarrativeLanguage(value) != "" {
		return narrativeFallback
	}
	return escapeLevelTwoHeadings(value)
}

func forbiddenNarrativeLanguage(value string) string {
	value = strings.ToLower(value)
	for _, noise := range [...]string{"tao", "slice", "lifecycle", "squash and merge", "merge guidance", "cleanup --dry-run"} {
		if strings.Contains(value, noise) {
			return noise
		}
	}
	return ""
}

func escapeLevelTwoHeadings(value string) string {
	lines := strings.Split(value, "\n")
	var fence byte
	var fenceLength int
	for i, line := range lines {
		trimmedLeft := strings.TrimLeft(line, " \t")
		marker, length := fenceMarker(trimmedLeft)
		if fence != 0 {
			if marker == fence && length >= fenceLength && strings.TrimSpace(trimmedLeft[length:]) == "" {
				fence = 0
				fenceLength = 0
			}
			continue
		}
		if marker != 0 {
			fence = marker
			fenceLength = length
			continue
		}
		content := strings.TrimSpace(line)
		if strings.HasPrefix(content, "## ") || strings.HasPrefix(content, "##\t") {
			headingStart := strings.Index(line, "##")
			lines[i] = line[:headingStart] + `\` + line[headingStart:]
			continue
		}
		if i > 0 && strings.TrimSpace(lines[i-1]) != "" && setextLevelTwoUnderline(line) {
			underlineStart := strings.Index(line, "-")
			lines[i] = line[:underlineStart] + `\` + line[underlineStart:]
		}
	}
	return strings.Join(lines, "\n")
}

func scope(diffStat string) string {
	var b strings.Builder
	b.WriteString("<details>\n<summary>Changed files</summary>\n\n")
	if strings.TrimSpace(diffStat) != "" {
		b.WriteString("```text\n")
		b.WriteString(diffStat)
		if !strings.HasSuffix(diffStat, "\n") {
			b.WriteByte('\n')
		}
		b.WriteString("```\n\n")
	} else {
		b.WriteString("No changed-file summary is available.\n\n")
	}
	b.WriteString("</details>\n")
	return b.String()
}

func writeTests(b *strings.Builder, results []VerificationResult) {
	wrote := false
	for _, result := range results {
		command := strings.TrimSpace(result.Command)
		if command == "" || isTaoLifecycleCommand(command) {
			continue
		}
		outcome := strings.TrimSpace(result.Result)
		if outcome == "" {
			outcome = "recorded"
		}
		fmt.Fprintf(b, "- `%s`: %s\n", command, outcome)
		wrote = true
	}
	if !wrote {
		b.WriteString("No automated test results were recorded.\n")
	}
}

func testsContainTaoCommand(tests string) bool {
	for _, line := range strings.Split(tests, "\n") {
		candidate := strings.TrimSpace(line)
		candidate = strings.TrimSpace(strings.TrimLeft(candidate, "-*+0123456789. "))
		if isTaoLifecycleCommand(candidate) {
			return true
		}
		for {
			start := strings.IndexByte(candidate, '`')
			if start < 0 {
				break
			}
			candidate = candidate[start+1:]
			end := strings.IndexByte(candidate, '`')
			if end < 0 {
				break
			}
			if isTaoLifecycleCommand(strings.TrimSpace(candidate[:end])) {
				return true
			}
			candidate = candidate[end+1:]
		}
	}
	return false
}

func testsSection(body string) (string, error) {
	lines := strings.Split(strings.ReplaceAll(body, "\r\n", "\n"), "\n")
	headings, headingLines := levelTwoHeadings(lines)
	if len(headings) != 5 || headings[2] != "## Tests" || headings[3] != "## Deploy" {
		return "", fmt.Errorf("deterministic draft does not contain the required Tests section")
	}
	return strings.TrimSpace(strings.Join(lines[headingLines[2]+1:headingLines[3]], "\n")), nil
}

func levelTwoHeadings(lines []string) ([]string, []int) {
	var headings []string
	var headingLines []int
	var fence byte
	var fenceLength int
	for i, line := range lines {
		trimmedLeft := strings.TrimLeft(line, " \t")
		marker, length := fenceMarker(trimmedLeft)
		if fence != 0 {
			if marker == fence && length >= fenceLength && strings.TrimSpace(trimmedLeft[length:]) == "" {
				fence = 0
				fenceLength = 0
			}
			continue
		}
		if marker != 0 {
			fence = marker
			fenceLength = length
			continue
		}
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "## ") || strings.HasPrefix(trimmed, "##\t") {
			headings = append(headings, trimmed)
			headingLines = append(headingLines, i)
			continue
		}
		if i > 0 && setextLevelTwoUnderline(line) {
			title := strings.TrimSpace(lines[i-1])
			if title != "" {
				headings = append(headings, title)
				headingLines = append(headingLines, i)
			}
		}
	}
	return headings, headingLines
}

func setextLevelTwoUnderline(line string) bool {
	content := strings.TrimLeft(line, " ")
	if len(line)-len(content) > 3 {
		return false
	}
	content = strings.TrimRight(content, " \t")
	return content != "" && strings.Trim(content, "-") == ""
}

func fenceMarker(line string) (byte, int) {
	if len(line) < 3 || (line[0] != '`' && line[0] != '~') {
		return 0, 0
	}
	length := 1
	for length < len(line) && line[length] == line[0] {
		length++
	}
	if length < 3 {
		return 0, 0
	}
	return line[0], length
}

func isTaoLifecycleCommand(command string) bool {
	for _, fields := range verificationimpl.CommandPipelines(command) {
		if shellCommandRunsTao(fields) {
			return true
		}
	}
	return false
}

func shellCommandRunsTao(fields []string) bool {
	for i := 0; i < len(fields); {
		for i < len(fields) && isShellEnvironmentAssignment(fields[i]) {
			i++
		}
		if i >= len(fields) {
			return false
		}
		if isTaoExecutable(fields[i]) {
			return true
		}
		if !isEnvironmentExecutable(fields[i]) {
			return false
		}
		i++
	environmentPrefix:
		for i < len(fields) {
			switch {
			case fields[i] == "--":
				i++
				break environmentPrefix
			case isShellEnvironmentAssignment(fields[i]):
				i++
			case environmentOptionTakesValue(fields[i]) && i+1 < len(fields):
				i += 2
			case strings.HasPrefix(fields[i], "-"):
				i++
			default:
				break environmentPrefix
			}
		}
	}
	return false
}

func isTaoExecutable(value string) bool {
	return value == "tao" || strings.HasSuffix(value, "/tao")
}

func isEnvironmentExecutable(value string) bool {
	return value == "env" || strings.HasSuffix(value, "/env")
}

func isShellEnvironmentAssignment(value string) bool {
	name, _, ok := strings.Cut(value, "=")
	if !ok || name == "" || !isShellNameStart(name[0]) {
		return false
	}
	for i := 1; i < len(name); i++ {
		if !isShellNameStart(name[i]) && (name[i] < '0' || name[i] > '9') {
			return false
		}
	}
	return true
}

func isShellNameStart(value byte) bool {
	return (value >= 'a' && value <= 'z') || (value >= 'A' && value <= 'Z') || value == '_'
}

func environmentOptionTakesValue(value string) bool {
	return value == "-u" || value == "--unset" || value == "-C" || value == "--chdir" || value == "-S" || value == "--split-string"
}
