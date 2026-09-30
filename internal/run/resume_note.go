package run

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/iamseth/tao/internal/agentinput"
	"github.com/iamseth/tao/internal/atomicfile"
	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/internal/taodata"
)

// ResumeNoteStore holds disposable agent-authored context, never lifecycle or
// recovery evidence. Nothing in ordinary plan loading consults this cache.
// Callers must omit failures best-effort and enforce admission independently.
// DataHome is independent of the plan directory, including custom plans roots.
type ResumeNoteStore struct{ DataHome string }

// JSON escaping may expand each input byte sixfold. Identity and revision are
// fixed-size digests; no paths, blocker prose, or event history are persisted.
const maxResumeNoteCacheBytes = 6*agentinput.MaxFileBytes + 1024

type resumeNoteEnvelope struct {
	Version  int    `json:"version"`
	Identity string `json:"identity"`
	Revision string `json:"revision"`
	Text     string `json:"text"`
}

// ReadResumeNoteFile accepts only bounded, nonempty regular-file text.
func ReadResumeNoteFile(path string) (string, error) {
	data, err := readResumeNoteBytes(path, agentinput.MaxFileBytes)
	if err != nil {
		return "", err
	}
	return boundedResumeNote(string(data))
}

func boundedResumeNote(text string) (string, error) {
	if int64(len(text)) > agentinput.MaxFileBytes || !utf8.ValidString(text) {
		return "", errors.New("resume note exceeds byte limit or is not UTF-8")
	}
	text, err := agentinput.BoundedText(text, "resume note")
	if err != nil {
		return "", err
	}
	if text == "" {
		return "", errors.New("resume note is empty")
	}
	return text, nil
}

func readResumeNoteBytes(path string, limit int64) ([]byte, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, os.ErrNotExist
	}
	if err != nil {
		return nil, errors.New("cannot inspect resume note")
	}
	if !info.Mode().IsRegular() || info.Size() > limit {
		return nil, errors.New("resume note is not a bounded regular file")
	}
	data, err := agentinput.ReadBoundedFile(path, "resume note", limit)
	if err != nil {
		return nil, errors.New("cannot read bounded resume note")
	}
	return data, nil
}

// Save replaces the single entry for a currently blocked slice. The supplied
// detail must be the settled Tao snapshot after blocking, not an agent claim.
func (s ResumeNoteStore) Save(detail *plan.PlanDetail, sliceID, text string) error {
	text, err := boundedResumeNote(text)
	if err != nil {
		return err
	}
	revision := resumeNoteRevision(detail, sliceID)
	if revision == "" {
		return errors.New("resume note requires an unambiguous current blocked slice")
	}
	path, identity, err := s.entry(detail.Dir, sliceID, detail, true)
	if err != nil {
		return err
	}
	data, err := json.Marshal(resumeNoteEnvelope{Version: 1, Identity: identity, Revision: revision, Text: text})
	if err != nil || int64(len(data)) > maxResumeNoteCacheBytes {
		return errors.New("cannot encode bounded resume note")
	}
	if err := atomicfile.Write(path, data, atomicfile.Options{Perm: 0o600}); err != nil {
		return errors.New("cannot publish resume note")
	}
	return nil
}

// Load compares advisory freshness only. To hand context across ContinueBlocked,
// use an immutable detail snapshot captured BEFORE that mutation clears the
// blocker and updates timing; loading the continued live detail returns nothing.
// An old snapshot is not proof that continuing or recovering is safe.
func (s ResumeNoteStore) Load(detail *plan.PlanDetail, sliceID string) (string, error) {
	revision := resumeNoteRevision(detail, sliceID)
	if revision == "" || !filepath.IsAbs(detail.Dir) {
		return "", nil
	}
	path, identity, err := s.entry(detail.Dir, sliceID, detail, false)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	data, err := readResumeNoteBytes(path, maxResumeNoteCacheBytes)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	var envelope resumeNoteEnvelope
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&envelope); err != nil {
		return "", errors.New("invalid resume note cache envelope")
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return "", errors.New("invalid resume note cache trailer")
	}
	if envelope.Version != 1 {
		return "", errors.New("unsupported resume note cache version")
	}
	if envelope.Identity != identity || envelope.Revision != revision {
		return "", nil
	}
	return boundedResumeNote(envelope.Text)
}

