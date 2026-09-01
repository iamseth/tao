package insights

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/iamseth/tao/internal/agent/logrecord"
)

const (
	logLookback        = 30 * 24 * time.Hour
	maxLogCandidates   = 200
	maxLogBytes        = 4 << 20
	maxTotalLogBytes   = 32 << 20
	maxLogLineBytes    = 64 << 10
	maxSignals         = 10_000
	maxLogExcerptBytes = 160
	maxSignalExemplars = 2
)

// RecentLogReport contains bounded, normalized evidence from recent agent logs.
// It never contains complete provider output or raw tool payloads.
type RecentLogReport struct {
	Coverage           LogCoverage             `json:"coverage"`
	Repositories       []RepositoryLogCoverage `json:"repositories,omitempty"`
	MissingExecutables []LogSignal             `json:"missing_executables,omitempty"`
	ToolUses           []LogSignal             `json:"tool_uses,omitempty"`
	ExternalSystems    []LogSignal             `json:"external_systems,omitempty"`
}

// RepositoryLogCoverage qualifies recent-log coverage by repository.
type RepositoryLogCoverage struct {
	RepositoryID   string      `json:"repository_id"`
	RepositoryName string      `json:"repository_name,omitempty"`
	Coverage       LogCoverage `json:"coverage"`
}

// LogCoverage explains which plan logs were eligible for recent-log analysis.
type LogCoverage struct {
	Eligible       int `json:"eligible"`
	Scanned        int `json:"scanned"`
	MissingRecency int `json:"missing_recency"`
	OutsideWindow  int `json:"outside_window"`
	Missing        int `json:"missing"`
	Unreadable     int `json:"unreadable"`
	Unsupported    int `json:"unsupported"`
	Oversized      int `json:"oversized"`
	WorkLimited    int `json:"work_limited"`
}

// LogSignal is a normalized signal with concentration and bounded evidence.
type LogSignal struct {
	Name            string        `json:"name"`
	Count           int           `json:"count"`
	PlanCount       int           `json:"plan_count"`
	RepositoryCount int           `json:"repository_count"`
	Exemplars       []LogExemplar `json:"exemplars,omitempty"`
}

// LogExemplar attributes a short redacted excerpt without retaining raw logs.
type LogExemplar struct {
	RepositoryID   string `json:"repository_id,omitempty"`
	RepositoryName string `json:"repository_name,omitempty"`
	PlanID         string `json:"plan_id"`
	Excerpt        string `json:"excerpt"`
}

type logAccumulator struct {
	missing            map[string]*logSignalAccumulator
	tools              map[string]*logSignalAccumulator
	external           map[string]*logSignalAccumulator
	candidates         []logCandidate
	repositoryCoverage map[string]*RepositoryLogCoverage
	bytesRead          int64
	signals            int
	scannedCandidates  int
}

type logCandidate struct {
	repository   sourceIdentity
	planID       string
	dir          string
	lastActivity time.Time
}

type logCoverageClass int

const (
	logEligible logCoverageClass = iota
	logScanned
	logMissingRecency
	logOutsideWindow
	logMissing
	logUnreadable
	logUnsupported
	logOversized
	logWorkLimited
)

type logSignalAccumulator struct {
	count        int
	plans        map[string]struct{}
	repositories map[string]struct{}
	exemplars    []LogExemplar
}

type logAttribution struct {
	repository sourceIdentity
	planID     string
}

var (
	credentialPattern     = regexp.MustCompile(`(?i)(authorization\s*[:=]\s*(?:bearer\s+)?|(?:api[_-]?key|access[_-]?token|password|passwd|secret)\s*[:=]\s*)[^\s,;]+`)
	flagCredentialPattern = regexp.MustCompile(`(?i)(--?(?:api[_-]?key|access[_-]?token|token|password|passwd|secret)\s+)[^\s,;]+`)
	secretValuePattern    = regexp.MustCompile(`\b(?:gh[pousr]_[A-Za-z0-9]{16,}|sk-[A-Za-z0-9_-]{16,})\b`)
	urlPattern            = regexp.MustCompile(`https?://[^\s"'<>]+`)
	missingPatterns       = []*regexp.Regexp{
		regexp.MustCompile(`(?i)^(?:(?:bash|sh|zsh|dash|fish|ksh):\s*(?:(?:line\s+)?\d+:\s*)?)?([a-z0-9][a-z0-9._+-]*):\s*(?:command not found|not found)$`),
		regexp.MustCompile(`(?i)^(?:exec|spawn)(?:vp)?\s*(?:[(:]\s*)?["']?([a-z0-9][a-z0-9._+-]*)["']?(?:\s*\))?\s*:?\s*(?:enoent|executable file not found(?:\s+in\s+\$path)?)$`),
		regexp.MustCompile(`(?i)^["']([a-z0-9][a-z0-9._+-]*)["']:\s*executable file not found(?:\s+in\s+\$path)?$`),
	}
)

