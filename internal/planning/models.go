package planning

import "time"

const (
	// SessionSchema is the durable JSON schema for repo-scoped planning sessions.
	SessionSchema = "tao.planning.session.v1"
)

type Status string

const (
	StatusDraft     Status = "draft"
	StatusCompleted Status = "completed"
)

type ActionStatus string

const ActionStatusRunning ActionStatus = "running"

type MessageRole string

const RoleUser MessageRole = "user"

// Session is a durable, non-executable planning conversation anchored to one repo.
type Session struct {
	Schema         string              `json:"schema"`
	ID             string              `json:"id"`
	Title          string              `json:"title"`
	Status         Status              `json:"status"`
	Repo           RepoRef             `json:"repo"`
	CreatedAt      time.Time           `json:"created_at"`
	UpdatedAt      time.Time           `json:"updated_at"`
	LastActivityAt time.Time           `json:"last_activity_at"`
	Messages       []TranscriptMessage `json:"messages"`
	Actions        []PlanningAction    `json:"actions,omitempty"`
	GeneratedPlan  *GeneratedPlanLink  `json:"generated_plan,omitempty"`
	Validation     *ValidationResult   `json:"validation,omitempty"`
	Failure        *FailureDetails     `json:"failure,omitempty"`
	Source         *SourceEnvelope     `json:"source,omitempty"`
}

// SourceEnvelope records optional typed provenance without coupling planning to
// the source subsystem. Unknown source types and fields remain readable.
type SourceEnvelope struct {
	Type string              `json:"type"`
	Note *SourceNoteSnapshot `json:"note,omitempty"`
}

// SourceNoteSnapshot is the immutable note context captured when a note is
// promoted into supervised planning.
type SourceNoteSnapshot struct {
	ID         string    `json:"id"`
	Text       string    `json:"text"`
	Tags       []string  `json:"tags,omitempty"`
	RepoID     string    `json:"repo_id"`
	RepoName   string    `json:"repo_name,omitempty"`
	CapturedAt time.Time `json:"captured_at"`
}

type RepoRef struct {
	ID        string `json:"id"`
	Name      string `json:"name,omitempty"`
	Root      string `json:"root,omitempty"`
	Branch    string `json:"branch,omitempty"`
	RemoteURL string `json:"remote_url,omitempty"`
}

type TranscriptMessage struct {
	ID        string            `json:"id"`
	Role      MessageRole       `json:"role"`
	Content   string            `json:"content"`
	Command   string            `json:"command,omitempty"`
	CreatedAt time.Time         `json:"created_at"`
	Metadata  map[string]string `json:"metadata,omitempty"`
}

type PlanningAction struct {
	ID                 string       `json:"id"`
	Command            string       `json:"command"`
	Input              string       `json:"input,omitempty"`
	UserMessageID      string       `json:"user_message_id,omitempty"`
	AssistantMessageID string       `json:"assistant_message_id,omitempty"`
	Status             ActionStatus `json:"status"`
	StartedAt          time.Time    `json:"started_at"`
	CompletedAt        *time.Time   `json:"completed_at,omitempty"`
	Error              string       `json:"error,omitempty"`
}

type GeneratedPlanLink struct {
	PlanID    string    `json:"plan_id"`
	PlanDir   string    `json:"plan_dir,omitempty"`
	RouteID   string    `json:"route_id,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

type ValidationResult struct {
	CheckedAt time.Time           `json:"checked_at"`
	OK        bool                `json:"ok"`
	Findings  []ValidationFinding `json:"findings,omitempty"`
}

type ValidationFinding struct {
	Severity string `json:"severity"`
	Path     string `json:"path,omitempty"`
	Message  string `json:"message"`
}

type FailureDetails struct {
	OccurredAt time.Time `json:"occurred_at"`
	Stage      string    `json:"stage"`
	Message    string    `json:"message"`
	Retriable  bool      `json:"retriable"`
}

type SessionSummary struct {
	ID                 string    `json:"id"`
	RouteID            string    `json:"route_id"`
	Title              string    `json:"title"`
	Status             Status    `json:"status"`
	RepoID             string    `json:"repo_id"`
	RepoName           string    `json:"repo_name,omitempty"`
	RepoRoot           string    `json:"repo_root,omitempty"`
	MessageCount       int       `json:"message_count"`
	GeneratedPlanID    string    `json:"generated_plan_id,omitempty"`
	GeneratedPlanRoute string    `json:"generated_plan_route,omitempty"`
	FailureMessage     string    `json:"failure_message,omitempty"`
	SourceType         string    `json:"source_type,omitempty"`
	SourceID           string    `json:"source_id,omitempty"`
	CreatedAt          time.Time `json:"created_at"`
	UpdatedAt          time.Time `json:"updated_at"`
	LastActivityAt     time.Time `json:"last_activity_at"`
}

type ListFilter struct {
	RepoID string
}

type ListResult struct {
	Sessions []SessionSummary `json:"sessions"`
	Warnings []string         `json:"warnings,omitempty"`
}

func (s *Session) ActiveAction() *PlanningAction {
	if s == nil {
		return nil
	}
	for i := len(s.Actions) - 1; i >= 0; i-- {
		if s.Actions[i].Status == ActionStatusRunning {
			return &s.Actions[i]
		}
	}
	return nil
}

func (s *Session) LastAction() *PlanningAction {
	if s == nil || len(s.Actions) == 0 {
		return nil
	}
	return &s.Actions[len(s.Actions)-1]
}

func Summarize(session *Session) SessionSummary {
	if session == nil {
		return SessionSummary{}
	}
	summary := SessionSummary{
		ID:             session.ID,
		RouteID:        QualifyID(session.Repo.ID, session.ID),
		Title:          session.Title,
		Status:         session.Status,
		RepoID:         session.Repo.ID,
		RepoName:       session.Repo.Name,
		RepoRoot:       session.Repo.Root,
		MessageCount:   len(session.Messages),
		CreatedAt:      session.CreatedAt,
		UpdatedAt:      session.UpdatedAt,
		LastActivityAt: session.LastActivityAt,
	}
	if session.GeneratedPlan != nil {
		summary.GeneratedPlanID = session.GeneratedPlan.PlanID
		summary.GeneratedPlanRoute = session.GeneratedPlan.RouteID
	}
	if session.Failure != nil {
		summary.FailureMessage = session.Failure.Message
	}
	if session.Source != nil {
		summary.SourceType = session.Source.Type
		if session.Source.Note != nil {
			summary.SourceID = session.Source.Note.ID
		}
	}
	return summary
}

func QualifyID(repoID, id string) string {
	if repoID == "" {
		return id
	}
	return repoID + ":" + id
}
