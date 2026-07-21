package planning

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/prompts"
)

type PlanAllocation struct {
	ID  string
	Dir string
}

type SliceRepository interface {
	AllocatePlanForSession(context.Context, *Session, string) (PlanAllocation, error)
	ValidateAllocatedPlan(context.Context, PlanAllocation) (*plan.PlanDetail, ValidationResult, error)
	DeleteAllocatedPlan(context.Context, PlanAllocation) error
}

func (s *Service) sliceRepository() (SliceRepository, error) {
	if s == nil || s.Repo == nil {
		return nil, ErrServiceUnavailable
	}
	return s.Repo, nil
}

func renderNoteSlicePrompt(session *Session, extra string, allocation PlanAllocation, unsupervised bool) (string, error) {
	return prompts.Render(prompts.PromptNoteSlice, prompts.Data{
		PlanDir:            allocation.Dir,
		Arguments:          strings.TrimSpace(extra),
		SessionID:          session.ID,
		Title:              valueOrPlaceholder(session.Title),
		RepoID:             session.Repo.ID,
		RepoName:           valueOrPlaceholder(session.Repo.Name),
		RepoRoot:           valueOrPlaceholder(session.Repo.Root),
		RepoBranch:         valueOrPlaceholder(session.Repo.Branch),
		Transcript:         renderTranscript(session.Messages),
		UnsupervisedPolicy: unsupervised,
	})
}

func renderTranscript(messages []TranscriptMessage) string {
	if len(messages) == 0 {
		return "(No transcript.)"
	}
	var b strings.Builder
	for _, message := range messages {
		fmt.Fprintf(&b, "### %s %s", message.Role, message.ID)
		if message.Command != "" {
			fmt.Fprintf(&b, " command=/%s", message.Command)
		}
		if !message.CreatedAt.IsZero() {
			fmt.Fprintf(&b, " at %s", message.CreatedAt.UTC().Format(time.RFC3339))
		}
		fmt.Fprintf(&b, "\n\n%s\n\n", message.Content)
	}
	return strings.TrimSpace(b.String())
}

func sliceSlug(session *Session, extra string) string {
	for _, candidate := range []string{extra, session.Title, firstUserMessage(session)} {
		if slug := cleanSlug(candidate); slug != "" {
			return slug
		}
	}
	return "note-plan"
}

func firstUserMessage(session *Session) string {
	if session == nil {
		return ""
	}
	for _, message := range session.Messages {
		if message.Role == RoleUser {
			return message.Content
		}
	}
	return ""
}

func (r *FileRepository) AllocatePlanForSession(ctx context.Context, session *Session, slug string) (PlanAllocation, error) {
	if err := ctx.Err(); err != nil {
		return PlanAllocation{}, err
	}
	if session == nil {
		return PlanAllocation{}, fmt.Errorf("planning session is required")
	}
	if strings.TrimSpace(session.Repo.ID) == "" {
		return PlanAllocation{}, fmt.Errorf("repo is required")
	}
	registry := r.registry()
	registry.Now = r.now
	repo, err := registry.ReadRepo(session.Repo.ID)
	if err != nil {
		if os.IsNotExist(err) {
			return PlanAllocation{}, fmt.Errorf("repo %q is not registered", session.Repo.ID)
		}
		return PlanAllocation{}, fmt.Errorf("read repo %q: %w", session.Repo.ID, err)
	}
	allocation, err := registry.AllocatePlan(repo, slug)
	if err != nil {
		return PlanAllocation{}, err
	}
	return PlanAllocation{ID: allocation.ID, Dir: allocation.Dir}, nil
}

func (r *FileRepository) ValidateAllocatedPlan(ctx context.Context, allocation PlanAllocation) (*plan.PlanDetail, ValidationResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, ValidationResult{}, err
	}
	result := ValidationResult{CheckedAt: r.now().UTC()}
	if strings.TrimSpace(allocation.Dir) == "" {
		result.Findings = append(result.Findings, ValidationFinding{Severity: "error", Message: "allocated plan directory is empty"})
		return nil, finishValidation(result), nil
	}
	detail, err := plan.NewFileRepository(filepath.Dir(allocation.Dir)).ResolvePlan(ctx, allocation.Dir)
	if err != nil {
		result.Findings = append(result.Findings, ValidationFinding{Severity: "error", Path: allocation.Dir, Message: err.Error()})
		return nil, finishValidation(result), nil //nolint:nilerr // resolve failure is reported as a validation finding, not a hard error
	}
	for _, warning := range detail.Warnings {
		result.Findings = append(result.Findings, ValidationFinding{Severity: "warning", Message: warning})
	}
	if detail.State.Plan.ID != allocation.ID {
		result.Findings = append(result.Findings, ValidationFinding{Severity: "error", Path: filepath.Join(allocation.Dir, "state.json"), Message: fmt.Sprintf("state.json plan.id %q does not match allocated plan id %q", detail.State.Plan.ID, allocation.ID)})
	}
	verification := plan.ValidatePlanVerification(detail)
	for _, finding := range verification.Findings {
		result.Findings = append(result.Findings, ValidationFinding{Severity: string(finding.Severity), Path: finding.Path, Message: verificationFindingMessage(finding)})
	}
	return detail, finishValidation(result), nil
}

func (r *FileRepository) DeleteAllocatedPlan(ctx context.Context, allocation PlanAllocation) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if allocation.Dir == "" {
		return nil
	}
	if filepath.Base(filepath.Clean(allocation.Dir)) != allocation.ID {
		return fmt.Errorf("refusing to delete allocation %q at %q", allocation.ID, allocation.Dir)
	}
	return os.RemoveAll(allocation.Dir)
}

func finishValidation(result ValidationResult) ValidationResult {
	result.OK = true
	for _, finding := range result.Findings {
		if finding.Severity == "error" {
			result.OK = false
			break
		}
	}
	return result
}

func validationError(result ValidationResult) error {
	for _, finding := range result.Findings {
		if finding.Severity == "error" {
			return fmt.Errorf("generated plan validation failed: %s", finding.Message)
		}
	}
	return fmt.Errorf("generated plan validation failed")
}

func verificationFindingMessage(finding plan.VerificationFinding) string {
	message := finding.Message
	if finding.SliceID != "" {
		message = fmt.Sprintf("%s: %s", finding.SliceID, message)
	}
	if finding.Command != "" {
		message = fmt.Sprintf("%s (command: %s)", message, finding.Command)
	}
	if finding.Suggestion != "" {
		message = fmt.Sprintf("%s (suggestion: %s)", message, finding.Suggestion)
	}
	return message
}
