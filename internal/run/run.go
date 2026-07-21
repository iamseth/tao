package run

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/iamseth/tao/internal/agent"
	"github.com/iamseth/tao/internal/commandrunner"
	"github.com/iamseth/tao/internal/plan"
)

type CommandRunner = commandrunner.Runner

type PlanMutationRecord interface {
	SliceStartRecorder
	SliceStartRepairer
	FinalVerificationRecorder
	RunMetadataRecorder
	ReviewRecorder
}

// SliceStartRecorder owns complete durable operations for prepared run starts.
type SliceStartRecorder interface {
	StartSliceWithRunCommitPolicy(sliceID string, executionRoot string, commitPolicy string, startingDirtyPaths []string, now time.Time) error
	StartSliceWithRunBoundary(sliceID string, executionRoot string, commitPolicy string, startingDirtyPaths []string, boundary plan.SliceExecutionStart, now time.Time) error
}

// SliceStartRepairer completes any persisted prefix of an automatic slice
// start after the live Git boundary has been validated.
type SliceStartRepairer interface {
	RepairSliceStartWithRunBoundary(sliceID string, executionRoot string, commitPolicy string, startingDirtyPaths []string, boundary plan.SliceExecutionStart, startedAt time.Time) error
	RepairMissingSliceStartedEvent(sliceID string, startedAt time.Time) error
}

// FinalVerificationRecorder owns final-verification state metadata without
// exposing state.json persistence to orchestration.
type FinalVerificationRecorder interface {
	RecordFinalVerification(verification plan.FinalVerification) error
}

// RunMetadataRecorder groups remaining run metadata operations that do not
// belong to slice start, final verification, or review.
type RunMetadataRecorder interface {
	ContinueBlocked(now time.Time) error
	PersistState() error
	RecordStartingBranch(branch string) error
	RecordPullRequest(pr plan.PullRequest, branch, headSHA string) error
}

// ReviewRecorder owns durable review outcomes used by run finalization.
type ReviewRecorder interface {
	RecordReviewError(review plan.PlanReview, agent string) error
	RecordReviewCompleted(review plan.PlanReview, agent string) error
}

type PlanRecordFactory func(detail *plan.PlanDetail) (PlanMutationRecord, error)

// Options is the construction surface callers fill to start a run. It composes
// the user-visible run configuration (ExecutionConfig) and the run's
// collaborators (RunDependencies) instead of mirroring their fields, so a new
// run setting or dependency is added in exactly one place.
type Options struct {
	ExecutionConfig
	RunDependencies
}

// ExecutionConfig carries the run configuration the executor reads. It embeds
// the resolved runtime options and adds SkipPermissions, the only process-only
// knob that stays outside runtimeconfig.
type ExecutionConfig struct {
	ResolvedRunOptions
	SkipPermissions bool
}

type runExecution struct {
	Config             ExecutionConfig
	Dependencies       RunDependencies
	ExecutionRoot      string
	StartingBranch     string
	StartingDirtyPaths []string
	ExecutionBoundary  *ExecutionBoundaryAction
}

type SliceRun struct {
	PlanDir       string
	SliceID       string
	LogPath       string
	RunPacket     string
	RepoRoot      string
	Resuming      bool
	ResumeAttempt int
}

type SliceExecutor interface {
	RunSlice(ctx context.Context, run SliceRun) error
}

type PullRequestRun struct {
	PlanDir  string
	PlanID   string
	LogPath  string
	Detail   *plan.PlanDetail
	RepoRoot string
	Branch   string
	HeadSHA  string
}

type PullRequestCreator interface {
	CreatePullRequest(ctx context.Context, run PullRequestRun) (plan.PullRequest, error)
}

type PullRequestBodyRun struct {
	PlanDir    string
	PlanID     string
	Detail     *plan.PlanDetail
	RepoRoot   string
	Branch     string
	HeadSHA    string
	BaseBranch string
	Title      string
	DraftBody  string
}

type PullRequestBodyGenerator interface {
	GeneratePullRequestBody(ctx context.Context, run PullRequestBodyRun) (string, error)
}

type AgentSessionRequest struct {
	PlanDir             string
	RepoRoot            string
	LogAction           string
	Prompt              string
	CaptureOutput       bool
	Metrics             *AgentSessionMetricsRequest
	NoProgressToolLimit int
}

type AgentSessionMetricsRequest struct {
	SliceID string
}

type AgentSessionResult struct {
	Output    string
	FinalText string
}

type AgentSessionExecutor interface {
	RunAgentSession(ctx context.Context, request AgentSessionRequest) (AgentSessionResult, error)
}

