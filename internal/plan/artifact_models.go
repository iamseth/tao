package plan

import "time"

const (
	PlanningSessionExportFile = "planning-session.json"
	PlanningSessionStatsFile  = "planning-session-stats.json"
	PlanningPromptFile        = "planning-prompt.md"
	PlanningBriefFile         = "planning-brief.md"
	ReviewFile                = "review.md"
	PlanMarkdownFile          = "plan.md"
)

// SlicesFile is the executable slice artifact for a plan.
type SlicesFile struct {
	Schema    string    `json:"schema"`
	PlanID    string    `json:"plan_id"`
	Execution Execution `json:"execution"`
	Slices    []Slice   `json:"slices"`
}

// Execution describes how slices are intended to be scheduled.
type Execution struct {
	Mode         string `json:"mode"`
	ParallelSafe bool   `json:"parallel_safe"`
}

// Slice is one independently reviewable unit of agent work.
type Slice struct {
	ID                  string                  `json:"id"`
	Title               string                  `json:"title"`
	Status              string                  `json:"status"`
	BlockerNote         string                  `json:"blocker_note,omitempty"`
	ExecutionRoot       string                  `json:"execution_root,omitempty"`
	ExecutionStart      *SliceExecutionStart    `json:"execution_start,omitempty"`
	Tags                []string                `json:"tags,omitempty"`
	DependsOn           []string                `json:"depends_on"`
	Timing              SliceTiming             `json:"timing"`
	Goal                string                  `json:"goal"`
	Context             string                  `json:"context"`
	Tasks               []string                `json:"tasks"`
	ExpectedFiles       []string                `json:"expected_files"`
	RequiredInputs      []RequiredInput         `json:"required_inputs,omitempty"`
	Verification        Verification            `json:"verification"`
	Approval            *Approval               `json:"approval,omitempty"`
	Notes               string                  `json:"notes,omitempty"`
	VerificationResults []VerificationRun       `json:"verification_results,omitempty"`
	CommitIntent        *SliceCommitIntent      `json:"commit_intent,omitempty"`
	Completion          *SliceCompletionOutcome `json:"completion,omitempty"`
	Extra               map[string]any          `json:"-"`
}

// SliceExecutionStart protects the branch and HEAD prepared for automatic work.
type SliceExecutionStart struct {
	Branch            string `json:"branch"`
	Head              string `json:"head"`
	CommitPolicy      string `json:"commit_policy,omitempty"`
	WorkspaceStrategy string `json:"workspace_strategy,omitempty"`
}

// SliceCommitIntent is the durable boundary written before Tao mutates Git.
// Message is the exact final message reused during recovery; Hash binds new
// intents to both that message and the completion report. Legacy hashes remain
// readable by the completion service.
type SliceCommitIntent struct {
	Hash           string    `json:"hash"`
	Policy         string    `json:"policy"`
	StartingBranch string    `json:"starting_branch,omitempty"`
	StartingHead   string    `json:"starting_head,omitempty"`
	Message        string    `json:"message,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
}

// SliceCompletionOutcome records how work was settled at slice completion.
type SliceCompletionOutcome struct {
	Outcome   string `json:"outcome"`
	CommitSHA string `json:"commit_sha,omitempty"`
}

const (
	SliceCompletionCommitted         = "committed"
	SliceCompletionNoChanges         = "no_changes"
	SliceCompletionManualUncommitted = "manual_uncommitted"
)

// SliceTiming records lifecycle timestamps for one slice.
type SliceTiming struct {
	CreatedAt       time.Time  `json:"created_at"`
	StartedAt       *time.Time `json:"started_at"`
	CompletedAt     *time.Time `json:"completed_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
	LastActivityAt  *time.Time `json:"last_activity_at"`
	DurationSeconds *int64     `json:"duration_seconds"`
}

const (
	RequiredInputFile      = "file"
	RequiredInputDirectory = "directory"
)

// RequiredInput declares a concrete repository input needed by a slice.
type RequiredInput struct {
	Path   string `json:"path"`
	Kind   string `json:"kind"`
	Reason string `json:"reason"`
}

// Verification lists the commands and manual checks required for a slice.
type Verification struct {
	Commands     []string           `json:"commands"`
	Source       string             `json:"source,omitempty"`
	Steps        []VerificationStep `json:"steps,omitempty"`
	ManualChecks []string           `json:"manual_checks"`
}

// VerificationStep preserves structured verification context when available.
type VerificationStep struct {
	Command string `json:"command"`
	CWD     string `json:"cwd,omitempty"`
	Source  string `json:"source,omitempty"`
	Reason  string `json:"reason,omitempty"`
}