func newLogAccumulator() *logAccumulator {
	return &logAccumulator{
		missing:            make(map[string]*logSignalAccumulator),
		tools:              make(map[string]*logSignalAccumulator),
		external:           make(map[string]*logSignalAccumulator),
		repositoryCoverage: make(map[string]*RepositoryLogCoverage),
	}
}

func discoverRecentLog(report *Report, acc *logAccumulator, now time.Time, summary planSummary, repository sourceIdentity) {
	if summary.lastActivity == nil || summary.lastActivity.IsZero() {
		classifyLogCoverage(report, acc, repository, logMissingRecency)
		return
	}
	if summary.lastActivity.Before(now.Add(-logLookback)) {
		classifyLogCoverage(report, acc, repository, logOutsideWindow)
		return
	}
	classifyLogCoverage(report, acc, repository, logEligible)
	acc.candidates = append(acc.candidates, logCandidate{
		repository:   repository,
		planID:       summary.id,
		dir:          summary.dir,
		lastActivity: *summary.lastActivity,
	})
}

func scanRecentLogs(ctx context.Context, report *Report, acc *logAccumulator, now time.Time) error {
	for _, round := range balancedLogRounds(acc.candidates) {
		for _, candidate := range round {
			if err := ctx.Err(); err != nil {
				return err
			}
			if acc.scannedCandidates >= maxLogCandidates || acc.bytesRead >= maxTotalLogBytes || acc.signals >= maxSignals {
				classifyLogCoverage(report, acc, candidate.repository, logWorkLimited)
				continue
			}
			acc.scannedCandidates++
			if err := scanRecentLog(ctx, report, acc, now, candidate); err != nil {
				return err
			}
		}
	}
	return nil
}

func balancedLogRounds(candidates []logCandidate) [][]logCandidate {
	groups := make(map[string][]logCandidate)
	for _, candidate := range candidates {
		key := logRepositoryKey(candidate.repository)
		groups[key] = append(groups[key], candidate)
	}
	keys := make([]string, 0, len(groups))
	for key, group := range groups {
		sort.SliceStable(group, func(i, j int) bool {
			if !group[i].lastActivity.Equal(group[j].lastActivity) {
				return group[i].lastActivity.After(group[j].lastActivity)
			}
			if group[i].planID != group[j].planID {
				return group[i].planID < group[j].planID
			}
			return group[i].dir < group[j].dir
		})
		groups[key] = group
		keys = append(keys, key)
	}
	sort.Strings(keys)

	var rounds [][]logCandidate
	for index := 0; ; index++ {
		var round []logCandidate
		for _, key := range keys {
			if index < len(groups[key]) {
				round = append(round, groups[key][index])
			}
		}
		if len(round) == 0 {
			return rounds
		}
		sort.SliceStable(round, func(i, j int) bool {
			if !round[i].lastActivity.Equal(round[j].lastActivity) {
				return round[i].lastActivity.After(round[j].lastActivity)
			}
			if round[i].repository.id != round[j].repository.id {
				return round[i].repository.id < round[j].repository.id
			}
			if round[i].repository.name != round[j].repository.name {
				return round[i].repository.name < round[j].repository.name
			}
			if round[i].planID != round[j].planID {
				return round[i].planID < round[j].planID
			}
			return round[i].dir < round[j].dir
		})
		rounds = append(rounds, round)
	}
}

