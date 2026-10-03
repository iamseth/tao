package planning

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/iamseth/tao/internal/agent"
	"github.com/iamseth/tao/internal/agentsession"
	"github.com/iamseth/tao/internal/agenttelemetry"
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
	if e.CleanupErr != nil {
		return fmt.Sprintf("%s: %v; cleanup failed: %v", e.Stage, e.Err, e.CleanupErr)
	}
	return fmt.Sprintf("%s: %v", e.Stage, e.Err)
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
	var failedValidation *ValidationResult
	var treatment *Treatment
	fail := func(stage GenerationStage, cause error) (*GeneratePlanResult, error) {
		return nil, &GenerationError{
			Stage: stage, Err: cause, CleanupErr: repo.DeleteAllocatedPlan(context.Background(), allocation),
			Allocation: allocation, Validation: failedValidation, Treatment: treatment,
		}
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
	result, err := s.runtimeFor(kind).RunSession(ctx, agent.Session{
		RepoRoot: request.Session.Repo.Root, Prompt: prompt, PermissionMode: mode,
		Timeout: request.Timeout, Model: s.Model, Effort: s.Effort, Progress: s.Log, CollectMetrics: true,
	})
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