// Clear removes only advisory cache data. Failed deletion cannot make an old
// entry fresh against a newer block, restart, or completion.
func (s ResumeNoteStore) Clear(planDir, sliceID string) error {
	path, _, err := s.entry(planDir, sliceID, nil, false)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := atomicfile.Remove(path, atomicfile.RemoveOptions{}); err != nil && !errors.Is(err, os.ErrNotExist) {
		return errors.New("cannot remove resume note")
	}
	return nil
}

func resumeNoteRevision(detail *plan.PlanDetail, sliceID string) string {
	if detail == nil || sliceID == "" || detail.State.Plan.ID == "" || detail.State.Status != plan.StatusBlocked ||
		detail.State.Plan.CurrentSlice == nil || *detail.State.Plan.CurrentSlice != sliceID ||
		detail.State.Plan.Timing.CompletedAt != nil || slices.Contains(detail.State.Plan.CompletedSlices, sliceID) ||
		!slices.Contains(detail.State.Plan.PendingSlices, sliceID) {
		return ""
	}
	var selected *plan.Slice
	for i := range detail.Slices.Slices {
		candidate := &detail.Slices.Slices[i]
		if candidate.ID == sliceID {
			if selected != nil {
				return ""
			}
			selected = candidate
		} else if candidate.Status == plan.StatusBlocked {
			return ""
		}
	}
	if selected == nil || selected.Status != plan.StatusBlocked || strings.TrimSpace(selected.BlockerNote) == "" ||
		selected.Completion != nil || selected.CommitIntent != nil || selected.Timing.CompletedAt != nil ||
		selected.Timing.StartedAt == nil || selected.Timing.StartedAt.IsZero() || selected.ExecutionRoot == "" ||
		selected.Timing.UpdatedAt.IsZero() || selected.Timing.LastActivityAt == nil ||
		!selected.Timing.UpdatedAt.Equal(*selected.Timing.LastActivityAt) || selected.Timing.UpdatedAt.Before(*selected.Timing.StartedAt) {
		return ""
	}
	// Hash a small projection, not arbitrary artifact extras. Timing catches an
	// idempotent block with no new event. Only block and execution-generation
	// events distinguish executions: post-block metrics and timeout diagnostics
	// are advisory and must not invalidate a note saved by the active session.
	h := sha256.New()
	encoder := json.NewEncoder(h)
	values := []any{detail.State.Plan.ID, detail.State.CreatedAt, detail.State.UpdatedAt,
		selected.Timing, selected.BlockerNote, selected.ExecutionRoot, selected.ExecutionStart}
	for _, value := range values {
		if encoder.Encode(value) != nil {
			return ""
		}
	}
	started, blocked := false, false
	var previous time.Time
	for _, event := range detail.Events {
		if event.SliceID != sliceID {
			continue
		}
		switch event.Type {
		case plan.EventTypeSliceStarted, plan.EventTypeSliceBlocked, plan.EventTypeSliceRestarted,
			plan.EventTypeSliceResumeAttempted, plan.EventTypeSliceCompleted:
		default:
			continue
		}
		if event.Timestamp.IsZero() || event.PlanID != detail.State.Plan.ID || event.Timestamp.After(selected.Timing.UpdatedAt) || event.Timestamp.Before(previous) {
			return ""
		}
		previous = event.Timestamp
		if encoder.Encode([]any{event.Type, event.Timestamp}) != nil {
			return ""
		}
		switch event.Type {
		case plan.EventTypeSliceStarted:
			started, blocked = !event.Timestamp.Before(*selected.Timing.StartedAt), false
		case plan.EventTypeSliceRestarted, plan.EventTypeSliceCompleted:
			started, blocked = false, false
		case plan.EventTypeSliceResumeAttempted:
			blocked = false
		case plan.EventTypeSliceBlocked:
			blocked = started
		}
	}
	if !blocked {
		return ""
	}
	return hex.EncodeToString(h.Sum(nil))
}