// Approval gates a slice until an explicit external decision is recorded.
type Approval struct {
	Required   bool    `json:"required"`
	Reason     string  `json:"reason"`
	Approved   bool    `json:"approved"`
	ApprovedBy *string `json:"approved_by,omitempty"`
	ApprovedAt *string `json:"approved_at,omitempty"`
}

// VerificationRun records the result of a completed verification command.
type VerificationRun struct {
	Command string `json:"command"`
	CWD     string `json:"cwd"`
	Result  string `json:"result"`
	Details string `json:"details"`
}

// PlanDetail is the loaded representation of one plan directory.
type PlanDetail struct {
	Dir             string
	State           State
	Slices          SlicesFile
	Events          []Event
	PlanningSession PlanningSessionArtifacts
	PlanningBrief   PlanningBriefArtifact
	Review          PlanReviewArtifact
	PlanNarrative   PlanNarrativeArtifact
	Warnings        []string

	// Loaded baselines retain artifact snapshots so a later PlanRecord can
	// distinguish caller edits from concurrently settled changes. Hand-built
	// details leave them nil and use persisted artifacts at record construction.
	loadedStateBaseline  *State
	loadedSlicesBaseline *SlicesFile
}

// PlanningSessionArtifacts are optional sidecars captured from the Agent planning session.
type PlanningSessionArtifacts struct {
	ExportPath string
	HasExport  bool
	Stats      *PlanningSessionStats
	PromptPath string
	Prompt     string
}

// PlanningBriefArtifact is the optional concise planning summary for new plans.
type PlanningBriefArtifact struct {
	Path    string
	Content string
}

// PlanReviewArtifact is the optional human-readable persisted review.
type PlanReviewArtifact struct {
	Path    string
	Content string
}

// PlanNarrativeArtifact is the optional fuller human plan narrative.
type PlanNarrativeArtifact struct {
	Path    string
	Content string
}

// PlanningSessionStats is Tao's stable summary schema for planning-session capture.
type PlanningSessionStats struct {
	Schema                  string     `json:"schema,omitempty"`
	PlanID                  string     `json:"plan_id,omitempty"`
	Agent                   string     `json:"agent,omitempty"`
	SessionID               string     `json:"session_id"`
	RepositoryRoot          string     `json:"repository_root,omitempty"`
	PlanningStartedAt       *time.Time `json:"planning_started_at,omitempty"`
	TimeCreated             *time.Time `json:"time_created,omitempty"`
	TimeUpdated             *time.Time `json:"time_updated,omitempty"`
	CaptureStatus           string     `json:"capture_status,omitempty"`
	CaptureSuspect          bool       `json:"capture_suspect,omitempty"`
	CaptureSuspectReason    string     `json:"capture_suspect_reason,omitempty"`
	ProviderID              string     `json:"provider_id,omitempty"`
	ModelID                 string     `json:"model_id,omitempty"`
	InputTokens             int64      `json:"input_tokens,omitempty"`
	OutputTokens            int64      `json:"output_tokens,omitempty"`
	ReasoningTokens         int64      `json:"reasoning_tokens,omitempty"`
	CacheReadTokens         int64      `json:"cache_read_tokens,omitempty"`
	CacheWriteTokens        int64      `json:"cache_write_tokens,omitempty"`
	TotalTokens             int64      `json:"total_tokens,omitempty"`
	Cost                    float64    `json:"cost,omitempty"`
	TotalMessages           int64      `json:"total_messages,omitempty"`
	UserMessages            int64      `json:"user_messages,omitempty"`
	AssistantMessages       int64      `json:"assistant_messages,omitempty"`
	ErroredMessages         int64      `json:"errored_messages,omitempty"`
	ToolCalls               int64      `json:"tool_calls,omitempty"`
	ExportSanitized         bool       `json:"export_sanitized"`
	ExportStatus            string     `json:"export_status,omitempty"`
	ExportError             string     `json:"export_error,omitempty"`
	PromptExtracted         bool       `json:"prompt_extracted"`
	PromptExtractionNote    string     `json:"prompt_extraction_note,omitempty"`
	PromptExtractionSource  string     `json:"prompt_extraction_source,omitempty"`
	PromptExtractionFailure string     `json:"prompt_extraction_failure,omitempty"`
	PromptMessageRows       int64      `json:"prompt_message_rows_examined,omitempty"`
	PromptPartRows          int64      `json:"prompt_part_rows_examined,omitempty"`
	PromptBytes             int64      `json:"prompt_bytes,omitempty"`
	PromptLines             int64      `json:"prompt_lines,omitempty"`
}
