package plannerroute

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/iamseth/tao/internal/randtoken"
	"github.com/iamseth/tao/internal/runtimeconfig"
)

const Schema = "tao.planner.route.v1"
const FeatureSchema = "tao.planner.route.context.v1"
const Inherited = "inherited"

type Mode string

const (
	ModeOff        Mode = "off"
	ModeShadow     Mode = "shadow"
	ModeRandomized Mode = "randomized"
)

func ParseMode(value string) (Mode, error) {
	if value == "" {
		return ModeOff, nil
	}
	switch Mode(value) {
	case ModeOff, ModeShadow, ModeRandomized:
		return Mode(value), nil
	default:
		return "", fmt.Errorf("unsupported planner routing mode %q (want off, shadow, or randomized)", value)
	}
}

// Arm is an exact treatment tuple. Provider, Model, and ReasoningEffort are
// recorded as Inherited until those settings can be controlled.
type Arm struct {
	Runtime         runtimeconfig.AgentKind `json:"runtime"`
	Provider        string                  `json:"provider"`
	Model           string                  `json:"model"`
	ReasoningEffort string                  `json:"reasoning_effort"`
	PromptVersion   string                  `json:"prompt_version"`
	PermissionMode  string                  `json:"permission_mode"`
}

func (a Arm) Key() string {
	return strings.Join([]string{
		string(a.Runtime), a.Provider, a.Model, a.ReasoningEffort, a.PromptVersion, a.PermissionMode,
	}, "|")
}

type WeightedArm struct {
	Arm         Arm     `json:"arm"`
	Probability float64 `json:"probability"`
}

type UnitKey struct {
	RepoID string `json:"repo_id"`
	Kind   string `json:"kind"`
	ID     string `json:"id"`
}

type Assignment struct {
	PolicyVersion  string        `json:"policy_version"`
	Mode           Mode          `json:"mode"`
	UnitKey        UnitKey       `json:"unit_key"`
	Eligible       []WeightedArm `json:"eligible"`
	Selected       Arm           `json:"selected"`
	Draw           float64       `json:"draw"`
	ManualOverride bool          `json:"manual_override"`
	OverrideArm    *Arm          `json:"override_arm,omitempty"`
}

// Context contains only pre-treatment features, never planner output.
type Context struct {
	FeatureSchema   string                  `json:"feature_schema"`
	RepoID          string                  `json:"repo_id"`
	RepoName        string                  `json:"repo_name"`
	UnitKind        string                  `json:"unit_kind"`
	UnitID          string                  `json:"unit_id"`
	NoteTags        []string                `json:"note_tags"`
	NoteTextBucket  string                  `json:"note_text_bucket"`
	BaselineRuntime runtimeconfig.AgentKind `json:"baseline_runtime"`
	PromptVersion   string                  `json:"prompt_version"`
	PermissionMode  string                  `json:"permission_mode"`
	BuildVersion    string                  `json:"build_version"`
}

func NoteTextBucket(length int) string {
	switch {
	case length < 1000:
		return "short"
	case length < 4000:
		return "medium"
	default:
		return "long"
	}
}

type EntryKind string

const (
	EntryAssigned EntryKind = "assigned"
	EntryTreated  EntryKind = "treated"
	EntryAttempt  EntryKind = "attempt"
	EntryLinked   EntryKind = "linked"
)

type Treatment struct {
	RuntimeLabel        string `json:"runtime_label"`
	ProviderID          string `json:"provider_id"`
	ModelID             string `json:"model_id"`
	MetricsAvailability string `json:"metrics_availability"`
	// Failover stays empty while planner failover is unsupported.
	Failover string `json:"failover"`
}

type Attempt struct {
	Stage string `json:"stage"`
	// Outcome is plan_created, failed, or interrupted.
	Outcome string `json:"outcome"`
}

type Link struct {
	PlanID  string `json:"plan_id"`
	PlanDir string `json:"plan_dir"`
}

type Entry struct {
	Kind      EntryKind  `json:"kind"`
	At        time.Time  `json:"at"`
	Treatment *Treatment `json:"treatment,omitempty"`
	Attempt   *Attempt   `json:"attempt,omitempty"`
	Link      *Link      `json:"link,omitempty"`
}

type Record struct {
	Schema     string     `json:"schema"`
	ID         string     `json:"id"`
	RepoID     string     `json:"repo_id"`
	CreatedAt  time.Time  `json:"created_at"`
	Context    Context    `json:"context"`
	Assignment Assignment `json:"assignment"`
	Entries    []Entry    `json:"entries"`
}

var routeIDPattern = regexp.MustCompile(`^[0-9]{8}-[0-9]{6}-[0-9a-f]{32}$`)

func NewRouteID(now time.Time) (string, error) {
	token, err := randtoken.New()
	if err != nil {
		return "", fmt.Errorf("generate planner route ID: %w", err)
	}
	return now.UTC().Format("20060102-150405") + "-" + token, nil
}

// ValidRouteID checks the generated shape so an ID is safe as a path component.
func ValidRouteID(id string) bool {
	return routeIDPattern.MatchString(id)
}

func (r Record) LinkedPlanID() string {
	for _, entry := range r.Entries {
		if entry.Kind == EntryLinked {
			if entry.Link == nil {
				return ""
			}
			return entry.Link.PlanID
		}
	}
	return ""
}

func (r Record) Validate() error {
	if r.Schema != Schema {
		return fmt.Errorf("unsupported planner route schema %q", r.Schema)
	}
	if !ValidRouteID(r.ID) {
		return fmt.Errorf("invalid planner route ID %q", r.ID)
	}
	if strings.TrimSpace(r.RepoID) == "" {
		return fmt.Errorf("planner route repo_id is required")
	}
	if strings.TrimSpace(r.Assignment.PolicyVersion) == "" {
		return fmt.Errorf("planner route assignment policy_version is required")
	}
	linked := false
	for _, entry := range r.Entries {
		if entry.Kind == EntryLinked {
			if linked {
				return fmt.Errorf("planner route must have at most one linked entry")
			}
			linked = true
		}
	}
	return nil
}