func (s ResumeNoteStore) entry(planDir, sliceID string, detail *plan.PlanDetail, create bool) (string, string, error) {
	if !filepath.IsAbs(planDir) || sliceID == "" {
		return "", "", errors.New("ambiguous resume note identity")
	}
	canonical, err := filepath.EvalSymlinks(planDir)
	if err != nil {
		return "", "", os.ErrNotExist
	}
	info, err := os.Stat(canonical)
	if err != nil || !info.IsDir() {
		return "", "", os.ErrNotExist
	}
	identityData, _ := json.Marshal([]string{canonical, sliceID})
	identity := fmt.Sprintf("%x", sha256.Sum256(identityData))
	home := s.DataHome
	if home == "" {
		home = taodata.DataHome()
	}
	home, err = filepath.Abs(home)
	if err != nil {
		return "", "", errors.New("cannot resolve resume note data home")
	}
	// Reject all symlink components rather than following an alias into a
	// checkout. Existing ancestors are inspected before creating any directory.
	if err := inspectResumeNotePath(home); err != nil {
		return "", "", err
	}
	if detail != nil {
		roots := []string{detail.State.Repo.Root}
		if detail.State.Workspace != nil {
			roots = append(roots, detail.State.Workspace.Path)
		}
		for _, slice := range detail.Slices.Slices {
			if slice.ExecutionRoot != "" {
				roots = append(roots, slice.ExecutionRoot)
			}
		}
		for _, root := range roots {
			if root == "" {
				continue
			}
			resolved, err := filepath.EvalSymlinks(root)
			if err != nil || !filepath.IsAbs(resolved) {
				return "", "", errors.New("cannot exclude resume note worktree storage")
			}
			rel, err := filepath.Rel(resolved, home)
			if err != nil || rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))) {
				return "", "", errors.New("resume note data home is inside a worktree")
			}
		}
		if detail.State.Repo.Root == "" {
			return "", "", errors.New("missing resume note repository identity")
		}
	}
	dir := filepath.Join(home, "run-resume")
	path := filepath.Join(dir, identity+".json")
	if err := inspectResumeNotePath(path); err != nil {
		return "", "", err
	}
	if create {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return "", "", errors.New("cannot create resume note cache")
		}
	}
	dirInfo, err := os.Lstat(dir)
	if errors.Is(err, os.ErrNotExist) {
		return "", "", os.ErrNotExist
	}
	if err != nil || !dirInfo.IsDir() || dirInfo.Mode().Perm() != 0o700 {
		return "", "", errors.New("resume note cache is not private")
	}
	if info, err := os.Lstat(path); err == nil {
		if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
			return "", "", errors.New("resume note entry is not private regular data")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", "", errors.New("cannot inspect resume note entry")
	}
	return path, identity, nil
}

func inspectResumeNotePath(path string) error {
	for current := path; ; current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return errors.New("cannot inspect resume note storage path")
		}
		if err == nil {
			if info.Mode()&os.ModeSymlink != 0 {
				return errors.New("resume note storage path contains a symlink")
			}
			if current != path && !info.IsDir() {
				return errors.New("resume note storage parent is not a directory")
			}
			if info.IsDir() {
				if _, err := os.Lstat(filepath.Join(current, ".git")); !errors.Is(err, os.ErrNotExist) {
					return errors.New("resume note storage may be inside a worktree")
				}
			}
		}
		if filepath.Dir(current) == current {
			break
		}
	}
	return nil
}
