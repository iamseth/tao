package planning

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/iamseth/tao/internal/atomicfile"
	"github.com/iamseth/tao/internal/taodata"
)

type Repository interface {
	CreateSession(context.Context, CreateRequest) (*Session, error)
	ListSessions(context.Context, ListFilter) (ListResult, error)
	GetSession(context.Context, string) (*Session, error)
}

type FileRepository struct {
	DataHome string
	Registry taodata.Registry
	Now      func() time.Time
	Suffix   func() (string, error)
}

func NewFileRepository(dataHome string) *FileRepository {
	if dataHome == "" {
		dataHome = taodata.DataHome()
	}
	return &FileRepository{DataHome: dataHome, Registry: taodata.NewRegistry(dataHome), Now: time.Now, Suffix: randomSuffix}
}

// NewSession constructs and validates a draft planning session record.
func NewSession(id, title, initialPrompt string, repo taodata.Repo, source *SourceEnvelope, createdAt time.Time) (*Session, error) {
	createdAt = createdAt.UTC()
	session := &Session{
		Schema:         SessionSchema,
		ID:             id,
		Title:          title,
		Status:         StatusDraft,
		Repo:           repoRef(repo),
		CreatedAt:      createdAt,
		UpdatedAt:      createdAt,
		LastActivityAt: createdAt,
		Messages:       []TranscriptMessage{},
		Source:         normalizeSource(source, repo, createdAt),
	}
	if initialPrompt != "" {
		message := TranscriptMessage{ID: "msg-001", Role: RoleUser, Content: initialPrompt, CreatedAt: createdAt}
		if session.Source != nil && session.Source.Type == "note" && session.Source.Note != nil {
			message.Metadata = map[string]string{"source_type": "note", "source_id": session.Source.Note.ID}
		}
		session.Messages = append(session.Messages, message)
	}
	if err := validateSessionRecord(session); err != nil {
		return nil, err
	}
	return session, nil
}

func (r *FileRepository) CreateSession(ctx context.Context, req CreateRequest) (*Session, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	repoID := strings.TrimSpace(req.RepoID)
	if repoID == "" {
		return nil, fmt.Errorf("repo is required")
	}
	repo, err := r.registry().ReadRepo(repoID)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("repo %q is not registered", repoID)
		}
		return nil, fmt.Errorf("read repo %q: %w", repoID, err)
	}
	now := r.now().UTC()
	title := strings.TrimSpace(req.Title)
	if title == "" {
		title = titleFromPrompt(req.InitialPrompt)
	}
	if title == "" {
		title = "Draft Planning Session"
	}
	suffix, err := r.suffix()
	if err != nil {
		return nil, fmt.Errorf("generate session id: %w", err)
	}
	id := r.uniqueSessionID(repo.ID, now, title, suffix)
	session, err := NewSession(id, title, strings.TrimSpace(req.InitialPrompt), repo, req.Source, now)
	if err != nil {
		return nil, err
	}
	if err := r.writeSession(session); err != nil {
		return nil, err
	}
	return session, nil
}

func (r *FileRepository) ListSessions(ctx context.Context, filter ListFilter) (ListResult, error) {
	if err := ctx.Err(); err != nil {
		return ListResult{}, err
	}
	repos, err := r.reposForFilter(filter)
	if err != nil {
		return ListResult{}, err
	}
	result := ListResult{}
	for _, repo := range repos {
		dir := r.sessionsRoot(repo.ID)
		entries, err := os.ReadDir(dir)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return ListResult{}, err
		}
		for _, entry := range entries {
			if err := ctx.Err(); err != nil {
				return ListResult{}, err
			}
			if !entry.IsDir() {
				continue
			}
			session, err := r.readSession(filepath.Join(dir, entry.Name(), "session.json"))
			if err != nil {
				result.Warnings = append(result.Warnings, fmt.Sprintf("%s: %v", entry.Name(), err))
				continue
			}
			if session.Repo.ID == "" {
				session.Repo = repoRef(repo)
			}
			result.Sessions = append(result.Sessions, Summarize(session))
		}
	}
	sort.Slice(result.Sessions, func(i, j int) bool {
		left, right := result.Sessions[i], result.Sessions[j]
		if left.UpdatedAt.Equal(right.UpdatedAt) {
			return left.RouteID > right.RouteID
		}
		return left.UpdatedAt.After(right.UpdatedAt)
	})
	return result, nil
}

