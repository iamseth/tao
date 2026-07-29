package planning

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/iamseth/tao/internal/agent"
	"github.com/iamseth/tao/internal/plan"
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

// GeneratePlanResult contains the surviving validated allocation.
type GeneratePlanResult struct {
	Allocation PlanAllocation
	Detail     *plan.PlanDetail
	Validation ValidationResult
	Agent      AgentSummary
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
	fail := func(stage GenerationStage, cause error) (*GeneratePlanResult, error) {
		return nil, &GenerationError{
			Stage: stage, Err: cause, CleanupErr: repo.DeleteAllocatedPlan(context.Background(), allocation),
			Allocation: allocation, Validation: failedValidation,
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
	result, err := s.runtime().RunSession(ctx, agent.Session{
		RepoRoot: request.Session.Repo.Root, Prompt: prompt, PermissionMode: mode,
		Timeout: request.Timeout, Progress: s.Log,
	})
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
	summary := result.Output
	if summary == "" {
		summary = result.FinalText
	}
	if strings.TrimSpace(summary) == "" {
		summary = fmt.Sprintf("Created Tao plan %s at %s.", allocation.ID, allocation.Dir)
	}
	return &GeneratePlanResult{
		Allocation: allocation, Detail: detail, Validation: validation,
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
