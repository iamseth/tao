package planning

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/iamseth/tao/internal/agent"
	"github.com/iamseth/tao/internal/agentsession"
	"github.com/iamseth/tao/internal/agenttelemetry"
	"github.com/iamseth/tao/internal/atomicfile"
	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/internal/runtimeconfig"
)

// GenerationStage identifies the deterministic step at which plan generation failed.
type GenerationStage string

const (
	GenerationStageAllocation       GenerationStage = "slice_allocation"
	GenerationStagePrompt           GenerationStage = "slice_prompt"
	GenerationStageRuntime          GenerationStage = "slice_agent"
	GenerationStageRequiredArtifact GenerationStage = "slice_required_artifacts"
	GenerationStagePlanID           GenerationStage = "slice_plan_id"
	GenerationStageVerification     GenerationStage = "slice_verification"
	GenerationStageOpenQuestions    GenerationStage = "slice_open_questions"
	GenerationStageValidation       GenerationStage = "slice_validation"
)

// GenerationError retains the original generation failure and, separately, a
// best-effort cleanup failure. Unwrap always returns the original failure.
type GenerationError struct {
	LogPath    string
	Stage      GenerationStage
	Err        error
	CleanupErr error
	Allocation PlanAllocation
	Validation *ValidationResult
	Treatment  *Treatment
}

func (e *GenerationError) Error() string {
	if e == nil {
		return ""
	}
	message := fmt.Sprintf("%s: %v", e.Stage, e.Err)
	if e.CleanupErr != nil {
		message += fmt.Sprintf("; cleanup failed: %v", e.CleanupErr)
	}
	if e.LogPath != "" {
		message += "; planning log: " + e.LogPath
	}
	return message
}

func (e *GenerationError) Unwrap() error { return e.Err }

// GeneratePlanRequest describes one synchronous, provider-neutral planning run.
type GeneratePlanRequest struct {
	Session             *Session
	AgentKind           runtimeconfig.AgentKind // Empty inherits the service kind.
	Slug                string
	Extra               string
	PermissionMode      agent.PermissionMode
	Timeout             time.Duration
	RejectOpenQuestions bool
}

// AgentSummary is the provider-neutral textual result returned by the agent.
type AgentSummary struct {
	Output    string
	FinalText string
}

// Treatment reports the selected runtime and observed session measurements,
// including sessions that failed without producing a plan.
type Treatment struct {
	RuntimeLabel, ProviderID, ModelID string
	MetricsAvailability               string
}

// GeneratePlanResult contains the surviving validated allocation.
type GeneratePlanResult struct {
	Allocation PlanAllocation
	Detail     *plan.PlanDetail
	Validation ValidationResult
	Agent      AgentSummary
	Treatment  Treatment
	Summary    string
}

