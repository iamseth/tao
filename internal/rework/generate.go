package rework

import (
	"encoding/json"
	"fmt"
	"path"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/internal/verifydetect"
	"github.com/iamseth/tao/internal/workspace"
	"github.com/iamseth/tao/prompts"
)

const (
	addressReviewFindingTask  = "address the review finding"
	pullRequestFindingContext = "Generated from validated pull-request feedback."
)

// RefusalError reports an ordinary rework gate that was not satisfied.
type RefusalError struct{ Message string }

func (e *RefusalError) Error() string { return e.Message }

// Gate checks whether a plan may be reopened through the ordinary, non-forced path.
func Gate(detail *plan.PlanDetail, findings []plan.ReviewFinding) error {
	id := planID(detail)
	if detail == nil {
		return refuse("rework refused: plan detail is nil")
	}
	if detail.State.Status == plan.StatusInReview {
		return refuse(fmt.Sprintf("rework refused: plan %s is awaiting review; run `tao review --run %s` first", id, id))
	}
	if !plan.ReopenableStatus(detail.State.Status) {
		return refuse(fmt.Sprintf("rework refused: plan %s is %s; only reviewed plans can be reopened", id, displayField(detail.State.Status)))
	}
	review := plan.PersistedReview(detail)
	if review == nil {
		return refuse(fmt.Sprintf("rework refused: plan %s has no persisted review; run `tao review --run %s` first or pass --force", id, id))
	}
	if review.Status != "" && review.Status != plan.ReviewStatusCompleted {
		return refuse(fmt.Sprintf("rework refused: plan %s review status is %s; expected %s", id, review.Status, plan.ReviewStatusCompleted))
	}
	if review.Verdict != plan.ReviewVerdictChangesRequested {
		return refuse(fmt.Sprintf("rework refused: plan %s review verdict is %s; expected %s", id, displayField(review.Verdict), plan.ReviewVerdictChangesRequested))
	}
	if len(findings) == 0 {
		return refuse(fmt.Sprintf("rework refused: plan %s review has no findings to convert", id))
	}
	return nil
}

// PullRequestGate checks the ordinary non-forced gates using validated
// pull-request change requests as the authority to reopen an approved PR plan.
// The review-sourced Gate remains deliberately unchanged.
func PullRequestGate(detail *plan.PlanDetail, findings []plan.ReviewFinding) error {
	if err := pullRequestPlanGate(detail); err != nil {
		return err
	}
	if len(findings) == 0 {
		return refuse(fmt.Sprintf("rework refused: plan %s pull request has no change-request threads to convert", planID(detail)))
	}
	return nil
}

func pullRequestPlanGate(detail *plan.PlanDetail) error {
	id := planID(detail)
	if detail == nil {
		return refuse("rework refused: plan detail is nil")
	}
	if detail.State.Status == plan.StatusInReview {
		return refuse(fmt.Sprintf("rework refused: plan %s is awaiting review; run `tao review --run %s` first", id, id))
	}
	if !plan.ReopenableStatus(detail.State.Status) {
		return refuse(fmt.Sprintf("rework refused: plan %s is %s; only reviewed plans can be reopened", id, displayField(detail.State.Status)))
	}
	review := plan.PersistedReview(detail)
	if review == nil {
		return refuse(fmt.Sprintf("rework refused: plan %s has no persisted review; run `tao review --run %s` first or pass --force", id, id))
	}
	if review.Status != "" && review.Status != plan.ReviewStatusCompleted {
		return refuse(fmt.Sprintf("rework refused: plan %s review status is %s; expected %s", id, review.Status, plan.ReviewStatusCompleted))
	}
	if !plan.PlanIsPullRequestComplete(detail) {
		return refuse(fmt.Sprintf("rework refused: plan %s has no current approved pull-request completion", id))
	}
	return nil
}

// Record is the lifecycle mutation surface required to reopen a plan.
type Record interface {
	Detail() *plan.PlanDetail
	Reopen([]plan.Slice, time.Time) error
}

// PullRequestRecord atomically reopens a plan while recording the thread node
// IDs converted into rework slices.
type PullRequestRecord interface {
	Detail() *plan.PlanDetail
	ReopenFromPullRequest([]plan.Slice, []string, time.Time) error
}

