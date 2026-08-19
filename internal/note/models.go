package note

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	// Schema is intentionally distinct from the retired tao.note.v1 format.
	Schema  = "tao.repo-note.v1"
	MaxText = 64 * 1024
)

type Status string

const (
	StatusOpen     Status = "open"
	StatusPromoted Status = "promoted"
	StatusArchived Status = "archived"
)

// RepoReference identifies the registered repository that owns a note.
type RepoReference struct {
	ID   string `json:"id"`
	Root string `json:"root,omitempty"`
}

// ArchiveMetadata records an archive transition. Archives linked to a plan are
// terminal; archives without a plan retain the ordinary reversible lifecycle.
type ArchiveMetadata struct {
	ArchivedAt      time.Time            `json:"archived_at"`
	Reason          string               `json:"reason,omitempty"`
	Plan            *PlanLink            `json:"plan,omitempty"`
	PlanningSession *PlanningSessionLink `json:"planning_session,omitempty"`
}

// PlanningSessionLink identifies a durable planning session created from a note.
type PlanningSessionLink struct {
	ID  string `json:"id"`
	URL string `json:"url,omitempty"`
}

// PlanLink identifies a normal Tao plan created from a note.
type PlanLink struct {
	ID   string `json:"id"`
	Dir  string `json:"dir,omitempty"`
	Mode string `json:"mode,omitempty"`
}

// PromotionLinks records the typed destination of a promoted note.
type PromotionLinks struct {
	PlanningSession *PlanningSessionLink `json:"planning_session,omitempty"`
	Plan            *PlanLink            `json:"plan,omitempty"`
}

type Note struct {
	Schema    string           `json:"schema"`
	ID        string           `json:"id"`
	Repo      RepoReference    `json:"repo"`
	Text      string           `json:"text"`
	Tags      []string         `json:"tags,omitempty"`
	CreatedAt time.Time        `json:"created_at"`
	UpdatedAt time.Time        `json:"updated_at"`
	Status    Status           `json:"status"`
	Archive   *ArchiveMetadata `json:"archive,omitempty"`
	Promotion *PromotionLinks  `json:"promotion,omitempty"`
}

var (
	ErrNotFound        = errors.New("note not found")
	ErrAmbiguous       = errors.New("ambiguous note id")
	ErrInvalidID       = errors.New("invalid note id")
	ErrImmutable       = errors.New("promoted note is immutable")
	ErrInvalidState    = errors.New("invalid note lifecycle transition")
	ErrPromotionLocked = errors.New("note promotion lock held")
)

// Warning describes one unreadable record without suppressing valid records.
type Warning struct {
	Path string
	Err  error
}

func (w Warning) Error() string { return fmt.Sprintf("%s: %v", w.Path, w.Err) }

// Filter limits List results. A zero filter includes open notes only; All
// explicitly includes every lifecycle state.
type Filter struct {
	Statuses []Status
	Tags     []string
	All      bool
}

func validateText(text string) error {
	if strings.TrimSpace(text) == "" {
		return errors.New("note text is blank")
	}
	if len([]byte(text)) > MaxText {
		return fmt.Errorf("note text exceeds %d bytes", MaxText)
	}
	return nil
}

func normalizeTags(tags []string) []string {
	seen := make(map[string]struct{}, len(tags))
	out := make([]string, 0, len(tags))
	for _, tag := range tags {
		tag = strings.ToLower(strings.TrimSpace(tag))
		if tag == "" {
			continue
		}
		if _, ok := seen[tag]; ok {
			continue
		}
		seen[tag] = struct{}{}
		out = append(out, tag)
	}
	return out
}