// GeneratePlan allocates, prompts, runs, normalizes, and validates exactly one
// plan synchronously. Any failure after allocation removes only that allocation.
func (s *Service) GeneratePlan(ctx context.Context, request GeneratePlanRequest) (*GeneratePlanResult, error) {
	repo, err := s.sliceRepository()
	if err != nil {
		return nil, &GenerationError{Stage: GenerationStageAllocation, Err: err}
	}
	if request.Session == nil {
		return nil, &GenerationError{Stage: GenerationStageAllocation, Err: fmt.Errorf("planning session is required")}
	}
	slug := strings.TrimSpace(request.Slug)
	if slug == "" {
		slug = sliceSlug(request.Session, request.Extra)
	}
	allocation, err := repo.AllocatePlanForSession(ctx, request.Session, slug)
	if err != nil {
		return nil, &GenerationError{Stage: GenerationStageAllocation, Err: err}
	}
	var sessionLog planningLogBuffer
	var agentRan bool
	var failedValidation *ValidationResult
	var treatment *Treatment
	fail := func(stage GenerationStage, cause error) (*GeneratePlanResult, error) {
		var logPath string
		var logErr error
		if agentRan && s.RefusalLogDir != "" {
			if data := sessionLog.Bytes(); len(data) > 0 {
				logPath, logErr = writeRefusalLog(s.RefusalLogDir, allocation.ID, data)
			}
		}
		cleanupErr := repo.DeleteAllocatedPlan(context.Background(), allocation)
		return nil, &GenerationError{
			Stage: stage, Err: cause, CleanupErr: errors.Join(logErr, cleanupErr), LogPath: logPath,
			Allocation: allocation, Validation: failedValidation, Treatment: treatment,
		}
	}
	entries, err := os.ReadDir(allocation.Dir)
	if err != nil {
		return fail(GenerationStageAllocation, err)
	}
	baseline := make([]string, len(entries))
	for i, entry := range entries {
		baseline[i] = entry.Name()
	}
	prompt, err := renderNoteSlicePrompt(request.Session, request.Extra, allocation, request.RejectOpenQuestions)
	if err != nil {
		return fail(GenerationStagePrompt, err)
	}
	mode := request.PermissionMode
	if mode == "" {
		mode = agent.PermissionModeAuto
	}
	kind := request.AgentKind
	if kind == "" {
		kind = s.AgentKind
	}
	descriptor, _ := agent.Lookup(kind)
	agentRan = true
	var result agent.SessionResult
	for attempt := 0; ; attempt++ {
		result, err = s.runtimeFor(kind).RunSession(ctx, agent.Session{
			RepoRoot: request.Session.Repo.Root, Prompt: prompt, PermissionMode: mode,
			Timeout: request.Timeout, Model: s.Model, Effort: s.Effort, Progress: s.Log, Log: &sessionLog, CollectMetrics: true,
		})
		if !agent.IsRetryableTransportFailure(err) || attempt >= planningRetryLimit || ctx.Err() != nil ||
			errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			break
		}
		untouched, checkErr := allocationUntouched(allocation.Dir, baseline)
		if checkErr != nil || !untouched || ctx.Err() != nil {
			break
		}
		record := fmt.Sprintf("tao planning: retrying the planning session once after a retryable agent failure: %v\n", err)
		_, _ = sessionLog.Write([]byte(record))
		if s.Log != nil {
			_, _ = s.Log.Write([]byte(record))
		}
	}
	treatment = &Treatment{
		RuntimeLabel: descriptor.Label, ProviderID: "unknown", ModelID: "unknown",
		MetricsAvailability: string(result.MetricsAvailability()),
	}
	if result.Metrics != nil {
		if result.Metrics.ProviderID != "" {
			treatment.ProviderID = result.Metrics.ProviderID
		}
		if result.Metrics.ModelID != "" {
			treatment.ModelID = result.Metrics.ModelID
		}
	}
	if err != nil {
		return fail(GenerationStageRuntime, err)
	}
	detail, validation, err := repo.ValidateAllocatedPlan(ctx, allocation)
	if err != nil {
		return fail(GenerationStageValidation, err)
	}
	if !validation.OK {
		failedValidation = &validation
		return fail(validationFailureStage(validation), validationError(validation))
	}
	if request.RejectOpenQuestions && detail != nil && len(nonEmptyStrings(detail.State.OpenQuestions)) > 0 {
		return fail(GenerationStageOpenQuestions, fmt.Errorf("generated plan has unresolved open questions: %s", strings.Join(nonEmptyStrings(detail.State.OpenQuestions), "; ")))
	}
	// Only a surviving allocation is a durable telemetry destination. This
	// append never creates a plan directory and cannot change generation success.
	metrics := agenttelemetry.Project(agentsession.Result{
		AgentLabel: descriptor.Label, Metrics: result.Metrics, MetricsAvailability: result.MetricsAvailability(),
	}, plan.AgentRolePlanning, s.Effort, nil)
	event := agenttelemetry.Event(allocation.ID, "", time.Now(), metrics)
	appender := s.EventAppender
	if appender == nil {
		appender = plan.NewFileRepository("")
	}
	if err := appender.AppendEvent(allocation.Dir, event); err == nil && detail != nil {
		detail.Events = append(detail.Events, event)
	}
	summary := result.Output
	if summary == "" {
		summary = result.FinalText
	}
	if strings.TrimSpace(summary) == "" {
		summary = fmt.Sprintf("Created Tao plan %s at %s.", allocation.ID, allocation.Dir)
	}
	return &GeneratePlanResult{
		Allocation: allocation, Detail: detail, Validation: validation, Treatment: *treatment,
		Agent: AgentSummary{Output: result.Output, FinalText: result.FinalText}, Summary: summary,
	}, nil
}

const planningRetryLimit = 1

// allocationUntouched compares the sorted entry names captured at allocation.
// An unreadable allocation cannot authorize a fresh planning attempt.
func allocationUntouched(dir string, baseline []string) (bool, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false, err
	}
	names := make([]string, len(entries))
	for i, entry := range entries {
		names[i] = entry.Name()
	}
	return slices.Equal(names, baseline), nil
}

const planningLogLimit = 4 << 20
const planningLogTruncated = "\n[tao: planning log truncated]\n"

// planningLogBuffer retains a bounded head, accepting discarded bytes so logging
// never interrupts the agent session.
type planningLogBuffer struct {
	mu        sync.Mutex
	data      []byte
	truncated bool
}

// Bytes returns a snapshot independent of subsequent writes.
func (b *planningLogBuffer) Bytes() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	return slices.Clone(b.data)
}

func (b *planningLogBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := len(p)
	if b.truncated {
		return n, nil
	}
	if len(b.data)+n <= planningLogLimit {
		b.data = append(b.data, p...)
		return n, nil
	}
	headLimit := planningLogLimit - len(planningLogTruncated)
	if len(b.data) > headLimit {
		b.data = b.data[:headLimit]
	} else {
		b.data = append(b.data, p[:headLimit-len(b.data)]...)
	}
	b.data = append(b.data, planningLogTruncated...)
	b.truncated = true
	return n, nil
}

func writeRefusalLog(dir, allocationID string, log []byte) (string, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	path := filepath.Join(dir, allocationID+".log")
	if err := atomicfile.Write(path, log, atomicfile.Options{Perm: 0o600}); err != nil {
		return "", err
	}
	return path, nil
}

func validationFailureStage(validation ValidationResult) GenerationStage {
	for _, finding := range validation.Findings {
		if finding.Severity != "error" {
			continue
		}
		switch {
		case strings.Contains(finding.Message, "does not match allocated plan id"):
			return GenerationStagePlanID
		case strings.Contains(finding.Message, "command:") || strings.Contains(finding.Message, "working directory"):
			return GenerationStageVerification
		case finding.Path != "":
			return GenerationStageRequiredArtifact
		}
	}
	return GenerationStageValidation
}

func nonEmptyStrings(values []string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			result = append(result, value)
		}
	}
	return result
}