// Reopen applies ordinary rework gates, generates slices, and persists the reopen event.
func Reopen(record Record, now time.Time) ([]plan.Slice, error) {
	if record == nil {
		return nil, fmt.Errorf("plan record is nil")
	}
	detail := record.Detail()
	findings := ReviewFindings(detail)
	if err := Gate(detail, findings); err != nil {
		return nil, err
	}
	generationDetail := *detail
	generationDetail.State.UpdatedAt = now
	newSlices := GenerateSlices(&generationDetail, findings, nextRound(detail))
	if len(newSlices) == 0 {
		return nil, refuse(fmt.Sprintf("rework refused: plan %s has no review findings to convert", planID(detail)))
	}
	if err := record.Reopen(newSlices, now); err != nil {
		return nil, err
	}
	return newSlices, nil
}

// ReopenFromPullRequest maps the plan's persisted, validated thread triage to
// findings, applies the pull-request authority arm, and reopens the plan. Human
// thread prose is retained only in bounded untrusted packets on generated
// slices. A fresh Driver.Run started for the reopened plan records its baseline
// at the new current round, outside prior automatic-rework history.
func ReopenFromPullRequest(record PullRequestRecord, threads []PRThread, now time.Time) ([]plan.Slice, error) {
	if record == nil {
		return nil, fmt.Errorf("plan record is nil")
	}
	detail := record.Detail()
	if err := pullRequestPlanGate(detail); err != nil {
		return nil, err
	}
	changes, err := pullRequestChanges(detail, threads)
	if err != nil {
		return nil, err
	}
	findings := make([]plan.ReviewFinding, len(changes))
	for i := range changes {
		findings[i] = changes[i].finding
	}
	if err := PullRequestGate(detail, findings); err != nil {
		return nil, err
	}

	generationDetail := *detail
	generationDetail.State.UpdatedAt = now
	newSlices := GenerateSlices(&generationDetail, findings, nextRound(detail))
	if len(newSlices) != len(changes) {
		return nil, refuse(fmt.Sprintf("rework refused: plan %s pull-request findings could not all be converted", planID(detail)))
	}
	for i := range newSlices {
		newSlices[i].Goal = pullRequestFindingGoal(changes[i].finding.File)
		newSlices[i].Context = pullRequestFindingContext + "\n\n" + changes[i].packet
	}
	consumedThreadIDs := make([]string, len(changes))
	for i := range changes {
		consumedThreadIDs[i] = changes[i].threadID
	}
	if err := record.ReopenFromPullRequest(newSlices, consumedThreadIDs, now); err != nil {
		return nil, err
	}
	return newSlices, nil
}

type pullRequestChange struct {
	threadID string
	finding  plan.ReviewFinding
	packet   string
}

func pullRequestChanges(detail *plan.PlanDetail, threads []PRThread) ([]pullRequestChange, error) {
	id := planID(detail)
	if detail == nil {
		return nil, refuse("rework refused: plan detail is nil")
	}
	triage := detail.State.Plan.PRFeedbackTriage
	if len(triage) == 0 {
		return nil, refuse(fmt.Sprintf("rework refused: plan %s has no persisted pull-request feedback triage", id))
	}
	if len(triage) != len(threads) {
		return nil, refuse(fmt.Sprintf("rework refused: plan %s pull-request feedback triage does not match the current thread set", id))
	}

	changes := make([]pullRequestChange, 0, len(threads))
	seen := make(map[string]struct{}, len(threads))
	for _, thread := range threads {
		threadID := strings.TrimSpace(thread.NodeID)
		if threadID == "" {
			return nil, refuse(fmt.Sprintf("rework refused: plan %s pull-request thread is missing its node ID", id))
		}
		if _, duplicate := seen[threadID]; duplicate {
			return nil, refuse(fmt.Sprintf("rework refused: plan %s pull-request thread %q is duplicated", id, threadID))
		}
		seen[threadID] = struct{}{}
		classification, ok := triage[threadID]
		if !ok {
			return nil, refuse(fmt.Sprintf("rework refused: plan %s pull-request thread %q has no persisted classification", id, threadID))
		}
		switch PRThreadKind(strings.TrimSpace(classification.Kind)) {
		case PRThreadKindQuestion, PRThreadKindScope:
			continue
		case PRThreadKindUnmappable:
			return nil, refuse(fmt.Sprintf("rework refused: pull-request thread %q is unmappable: %s", threadID, displayField(classification.Rationale)))
		case PRThreadKindChange:
			if slices.Contains(detail.State.Plan.PRFeedbackConsumedThreadIDs, threadID) {
				continue
			}
		default:
			return nil, refuse(fmt.Sprintf("rework refused: pull-request thread %q has unsupported classification %q", threadID, classification.Kind))
		}

		file, ok := normalizeReviewFindingFile(thread.Path)
		if !ok {
			return nil, refuse(fmt.Sprintf("rework refused: pull-request change thread %q has an unsafe or unmappable file %q", threadID, thread.Path))
		}
		encoded, err := json.Marshal(threadPacket(thread))
		if err != nil {
			return nil, fmt.Errorf("encode pull-request thread %q: %w", threadID, err)
		}
		packet, err := prompts.RenderPRThreadPackets([]string{string(encoded)})
		if err != nil {
			return nil, fmt.Errorf("render pull-request thread %q: %w", threadID, err)
		}
		line := 0
		if thread.Line != nil && *thread.Line > 0 {
			line = *thread.Line
		}
		changes = append(changes, pullRequestChange{
			threadID: threadID,
			finding: plan.ReviewFinding{
				Severity: "pull-request",
				File:     file,
				Line:     line,
				Message:  pullRequestFindingGoal(file),
			},
			packet: strings.TrimSpace(packet),
		})
	}
	return changes, nil
}