func scanRecentLog(ctx context.Context, report *Report, acc *logAccumulator, now time.Time, candidate logCandidate) error {
	file, err := os.Open(filepath.Join(candidate.dir, "agent-run.log")) // #nosec G304 -- paths come from Tao's plan repository listing.
	if errors.Is(err, os.ErrNotExist) {
		classifyLogCoverage(report, acc, candidate.repository, logMissing)
		return nil
	}
	if err != nil {
		classifyLogCoverage(report, acc, candidate.repository, logUnreadable)
		return nil
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		classifyLogCoverage(report, acc, candidate.repository, logUnreadable)
		return nil
	}
	if info.Size() > maxLogBytes {
		classifyLogCoverage(report, acc, candidate.repository, logOversized)
		return nil
	}
	if info.Size() > int64(maxTotalLogBytes)-acc.bytesRead {
		classifyLogCoverage(report, acc, candidate.repository, logWorkLimited)
		return nil
	}

	remaining := min(int64(maxLogBytes), int64(maxTotalLogBytes)-acc.bytesRead)
	reader := &countingLimitedReader{reader: file, remaining: remaining + 1}
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 4096), maxLogLineBytes)
	var records []logrecord.Record
	framed := true
	started := false
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return err
		}
		line := scanner.Text()
		if strings.TrimSpace(line) == "" {
			continue
		}
		record, ok := logrecord.Parse(line)
		if !ok || (!started && record.Type != logrecord.TypeSession) {
			framed = false
			break
		}
		started = true
		records = append(records, record)
	}
	acc.bytesRead += reader.read
	if scanErr := scanner.Err(); scanErr != nil {
		if reader.read > remaining || strings.Contains(scanErr.Error(), "token too long") {
			classifyLogCoverage(report, acc, candidate.repository, logOversized)
		} else {
			classifyLogCoverage(report, acc, candidate.repository, logUnreadable)
		}
		return nil
	}
	if reader.read > remaining {
		classifyLogCoverage(report, acc, candidate.repository, logOversized)
		return nil
	}
	if !framed || !started {
		classifyLogCoverage(report, acc, candidate.repository, logUnsupported)
		return nil
	}
	consumeLogRecords(acc, logAttribution{repository: candidate.repository, planID: candidate.planID}, records, now.Add(-logLookback))
	classifyLogCoverage(report, acc, candidate.repository, logScanned)
	return nil
}

func classifyLogCoverage(report *Report, acc *logAccumulator, repository sourceIdentity, class logCoverageClass) {
	incrementLogCoverage(&report.RecentLogs.Coverage, class)
	if repository.id == "" {
		return
	}
	key := logRepositoryKey(repository)
	detail := acc.repositoryCoverage[key]
	if detail == nil {
		detail = &RepositoryLogCoverage{RepositoryID: repository.id, RepositoryName: repository.name}
		acc.repositoryCoverage[key] = detail
	}
	incrementLogCoverage(&detail.Coverage, class)
}

func incrementLogCoverage(coverage *LogCoverage, class logCoverageClass) {
	switch class {
	case logEligible:
		coverage.Eligible++
	case logScanned:
		coverage.Scanned++
	case logMissingRecency:
		coverage.MissingRecency++
	case logOutsideWindow:
		coverage.OutsideWindow++
	case logMissing:
		coverage.Missing++
	case logUnreadable:
		coverage.Unreadable++
	case logUnsupported:
		coverage.Unsupported++
	case logOversized:
		coverage.Oversized++
	case logWorkLimited:
		coverage.WorkLimited++
	}
}

func logRepositoryKey(repository sourceIdentity) string {
	if repository.id != "" {
		return repository.id
	}
	return "\x00" + repository.name
}

func consumeLogRecords(acc *logAccumulator, attr logAttribution, records []logrecord.Record, cutoff time.Time) {
	activeSession := false
	var activeExecutables []string
	for _, record := range records {
		switch record.Type {
		case logrecord.TypeSession:
			timestamp, err := time.Parse(time.RFC3339, record.Timestamp)
			activeSession = err == nil && !timestamp.Before(cutoff)
			activeExecutables = nil
		case logrecord.TypeToolCall:
			if activeSession {
				activeExecutables = consumeToolCall(acc, attr, strings.TrimSpace(record.Name+" "+record.Payload))
			}
		case logrecord.TypeToolResult:
			if activeSession && record.Failed && len(activeExecutables) > 0 && !containsRecordLikeOutput(record.Content) {
				for line := range strings.Lines(record.Content) {
					consumeExecutionOutput(acc, attr, strings.TrimSpace(line), activeExecutables)
				}
			}
			activeExecutables = nil
		default:
			activeExecutables = nil
		}
		if acc.signals >= maxSignals {
			return
		}
	}
}

