package note

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/iamseth/tao/internal/atomicfile"
)

var safeID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]*$`)

type Repository struct {
	Dir      string
	Repo     RepoReference
	Now      func() time.Time
	IDSuffix func() string
	Link     func(string, string) error
	Rename   func(string, string) error
}

func NewRepository(dir string, repo RepoReference) *Repository {
	return &Repository{Dir: dir, Repo: repo}
}

func (r *Repository) Create(ctx context.Context, text string, tags []string) (Note, error) {
	if err := ctx.Err(); err != nil {
		return Note{}, err
	}
	if strings.TrimSpace(r.Repo.ID) == "" {
		return Note{}, errors.New("registered repository id is required")
	}
	if err := validateText(text); err != nil {
		return Note{}, err
	}
	now := r.now().UTC()
	for attempt := range 100 {
		id := now.Format("20060102-150405") + "-" + r.suffix()
		if attempt > 0 {
			id = fmt.Sprintf("%s-%d", id, attempt+1)
		}
		if !validID(id) {
			return Note{}, fmt.Errorf("generated %w %q", ErrInvalidID, id)
		}
		n := Note{Schema: Schema, ID: id, Repo: r.Repo, Text: text, Tags: normalizeTags(tags), CreatedAt: now, UpdatedAt: now, Status: StatusOpen}
		err := r.write(ctx, n, true)
		if errors.Is(err, os.ErrExist) {
			continue
		}
		return n, err
	}
	return Note{}, errors.New("allocate unique note id")
}

func (r *Repository) Get(ctx context.Context, id string) (Note, error) {
	id, err := validateLookupID(id)
	if err != nil {
		return Note{}, err
	}
	if err := ctx.Err(); err != nil {
		return Note{}, err
	}
	if content, readErr := os.ReadFile(r.path(id)); readErr == nil { // #nosec G304 -- id is restricted to a safe basename.
		return r.decode(content, r.path(id))
	} else if !os.IsNotExist(readErr) {
		return Note{}, readErr
	}

	entries, err := os.ReadDir(r.Dir)
	if os.IsNotExist(err) {
		return Note{}, fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	if err != nil {
		return Note{}, err
	}
	matches := make([]string, 0, 1)
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return Note{}, err
		}
		name := entry.Name()
		if !entry.IsDir() && strings.HasSuffix(name, ".json") && strings.HasPrefix(strings.TrimSuffix(name, ".json"), id) {
			matches = append(matches, name)
		}
	}
	if len(matches) == 0 {
		return Note{}, fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	if len(matches) > 1 {
		return Note{}, fmt.Errorf("%w %q matches %s", ErrAmbiguous, id, strings.Join(matches, ", "))
	}
	path := filepath.Join(r.Dir, matches[0])
	content, err := os.ReadFile(path) // #nosec G304 -- name came from the configured note directory.
	if err != nil {
		return Note{}, err
	}
	return r.decode(content, path)
}

// List returns every valid matching note and one warning for each malformed or
// unsupported JSON record. Results are newest-updated first.
func (r *Repository) List(ctx context.Context, filter Filter) ([]Note, []Warning, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	entries, err := os.ReadDir(r.Dir)
	if os.IsNotExist(err) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	wantedStatuses := make(map[Status]bool, len(filter.Statuses))
	for _, status := range filter.Statuses {
		wantedStatuses[status] = true
	}
	if len(wantedStatuses) == 0 && !filter.All {
		wantedStatuses[StatusOpen] = true
	}
	wantedTags := normalizeTags(filter.Tags)
	var notes []Note
	var warnings []Warning
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		path := filepath.Join(r.Dir, entry.Name())
		content, readErr := os.ReadFile(path) // #nosec G304 -- name came from the configured note directory.
		if readErr != nil {
			warnings = append(warnings, Warning{Path: path, Err: readErr})
			continue
		}
		n, decodeErr := r.decode(content, path)
		if decodeErr != nil {
			warnings = append(warnings, Warning{Path: path, Err: decodeErr})
			continue
		}
		if len(wantedStatuses) > 0 && !wantedStatuses[n.Status] || !hasAllTags(n.Tags, wantedTags) {
			continue
		}
		notes = append(notes, n)
	}
	sort.Slice(notes, func(i, j int) bool {
		if notes[i].UpdatedAt.Equal(notes[j].UpdatedAt) {
			return notes[i].ID > notes[j].ID
		}
		return notes[i].UpdatedAt.After(notes[j].UpdatedAt)
	})
	return notes, warnings, nil
}

func (r *Repository) Edit(ctx context.Context, id, text string, tags []string) (Note, error) {
	return r.change(ctx, id, func(n *Note) error {
		if n.Status == StatusPromoted {
			return ErrImmutable
		}
		if err := validateText(text); err != nil {
			return err
		}
		n.Text, n.Tags = text, normalizeTags(tags)
		return nil
	})
}

func (r *Repository) Archive(ctx context.Context, id, reason string) (Note, error) {
	return r.change(ctx, id, func(n *Note) error {
		switch n.Status {
		case StatusPromoted:
			return ErrImmutable
		case StatusArchived:
			return nil
		case StatusOpen:
			n.Status = StatusArchived
			n.Archive = &ArchiveMetadata{ArchivedAt: r.now().UTC(), Reason: strings.TrimSpace(reason)}
			return nil
		default:
			return ErrInvalidState
		}
	})
}

func (r *Repository) Reopen(ctx context.Context, id string) (Note, error) {
	return r.change(ctx, id, func(n *Note) error {
		if n.Status != StatusArchived {
			return fmt.Errorf("%w: only archived notes can be reopened", ErrInvalidState)
		}
		n.Status, n.Archive = StatusOpen, nil
		return nil
	})
}

func (r *Repository) PromoteToPlanning(ctx context.Context, id string, link PlanningSessionLink) (Note, error) {
	if strings.TrimSpace(link.ID) == "" {
		return Note{}, errors.New("planning session id is required")
	}
	return r.promote(ctx, id, PromotionLinks{PlanningSession: &link})
}

func (r *Repository) PromoteToPlan(ctx context.Context, id string, link PlanLink) (Note, error) {
	if strings.TrimSpace(link.ID) == "" {
		return Note{}, errors.New("plan id is required")
	}
	return r.promote(ctx, id, PromotionLinks{Plan: &link})
}

func (r *Repository) promote(ctx context.Context, id string, links PromotionLinks) (Note, error) {
	return r.change(ctx, id, func(n *Note) error {
		if n.Status == StatusPromoted {
			if n.Promotion != nil && promotionsEqual(*n.Promotion, links) {
				return nil
			}
			return ErrImmutable
		}
		if n.Status != StatusOpen {
			return fmt.Errorf("%w: only open notes can be promoted", ErrInvalidState)
		}
		n.Status, n.Promotion = StatusPromoted, &links
		return nil
	})
}

func (r *Repository) change(ctx context.Context, id string, mutate func(*Note) error) (Note, error) {
	resolved, err := r.Get(ctx, id)
	if err != nil {
		return Note{}, err
	}
	lock, err := acquireMutationLock(ctx, r.Dir, resolved.ID)
	if err != nil {
		return Note{}, err
	}
	n, changeErr := r.changeLocked(ctx, resolved.ID, mutate)
	if releaseErr := lock.release(); releaseErr != nil {
		return Note{}, errors.Join(changeErr, releaseErr)
	}
	return n, changeErr
}

func (r *Repository) changeLocked(ctx context.Context, id string, mutate func(*Note) error) (Note, error) {
	n, err := r.Get(ctx, id)
	if err != nil {
		return Note{}, err
	}
	before := n
	if err := mutate(&n); err != nil {
		return Note{}, err
	}
	if reflect.DeepEqual(n, before) {
		return n, nil
	}
	n.UpdatedAt = r.now().UTC()
	if err := r.write(ctx, n, false); err != nil {
		return Note{}, err
	}
	return n, nil
}

func (r *Repository) decode(content []byte, path string) (Note, error) {
	var n Note
	if err := json.Unmarshal(content, &n); err != nil {
		return Note{}, fmt.Errorf("decode note: %w", err)
	}
	if n.Schema != Schema {
		return Note{}, fmt.Errorf("unsupported note schema %q", n.Schema)
	}
	if !validID(n.ID) || filepath.Base(path) != n.ID+".json" {
		return Note{}, fmt.Errorf("invalid or mismatched note id %q", n.ID)
	}
	if n.Repo.ID == "" || r.Repo.ID != "" && n.Repo.ID != r.Repo.ID {
		return Note{}, errors.New("note repository does not match store")
	}
	if err := validateText(n.Text); err != nil {
		return Note{}, err
	}
	if n.CreatedAt.IsZero() || n.UpdatedAt.IsZero() {
		return Note{}, errors.New("note timestamps are required")
	}
	switch n.Status {
	case StatusOpen:
		if n.Archive != nil || n.Promotion != nil {
			return Note{}, errors.New("open note has lifecycle metadata")
		}
	case StatusArchived:
		if n.Archive == nil || n.Promotion != nil {
			return Note{}, errors.New("archived note has invalid lifecycle metadata")
		}
	case StatusPromoted:
		if n.Archive != nil || n.Promotion == nil || (n.Promotion.Plan == nil) == (n.Promotion.PlanningSession == nil) {
			return Note{}, errors.New("promoted note must have exactly one promotion link")
		}
	default:
		return Note{}, fmt.Errorf("unsupported note status %q", n.Status)
	}
	n.Tags = normalizeTags(n.Tags)
	return n, nil
}

func (r *Repository) write(ctx context.Context, n Note, exclusive bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := r.ensureDir(); err != nil {
		return err
	}
	content, err := json.MarshalIndent(n, "", "  ")
	if err != nil {
		return err
	}
	content = append(content, '\n')
	if err := ctx.Err(); err != nil {
		return err
	}
	writeErr := atomicfile.Write(r.path(n.ID), content, atomicfile.Options{
		Perm:      0o600,
		Exclusive: exclusive,
		Link:      r.Link,
		Rename:    r.Rename,
	})
	if writeErr == nil {
		return nil
	}
	if exclusive {
		return fmt.Errorf("create note: %w", writeErr)
	}
	return fmt.Errorf("replace note: %w", writeErr)
}

func (r *Repository) ensureDir() error {
	if strings.TrimSpace(r.Dir) == "" {
		return errors.New("note directory is required")
	}
	parent := filepath.Dir(r.Dir)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(parent, 0o700); err != nil { //nolint:gosec // G302: directories require owner search permission.
		return err
	}
	if err := os.MkdirAll(r.Dir, 0o700); err != nil {
		return err
	}
	return os.Chmod(r.Dir, 0o700) //nolint:gosec // G302: directories require owner search permission.
}

func (r *Repository) path(id string) string { return filepath.Join(r.Dir, id+".json") }
func (r *Repository) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}
func (r *Repository) suffix() string {
	if r.IDSuffix != nil {
		return strings.TrimSpace(r.IDSuffix())
	}
	var value [4]byte
	if _, err := rand.Read(value[:]); err == nil {
		return hex.EncodeToString(value[:])
	}
	return fmt.Sprintf("%x", time.Now().UnixNano())
}
func validID(id string) bool { return safeID.MatchString(id) && id != "." && id != ".." }
func validateLookupID(id string) (string, error) {
	id = strings.TrimSpace(id)
	if !validID(id) {
		return "", fmt.Errorf("%w %q", ErrInvalidID, id)
	}
	return id, nil
}
func hasAllTags(tags, wanted []string) bool {
	for _, tag := range wanted {
		if !slices.Contains(tags, tag) {
			return false
		}
	}
	return true
}
func promotionsEqual(a, b PromotionLinks) bool {
	if a.Plan != nil && b.Plan != nil {
		return *a.Plan == *b.Plan
	}
	if a.PlanningSession != nil && b.PlanningSession != nil {
		return *a.PlanningSession == *b.PlanningSession
	}
	return false
}