func pullRequestFindingGoal(file string) string {
	if file == "" {
		return "Address pull-request change request"
	}
	return "Address pull-request change request in " + file
}

func refuse(message string) error { return &RefusalError{Message: message} }

func nextRound(detail *plan.PlanDetail) int { return RoundCount(detail) + 1 }

// RoundFromSliceID returns the positive round encoded before the final two
// index characters in a persisted r<round><NN>- rework slice ID.
func RoundFromSliceID(id string) int {
	return plan.ReworkRoundFromSliceID(id)
}

// RoundCount returns the highest deterministic r<round> rework slice round.
func RoundCount(detail *plan.PlanDetail) int {
	maxRound := 0
	if detail != nil {
		for _, slice := range detail.Slices.Slices {
			if round := RoundFromSliceID(slice.ID); round > maxRound {
				maxRound = round
			}
		}
	}
	return maxRound
}

func planID(detail *plan.PlanDetail) string {
	if detail == nil || strings.TrimSpace(detail.State.Plan.ID) == "" {
		return "plan"
	}
	return strings.TrimSpace(detail.State.Plan.ID)
}

func displayField(value string) string {
	if strings.TrimSpace(value) == "" {
		return "<empty>"
	}
	return strings.TrimSpace(value)
}

// GenerateSlices turns review findings into deterministic pending rework slices.
func GenerateSlices(detail *plan.PlanDetail, findings []plan.ReviewFinding, round int) []plan.Slice {
	if len(findings) == 0 {
		findings = ReviewFindings(detail)
	}
	if len(findings) == 0 {
		return nil
	}

	slices := make([]plan.Slice, 0, len(findings))
	for _, finding := range findings {
		file, ok := normalizeReviewFindingFile(finding.File)
		if !ok {
			continue
		}
		verification := reworkVerification(detail, file)
		slice := plan.Slice{
			ID:            sliceID(round, len(slices)+1, file),
			Title:         sliceTitle(file),
			Status:        plan.StatusPending,
			DependsOn:     []string{},
			Timing:        sliceTiming(detail),
			Goal:          sliceGoal(finding, file),
			Context:       sliceContext(finding, file),
			Tasks:         findingTasks(finding),
			ExpectedFiles: reworkExpectedFiles(detail, file),
			Verification: plan.Verification{
				Commands:     verification.commands,
				Source:       verification.source,
				ManualChecks: []string{},
			},
		}
		slices = append(slices, slice)
	}
	return slices
}

func sliceID(round int, index int, file string) string {
	return fmt.Sprintf("r%d%02d-%s", round, index, fileSlug(file))
}

func fileSlug(file string) string {
	value := normalizePlanPath(file)
	if value == "" {
		value = "finding"
	}
	var b strings.Builder
	lastDash := false
	for _, r := range strings.ToLower(value) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash && b.Len() > 0 {
			b.WriteByte('-')
			lastDash = true
		}
	}
	slug := strings.Trim(b.String(), "-")
	if slug == "" {
		return "finding"
	}
	return slug
}

func sliceTitle(file string) string {
	if file == "" {
		return "Address review finding"
	}
	return "Address review finding in " + file
}