// planSummary keeps log extraction independent from the larger lifecycle model.
type planSummary struct {
	id           string
	dir          string
	lastActivity *time.Time
}

type countingLimitedReader struct {
	reader    io.Reader
	remaining int64
	read      int64
}

func (r *countingLimitedReader) Read(p []byte) (int, error) {
	if r.remaining <= 0 {
		return 0, io.EOF
	}
	if int64(len(p)) > r.remaining {
		p = p[:r.remaining]
	}
	n, err := r.reader.Read(p)
	r.remaining -= int64(n)
	r.read += int64(n)
	return n, err
}

func containsRecordLikeOutput(content string) bool {
	for line := range strings.Lines(content) {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "→ ") || strings.HasPrefix(line, "✓ ") || strings.HasPrefix(line, "✗ ") || strings.HasPrefix(line, logrecord.Prefix) {
			return true
		}
	}
	return false
}

func consumeToolCall(acc *logAccumulator, attr logAttribution, value string) []string {
	tool, payload, _ := strings.Cut(value, " ")
	tool = normalizeName(tool)
	if tool == "" {
		return nil
	}
	command, shell := executionCommand(tool, payload)
	if shell {
		if command == "" {
			return nil
		}
		names := commandNames(command)
		for _, name := range names {
			addLogSignal(acc, acc.tools, name, attr, "tool")
			if system := commandSystem(name); system != "" {
				addLogSignal(acc, acc.external, system, attr, "external system")
			}
		}
		for _, system := range commandURLSystems(command) {
			addLogSignal(acc, acc.external, system, attr, "external system")
		}
		return names
	}
	addLogSignal(acc, acc.tools, tool, attr, "tool")
	return nil
}

func executionCommand(tool, payload string) (string, bool) {
	switch tool {
	case "bash", "shell", "terminal", "exec", "execute", "command":
	default:
		return "", false
	}
	var values map[string]any
	if json.Unmarshal([]byte(payload), &values) != nil {
		return "", true
	}
	for _, key := range []string{"command", "cmd", "script"} {
		if command, ok := values[key].(string); ok {
			return command, true
		}
	}
	return "", true
}

func commandNames(command string) []string {
	invocations, ok := shellInvocations(command)
	if !ok {
		return nil
	}
	var names []string
	for _, invocation := range invocations {
		for len(invocation) > 0 && shellAssignment(invocation[0]) {
			invocation = invocation[1:]
		}
		if len(invocation) == 0 {
			continue
		}
		name := normalizeName(filepath.Base(invocation[0]))
		if name == "sudo" {
			if len(invocation) < 2 || strings.HasPrefix(invocation[1], "-") {
				continue
			}
			name = normalizeName(filepath.Base(invocation[1]))
		}
		if name != "" && !slices.Contains(names, name) {
			names = append(names, name)
		}
	}
	return names
}

func commandSystem(command string) string {
	switch command {
	case "gh":
		return "github.com"
	case "aws":
		return "aws"
	case "gcloud":
		return "google-cloud"
	case "kubectl":
		return "kubernetes"
	case "docker":
		return "docker"
	case "terraform":
		return "terraform"
	default:
		return ""
	}
}

func commandURLSystems(command string) []string {
	invocations, ok := shellInvocations(command)
	if !ok {
		return nil
	}
	var systems []string
	for _, invocation := range invocations {
		name, arguments, ok := networkCommand(invocation)
		if !ok {
			continue
		}
		for _, operand := range networkURLOperands(name, arguments) {
			for _, system := range URLSystems(operand) {
				if !slices.Contains(systems, system) {
					systems = append(systems, system)
				}
			}
		}
	}
	return systems
}