func (r *FileRepository) GetSession(ctx context.Context, input string) (*Session, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	repoID, sessionID, qualified := strings.Cut(strings.TrimSpace(input), ":")
	if qualified {
		return r.loadRepoSession(repoID, sessionID)
	}
	if err := validateID(input); err != nil {
		return nil, err
	}
	result, err := r.ListSessions(ctx, ListFilter{})
	if err != nil {
		return nil, err
	}
	matches := make([]SessionSummary, 0, 1)
	for _, summary := range result.Sessions {
		if summary.ID == input || strings.HasPrefix(summary.ID, input) {
			matches = append(matches, summary)
		}
	}
	switch len(matches) {
	case 0:
		return nil, classify(ErrSessionNotFound, "planning session %q not found", input)
	case 1:
		return r.loadRepoSession(matches[0].RepoID, matches[0].ID)
	default:
		ids := make([]string, 0, len(matches))
		for _, match := range matches {
			ids = append(ids, match.RouteID)
		}
		sort.Strings(ids)
		return nil, classify(ErrInvalidSession, "planning session %q is ambiguous across repositories; use repo_id:session_id: %s", input, strings.Join(ids, ", "))
	}
}

func (r *FileRepository) loadRepoSession(repoID, sessionID string) (*Session, error) {
	if strings.TrimSpace(repoID) == "" {
		return nil, fmt.Errorf("repo is required")
	}
	if err := validateID(repoID); err != nil {
		return nil, err
	}
	if err := validateID(sessionID); err != nil {
		return nil, err
	}
	path := filepath.Join(r.sessionsRoot(repoID), sessionID, "session.json")
	session, err := r.readSession(path)
	if err != nil {
		return nil, err
	}
	if session.Repo.ID == "" {
		repo, err := r.registry().ReadRepo(repoID)
		if err == nil {
			session.Repo = repoRef(repo)
		}
	}
	return session, nil
}

func (r *FileRepository) reposForFilter(filter ListFilter) ([]taodata.Repo, error) {
	if repoID := strings.TrimSpace(filter.RepoID); repoID != "" {
		repo, err := r.registry().ReadRepo(repoID)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil, fmt.Errorf("repo %q is not registered", repoID)
			}
			return nil, err
		}
		return []taodata.Repo{repo}, nil
	}
	return r.registry().ListRepos()
}

func (r *FileRepository) writeSession(session *Session) error {
	if err := validateSessionRecord(session); err != nil {
		return err
	}
	path := filepath.Join(r.sessionsRoot(session.Repo.ID), session.ID, "session.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil { // #nosec G703 -- repo and session IDs are validated above.
		return fmt.Errorf("create planning session dir: %w", err)
	}
	content, err := json.MarshalIndent(session, "", "  ")
	if err != nil {
		return fmt.Errorf("encode planning session: %w", err)
	}
	if err := atomicfile.Write(path, append(content, '\n'), atomicfile.Options{Perm: 0o600}); err != nil {
		return fmt.Errorf("write planning session: %w", err)
	}
	return nil
}

func (r *FileRepository) readSession(path string) (*Session, error) {
	content, err := os.ReadFile(path) //nolint:gosec // G304: path derived from trusted session store layout
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, classify(ErrSessionNotFound, "planning session not found")
		}
		return nil, fmt.Errorf("read planning session: %w", err)
	}
	var session Session
	if err := json.Unmarshal(content, &session); err != nil {
		return nil, fmt.Errorf("decode planning session: %w", err)
	}
	if session.Schema != SessionSchema {
		return nil, fmt.Errorf("unsupported planning session schema %q", session.Schema)
	}
	if session.Status == "" {
		session.Status = StatusDraft
	}
	if session.Messages == nil {
		session.Messages = []TranscriptMessage{}
	}
	return &session, nil
}

func (r *FileRepository) uniqueSessionID(repoID string, now time.Time, title string, suffix string) string {
	base := now.Format("20060102-150405") + "-" + cleanSlug(title)
	if cleanSlug(title) == "" {
		base = now.Format("20060102-150405") + "-planning"
	}
	if suffix != "" {
		base += "-" + cleanSlug(suffix)
	}
	id := base
	for i := 2; ; i++ {
		_, err := os.Stat(filepath.Join(r.sessionsRoot(repoID), id, "session.json"))
		if errors.Is(err, os.ErrNotExist) {
			return id
		}
		if err != nil {
			return id
		}
		id = fmt.Sprintf("%s-%d", base, i)
	}
}