type ExecutionRootResolver interface {
	ResolveExecutionRoot(ctx context.Context, detail *plan.PlanDetail) (string, error)
}

type WorkspaceResolverInput struct {
	Config            ExecutionConfig
	ExecutionRoot     string
	CommandRunner     CommandRunner
	PlanRecordFactory PlanRecordFactory
	WorkspacePreparer WorkspacePreparer
	Now               func() time.Time
}

func (i WorkspaceResolverInput) commandRunner() CommandRunner { return i.CommandRunner }
func (i WorkspaceResolverInput) clock() func() time.Time      { return i.Now }

type WorkspacePreparer func(ctx context.Context, detail *plan.PlanDetail, input WorkspaceResolverInput) (string, error)

type Repository interface {
	plan.SliceRunRepository
}

type Service struct {
	repo         Repository
	out          io.Writer
	config       ExecutionConfig
	dependencies RunDependencies
}

func NewService(repo Repository, out io.Writer, options Options) Service {
	return Service{repo: repo, out: out, config: options.executionConfig(), dependencies: newRunDependencies(options)}
}

// WithPlanRunLock keeps exclusive ownership of request's plan while operation
// coordinates one or more Execute calls with mutations between them. Execute
// recognizes the returned context as already owning this plan's lock.
func (s Service) WithPlanRunLock(ctx context.Context, request Request, operation func(context.Context) error) error {
	if operation == nil {
		return fmt.Errorf("plan run lock operation is nil")
	}
	detail, err := s.repo.ResolvePlan(ctx, request.Input)
	if err != nil {
		return err
	}
	if detail == nil {
		return fmt.Errorf("plan %q not found", request.Input)
	}
	return withPlanRunLock(ctx, detail, now(s.dependencies).UTC(), operation)
}

func CheckRequestCanStart(detail *plan.PlanDetail, request Request) error {
	// An unsettled automatic completion is intentionally non-runnable to normal
	// lifecycle consumers, but Execute must inspect it to produce the guarded
	// post-intent recovery path without starting an agent.
	if detail != nil && detail.State.Plan.CurrentSlice != nil {
		if slice := interruptedSlice(detail, *detail.State.Plan.CurrentSlice); slice != nil && slice.Status == plan.StatusInProgress && (slice.CommitIntent != nil || slice.Completion != nil) {
			return nil
		}
	}
	capabilities := plan.AnalyzeRunCapabilities(detail)
	if capabilities.CanRun || (request.Continue && capabilities.CanContinue) {
		return nil
	}
	if request.Continue && capabilities.ContinueDisabledReason != "" {
		return cannotStartf("%s", capabilities.ContinueDisabledReason)
	}
	return runDisabledError(capabilities)
}

func (s Service) Execute(ctx context.Context, request Request) error {
	config, err := prepareRequestConfig(s.config, request)
	if err != nil {
		return err
	}
	detail, err := s.repo.ResolvePlan(ctx, request.Input)
	if err != nil {
		return err
	}
	return trackRunStatus(s.dependencies.StatusReporter, runStatusLabel(detail.State.Plan.ID), func() error {
		return withPlanRunLock(ctx, detail, now(s.dependencies).UTC(), func(ownedCtx context.Context) error {
			if err := CheckRequestCanStart(detail, request); err != nil {
				return err
			}
			execution, err := s.prepareRunExecution(ownedCtx, detail, config)
			if err != nil {
				return err
			}
			return executeDetailWithExecution(ownedCtx, detail, func(ctx context.Context, detail *plan.PlanDetail) (*plan.PlanDetail, error) {
				return s.repo.ResolvePlan(ctx, detail.Dir)
			}, s.out, execution)
		})
	})
}

func runDisabledError(capabilities plan.RunCapabilities) error {
	if capabilities.DisabledReason != "" {
		return cannotStartf("%s", capabilities.DisabledReason)
	}
	return cannotStartf("plan cannot run")
}

// agentLabel resolves the short agent name recorded in telemetry and run audits
// via the registry, so the per-kind name lives only in the agent Descriptor. An
// empty kind resolves to Pi, matching the run defaults.
func agentLabel(kind AgentKind) string {
	descriptor, _ := agent.Lookup(kind)
	return descriptor.Label
}

func auditAgent(kind AgentKind) string {
	return agentLabel(kind)
}

func nextSliceLabel(id string) string {
	if id == "" {
		return "-"
	}
	return id
}

func writef(w io.Writer, format string, args ...any) error {
	_, err := fmt.Fprintf(w, format, args...)
	return err
}