func shellInvocations(command string) ([][]string, bool) {
	var invocations [][]string
	var words []string
	var word strings.Builder
	var quote rune
	escaped := false
	flushWord := func() {
		if word.Len() > 0 {
			words = append(words, word.String())
			word.Reset()
		}
	}
	flushInvocation := func() {
		flushWord()
		if len(words) > 0 {
			invocations = append(invocations, words)
			words = nil
		}
	}
	for index, character := range command {
		if escaped {
			word.WriteRune(character)
			escaped = false
			continue
		}
		if character == '\\' && quote != '\'' {
			escaped = true
			continue
		}
		if quote != 0 {
			if character == quote {
				quote = 0
			} else {
				word.WriteRune(character)
			}
			continue
		}
		switch character {
		case '\'', '"':
			quote = character
		case '`':
			return nil, false
		case '$':
			if index+1 < len(command) && command[index+1] == '(' {
				return nil, false
			}
			word.WriteRune(character)
		case ' ', '\t', '\r':
			flushWord()
		case ';', '|', '&', '\n':
			flushInvocation()
		case '(', ')', '<', '>':
			return nil, false
		case '#':
			if word.Len() == 0 {
				return nil, false
			}
			word.WriteRune(character)
		default:
			word.WriteRune(character)
		}
	}
	if escaped || quote != 0 {
		return nil, false
	}
	flushInvocation()
	return invocations, true
}

func networkCommand(invocation []string) (string, []string, bool) {
	for len(invocation) > 0 && shellAssignment(invocation[0]) {
		invocation = invocation[1:]
	}
	if len(invocation) == 0 {
		return "", nil, false
	}
	name := normalizeName(filepath.Base(invocation[0]))
	if name == "sudo" {
		if len(invocation) < 2 || strings.HasPrefix(invocation[1], "-") {
			return "", nil, false
		}
		invocation = invocation[1:]
		name = normalizeName(filepath.Base(invocation[0]))
	}
	if name != "curl" && name != "wget" {
		return "", nil, false
	}
	return name, invocation[1:], true
}

func shellAssignment(value string) bool {
	name, _, ok := strings.Cut(value, "=")
	if !ok || name == "" {
		return false
	}
	for index, character := range name {
		if character != '_' && !unicode.IsLetter(character) && (index == 0 || !unicode.IsDigit(character)) {
			return false
		}
	}
	return true
}

func networkURLOperands(command string, arguments []string) []string {
	var operands []string
	optionNeedsValue := false
	optionValueIsURL := false
	positional := false
	for _, argument := range arguments {
		if argument == "--" {
			positional = true
			optionNeedsValue = false
			continue
		}
		if !positional && strings.HasPrefix(argument, "--url=") {
			value := strings.TrimPrefix(argument, "--url=")
			if exactURL(value) {
				operands = append(operands, value)
			}
			optionNeedsValue = false
			continue
		}
		if !positional && argument == "--url" {
			optionNeedsValue = true
			optionValueIsURL = true
			continue
		}
		if !positional && strings.HasPrefix(argument, "-") && argument != "-" {
			longWithoutValue := strings.HasPrefix(argument, "--") && !strings.Contains(argument, "=")
			shortWithoutValue := !strings.HasPrefix(argument, "--") && len(argument) == 2
			optionNeedsValue = !networkFlagWithoutValue(command, argument) && (longWithoutValue || shortWithoutValue)
			optionValueIsURL = false
			continue
		}
		if exactURL(argument) && (!optionNeedsValue || optionValueIsURL) {
			operands = append(operands, argument)
		}
		optionNeedsValue = false
		optionValueIsURL = false
	}
	return operands
}

func networkFlagWithoutValue(command, argument string) bool {
	switch command {
	case "curl":
		if strings.HasPrefix(argument, "--") {
			switch argument {
			case "--compressed", "--fail", "--fail-with-body", "--head", "--include", "--insecure", "--location", "--silent", "--show-error", "--verbose":
				return true
			default:
				return false
			}
		}
		return strings.Trim(argument, "-fILlsSkv") == ""
	case "wget":
		if strings.HasPrefix(argument, "--") {
			switch argument {
			case "--continue", "--no-check-certificate", "--quiet", "--server-response", "--spider", "--verbose":
				return true
			default:
				return false
			}
		}
		return strings.Trim(argument, "-cqS") == ""
	default:
		return false
	}
}

func exactURL(value string) bool {
	value = strings.TrimRight(value, ").,;")
	match := urlPattern.FindStringIndex(value)
	return match != nil && match[0] == 0 && match[1] == len(value)
}

func URLSystems(value string) []string {
	var systems []string
	for _, raw := range urlPattern.FindAllString(value, -1) {
		parsed, err := url.Parse(strings.TrimRight(raw, ").,;"))
		if err != nil || parsed.Hostname() == "" {
			continue
		}
		host := strings.ToLower(parsed.Hostname())
		if !slices.Contains(systems, host) {
			systems = append(systems, host)
		}
	}
	return systems
}