func sliceGoal(finding plan.ReviewFinding, file string) string {
	if message := strings.TrimSpace(finding.Message); message != "" {
		return message
	}
	if file != "" {
		return "Address review finding in " + file
	}
	return "Address review finding"
}

func sliceContext(finding plan.ReviewFinding, file string) string {
	parts := []string{"Generated from a persisted plan review finding."}
	if finding.Severity != "" {
		parts = append(parts, "Severity: "+strings.TrimSpace(finding.Severity)+".")
	}
	if file != "" {
		location := file
		if finding.Line > 0 {
			location = fmt.Sprintf("%s:%d", file, finding.Line)
		}
		parts = append(parts, "Location: "+location+".")
	}
	return strings.Join(parts, " ")
}

func findingTasks(finding plan.ReviewFinding) []string {
	tasks := []string{addressReviewFindingTask}
	for _, task := range suggestionTasks(finding.Suggestion) {
		if task != "" {
			tasks = append(tasks, task)
		}
	}
	return tasks
}

func suggestionTasks(suggestion string) []string {
	var tasks []string
	for line := range strings.SplitSeq(suggestion, "\n") {
		line = cleanSuggestionLine(line)
		if line != "" {
			tasks = append(tasks, line)
		}
	}
	return tasks
}

func cleanSuggestionLine(line string) string {
	line = strings.TrimSpace(line)
	line = strings.TrimPrefix(line, "- ")
	line = strings.TrimPrefix(line, "* ")
	line = strings.TrimSpace(line)
	if len(line) > 2 && line[1] == '.' && line[0] >= '0' && line[0] <= '9' {
		line = strings.TrimSpace(line[2:])
	}
	return line
}

func sliceTiming(detail *plan.PlanDetail) plan.SliceTiming {
	if detail == nil || detail.State.UpdatedAt.IsZero() {
		return plan.SliceTiming{}
	}
	return plan.SliceTiming{CreatedAt: detail.State.UpdatedAt, UpdatedAt: detail.State.UpdatedAt}
}

func reworkExpectedFiles(detail *plan.PlanDetail, file string) []string {
	files := []string{file}
	if detail == nil {
		return files
	}
	for _, slice := range detail.Slices.Slices {
		if !sliceExpectedFilesOverlap(slice.ExpectedFiles, file) {
			continue
		}
		for _, expected := range slice.ExpectedFiles {
			if normalized, ok := normalizeReviewFindingFile(expected); ok {
				files = appendUnique(files, normalized)
			}
		}
	}
	return files
}

type verificationResolution struct {
	commands []string
	source   string
}

func reworkVerification(detail *plan.PlanDetail, file string) verificationResolution {
	inheritedCommands := overlappingVerificationCommands(detail, file)
	commands := appendUnique(nil, inheritedCommands...)
	packageCommands := packageVerificationCommands(detail, file)
	commands = appendUnique(commands, packageCommands...)
	if len(commands) > 0 {
		source := "overlapping original slice verification"
		if len(packageCommands) > 0 {
			if len(inheritedCommands) == 0 {
				source = "applicable Go package verification"
			} else {
				source += " plus applicable Go package verification"
			}
		}
		return verificationResolution{commands: commands, source: source}
	}

	if detector, ok := verificationDetector(detail); ok {
		if detected := detector.DetectCommands(); len(detected) > 0 {
			return verificationResolution{
				commands: appendUnique(nil, detected...),
				source:   "detected repository build/test verification",
			}
		}
	}
	return verificationResolution{
		commands: []string{"git diff --check -- " + shellQuote(file)},
		source:   "file-scoped Git diff check only; does not provide semantic test coverage",
	}
}

func packageVerificationCommands(detail *plan.PlanDetail, file string) []string {
	cleanFile := normalizePlanPath(file)
	if cleanFile == "" || path.Ext(cleanFile) != ".go" {
		return nil
	}
	detector, ok := verificationDetector(detail)
	if !ok {
		return nil
	}
	moduleDir, ok := detector.GoModuleForPath(cleanFile)
	if !ok {
		return nil
	}

	packageDir := path.Dir(cleanFile)
	if moduleDir != "." {
		packageDir = strings.TrimPrefix(packageDir, moduleDir)
		packageDir = strings.TrimPrefix(packageDir, "/")
	}
	target := "."
	if packageDir != "." && packageDir != "" {
		target = "./" + packageDir
	}
	command := "go test " + shellWord(target)
	if moduleDir != "." {
		command = "cd " + shellWord(moduleDir) + " && " + command
	}
	return []string{command}
}