func (r *FileRepository) sessionsRoot(repoID string) string {
	return filepath.Join(r.dataHome(), "repos", repoID, "planning-sessions")
}

func (r *FileRepository) registry() taodata.Registry {
	if r.Registry.DataHome != "" || r.Registry.Runner != nil || r.Registry.Now != nil {
		return r.Registry
	}
	return taodata.NewRegistry(r.dataHome())
}

func (r *FileRepository) dataHome() string {
	if r.DataHome != "" {
		return r.DataHome
	}
	return taodata.DataHome()
}

func (r *FileRepository) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

func (r *FileRepository) suffix() (string, error) {
	if r.Suffix != nil {
		return r.Suffix()
	}
	return randomSuffix()
}

func repoRef(repo taodata.Repo) RepoRef {
	return RepoRef{ID: repo.ID, Name: repo.Name, Root: repo.Root, Branch: repo.Branch, RemoteURL: repo.RemoteURL}
}

func normalizeSource(source *SourceEnvelope, repo taodata.Repo, now time.Time) *SourceEnvelope {
	if source == nil {
		return nil
	}
	normalized := &SourceEnvelope{Type: strings.ToLower(strings.TrimSpace(source.Type))}
	if source.Note == nil {
		return normalized
	}
	if normalized.Type == "" {
		normalized.Type = "note"
	}
	note := *source.Note
	note.ID = strings.TrimSpace(note.ID)
	note.RepoID = strings.TrimSpace(note.RepoID)
	note.RepoName = strings.TrimSpace(note.RepoName)
	if note.RepoID == "" {
		note.RepoID = repo.ID
	}
	if note.RepoName == "" && note.RepoID == repo.ID {
		note.RepoName = repo.Name
	}
	if note.CapturedAt.IsZero() {
		note.CapturedAt = now
	} else {
		note.CapturedAt = note.CapturedAt.UTC()
	}
	seen := make(map[string]struct{}, len(note.Tags))
	tags := make([]string, 0, len(note.Tags))
	for _, tag := range note.Tags {
		tag = strings.ToLower(strings.TrimSpace(tag))
		if tag == "" {
			continue
		}
		if _, exists := seen[tag]; exists {
			continue
		}
		seen[tag] = struct{}{}
		tags = append(tags, tag)
	}
	note.Tags = tags
	normalized.Note = &note
	return normalized
}

func titleFromPrompt(prompt string) string {
	for line := range strings.SplitSeq(prompt, "\n") {
		line = trimPlanningCommand(strings.TrimSpace(line))
		if line == "" {
			continue
		}
		if len(line) > 80 {
			return line[:79] + "…"
		}
		return line
	}
	return ""
}

func trimPlanningCommand(line string) string {
	for _, command := range []string{"/tao-plan", "/plan"} {
		rest, found := strings.CutPrefix(line, command)
		if !found {
			continue
		}
		trimmed := strings.TrimLeftFunc(rest, unicode.IsSpace)
		if rest == "" || trimmed != rest {
			return trimmed
		}
	}
	return line
}

func cleanSlug(value string) string {
	return taodata.Slug(value, 36)
}

func validateSessionRecord(session *Session) error {
	if session == nil {
		return fmt.Errorf("planning session is nil")
	}
	if session.Schema == "" {
		session.Schema = SessionSchema
	}
	if session.Schema != SessionSchema {
		return fmt.Errorf("unsupported planning session schema %q", session.Schema)
	}
	if err := validateID(session.ID); err != nil {
		return err
	}
	if strings.TrimSpace(session.Repo.ID) == "" {
		return fmt.Errorf("repo is required")
	}
	if err := validateID(session.Repo.ID); err != nil {
		return err
	}
	if session.Status == "" {
		session.Status = StatusDraft
	}
	if session.Messages == nil {
		session.Messages = []TranscriptMessage{}
	}
	return nil
}

func validateID(id string) error {
	if id == "" || strings.ContainsAny(id, `/\`) || filepath.Clean(id) != id || filepath.Ext(id) != "" {
		return classify(ErrInvalidSession, "invalid planning session id %q", id)
	}
	return nil
}

func randomSuffix() (string, error) {
	b := make([]byte, 3)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