func consumeExecutionOutput(acc *logAccumulator, attr logAttribution, line string, attempted []string) {
	for _, pattern := range missingPatterns {
		match := pattern.FindStringSubmatch(line)
		if len(match) > 1 {
			name := normalizeName(match[1])
			if slices.Contains(attempted, name) {
				addLogSignal(acc, acc.missing, name, attr, "missing executable")
			}
			return
		}
	}
}

func addLogSignal(acc *logAccumulator, target map[string]*logSignalAccumulator, name string, attr logAttribution, exemplarLabel string) {
	if !safeSignalName(name) || acc.signals >= maxSignals {
		return
	}
	signal := target[name]
	if signal == nil {
		signal = &logSignalAccumulator{plans: make(map[string]struct{}), repositories: make(map[string]struct{})}
		target[name] = signal
	}
	signal.count++
	acc.signals++
	planKey := attr.repository.id + "\x00" + attr.planID
	signal.plans[planKey] = struct{}{}
	signal.repositories[attr.repository.id] = struct{}{}
	exemplar := LogExemplar{RepositoryID: attr.repository.id, RepositoryName: attr.repository.name, PlanID: attr.planID, Excerpt: sanitizeExcerpt(exemplarLabel + ": " + name)}
	if exemplar.Excerpt != "" && len(signal.exemplars) < maxSignalExemplars && !slices.Contains(signal.exemplars, exemplar) {
		signal.exemplars = append(signal.exemplars, exemplar)
	}
}

func sanitizeExcerpt(value string) string {
	value = credentialPattern.ReplaceAllString(value, "${1}[REDACTED]")
	value = flagCredentialPattern.ReplaceAllString(value, "${1}[REDACTED]")
	value = secretValuePattern.ReplaceAllString(value, "[REDACTED]")
	value = urlPattern.ReplaceAllStringFunc(value, func(raw string) string {
		parsed, err := url.Parse(strings.TrimRight(raw, ").,;"))
		if err != nil || parsed.Hostname() == "" {
			return "[REDACTED_URL]"
		}
		return parsed.Scheme + "://" + parsed.Hostname() + "/[REDACTED]"
	})
	value = strings.Join(strings.Fields(value), " ")
	if len(value) > maxLogExcerptBytes {
		value = value[:maxLogExcerptBytes-3] + "..."
	}
	return value
}

func safeSignalName(value string) bool {
	if value == "" || len(value) > 80 || secretValuePattern.MatchString(value) {
		return false
	}
	lower := strings.ToLower(value)
	return !containsAny(lower, "password", "passwd", "secret", "token", "api_key", "apikey")
}

func normalizeName(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var b strings.Builder
	for _, r := range value {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '.' || r == '-' || r == '_' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func finalizeLogSignals(report *Report, acc *logAccumulator) {
	report.RecentLogs.Repositories = make([]RepositoryLogCoverage, 0, len(acc.repositoryCoverage))
	for _, coverage := range acc.repositoryCoverage {
		report.RecentLogs.Repositories = append(report.RecentLogs.Repositories, *coverage)
	}
	sort.Slice(report.RecentLogs.Repositories, func(i, j int) bool {
		if report.RecentLogs.Repositories[i].RepositoryID != report.RecentLogs.Repositories[j].RepositoryID {
			return report.RecentLogs.Repositories[i].RepositoryID < report.RecentLogs.Repositories[j].RepositoryID
		}
		return report.RecentLogs.Repositories[i].RepositoryName < report.RecentLogs.Repositories[j].RepositoryName
	})
	report.RecentLogs.MissingExecutables = flattenLogSignals(acc.missing)
	report.RecentLogs.ToolUses = flattenLogSignals(acc.tools)
	report.RecentLogs.ExternalSystems = flattenLogSignals(acc.external)
}

func flattenLogSignals(values map[string]*logSignalAccumulator) []LogSignal {
	result := make([]LogSignal, 0, len(values))
	for name, value := range values {
		result = append(result, LogSignal{Name: name, Count: value.count, PlanCount: len(value.plans), RepositoryCount: len(value.repositories), Exemplars: value.exemplars})
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Count != result[j].Count {
			return result[i].Count > result[j].Count
		}
		return result[i].Name < result[j].Name
	})
	return result
}