func shellWord(value string) string {
	if value != "" && strings.IndexFunc(value, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r) && !strings.ContainsRune("_@%+=:,./-", r)
	}) == -1 {
		return value
	}
	return shellQuote(value)
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'"'"'`) + "'"
}

func verificationDetector(detail *plan.PlanDetail) (verifydetect.Detector, bool) {
	if detail == nil {
		return verifydetect.Detector{}, false
	}
	workspaceState := detail.State.Workspace
	if workspaceState == nil || strings.TrimSpace(workspaceState.Strategy) == plan.WorkspaceStrategyCurrent {
		return verifydetect.OpenRoot(detail.State.Repo.Root)
	}

	recorded := workspace.ResolveRecordedWorktree(detail)
	if detector, ok := verifydetect.OpenRoot(recorded.Path); ok {
		return detector, true
	}
	if strings.TrimSpace(workspaceState.Strategy) == "" && recorded.Path == "" {
		return verifydetect.OpenRoot(detail.State.Repo.Root)
	}
	return verifydetect.Detector{}, false
}

func overlappingVerificationCommands(detail *plan.PlanDetail, file string) []string {
	if detail == nil {
		return nil
	}
	commands := make([]string, 0)
	for _, slice := range detail.Slices.Slices {
		if RoundFromSliceID(slice.ID) > 0 || !sliceExpectedFilesOverlap(slice.ExpectedFiles, file) {
			continue
		}
		commands = append(commands, slice.Verification.Commands...)
	}
	return commands
}

func sliceExpectedFilesOverlap(expectedFiles []string, file string) bool {
	for _, expected := range expectedFiles {
		if pathsOverlap(file, expected) {
			return true
		}
	}
	return false
}

func pathsOverlap(file string, expected string) bool {
	cleanFile := normalizePlanPath(file)
	cleanExpected := normalizePlanPath(expected)
	if cleanFile == "" || cleanExpected == "" {
		return false
	}
	if cleanFile == cleanExpected {
		return true
	}
	if strings.HasSuffix(strings.TrimSpace(expected), "/") {
		return strings.HasPrefix(cleanFile, cleanExpected+"/")
	}
	if path.Ext(cleanExpected) == "" && strings.HasPrefix(cleanFile, cleanExpected+"/") {
		return true
	}
	return false
}

func normalizeReviewFindingFile(value string) (string, bool) {
	value = strings.TrimSpace(strings.ReplaceAll(value, "\\", "/"))
	for strings.HasPrefix(value, "./") {
		value = strings.TrimPrefix(value, "./")
	}
	if value == "" || strings.HasPrefix(value, "/") || hasWindowsDrivePrefix(value) || hasParentPathSegment(value) || hasWildcardPathSegment(value) {
		return "", false
	}
	clean := path.Clean(value)
	if clean == "." || clean == "" || clean == "..." || strings.HasPrefix(clean, "../") || strings.HasSuffix(clean, "/...") || strings.Contains(clean, "/.../") {
		return "", false
	}
	return clean, true
}

func normalizePlanPath(value string) string {
	value = strings.TrimSpace(strings.ReplaceAll(value, "\\", "/"))
	value = strings.TrimPrefix(value, "./")
	value = strings.Trim(value, "/")
	if value == "" {
		return ""
	}
	clean := path.Clean(value)
	if clean == "." {
		return ""
	}
	return clean
}

func hasWindowsDrivePrefix(value string) bool {
	return len(value) >= 2 && value[1] == ':' && unicode.IsLetter(rune(value[0]))
}

func hasParentPathSegment(value string) bool {
	return slices.Contains(strings.Split(value, "/"), "..")
}

func hasWildcardPathSegment(value string) bool {
	return strings.ContainsAny(value, "*?[]{}") || value == "..." || strings.HasPrefix(value, ".../") || strings.HasSuffix(value, "/...") || strings.Contains(value, "/.../")
}

func appendUnique(commands []string, values ...string) []string {
	seen := make(map[string]bool, len(commands)+len(values))
	for _, command := range commands {
		seen[command] = true
	}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		commands = append(commands, value)
		seen[value] = true
	}
	return commands
}
