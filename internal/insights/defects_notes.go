package insights

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"regexp"
	"sort"
	"strings"
	"syscall"

	"github.com/iamseth/tao/internal/note"
)

var defectNoteID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]*$`)

const (
	defectNoteEntries    = 10000
	defectNoteFileBytes  = 1 << 20
	defectNoteTotalBytes = 16 << 20
)

// Missing or rejected note input reduces classification coverage, but does not
// prevent conventional fix collection. No filesystem enumeration prefix is used.
func readDefectNotes(ctx context.Context, repositoryID, dir string) ([]DefectNote, bool) {
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, false
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, false
	}
	defer func() { _ = root.Close() }()
	opened, err := root.Stat(".")
	if err != nil || !os.SameFile(info, opened) {
		return nil, false
	}
	d, err := root.Open(".")
	if err != nil {
		return nil, false
	}
	entries, err := d.ReadDir(defectNoteEntries + 1)
	_ = d.Close()
	if (err != nil && !errors.Is(err, io.EOF)) || len(entries) > defectNoteEntries {
		return nil, false
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	complete, total := true, 0
	var notes []DefectNote
	for _, entry := range entries {
		if ctx.Err() != nil {
			return notes, false
		}
		if !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		before, err := root.Lstat(entry.Name())
		if err != nil || !before.Mode().IsRegular() {
			complete = false
			continue
		}
		f, err := root.OpenFile(entry.Name(), os.O_RDONLY|syscall.O_NONBLOCK, 0)
		if err != nil {
			complete = false
			continue
		}
		after, statErr := f.Stat()
		if statErr != nil || !after.Mode().IsRegular() || !os.SameFile(before, after) {
			_ = f.Close()
			complete = false
			continue
		}
		remaining := min(defectNoteFileBytes, defectNoteTotalBytes-total)
		data, readErr := io.ReadAll(io.LimitReader(f, int64(remaining+1)))
		_ = f.Close()
		total += len(data)
		if total > defectNoteTotalBytes {
			return notes, false
		}
		if readErr != nil || len(data) > defectNoteFileBytes {
			complete = false
			continue
		}
		var n note.Note
		if json.Unmarshal(data, &n) != nil || n.Schema != note.Schema || n.Repo.ID != repositoryID || n.ID+".json" != entry.Name() || !defectNoteID.MatchString(n.ID) || !validDefectNoteLifecycle(n) || strings.TrimSpace(n.Text) == "" || len(n.Text) > note.MaxText || (n.Status != note.StatusOpen && n.Status != note.StatusArchived && n.Status != note.StatusPromoted) {
			complete = false
			continue
		}
		v := DefectNote{RepositoryID: repositoryID, Text: n.Text}
		for _, tag := range n.Tags {
			v.Tags = append(v.Tags, strings.ToLower(strings.TrimSpace(tag)))
		}
		if n.Archive != nil && n.Archive.Plan != nil {
			v.ArchivePlanID = n.Archive.Plan.ID
		}
		if n.Promotion != nil && n.Promotion.Plan != nil {
			v.LegacyPromotionPlanID = n.Promotion.Plan.ID
		}
		notes = append(notes, v)
	}
	return notes, complete
}

// Mirror the persisted model's lifecycle admission without invoking its store
// reader (which does not provide these collection byte bounds).
func validDefectNoteLifecycle(n note.Note) bool {
	if n.CreatedAt.IsZero() || n.UpdatedAt.IsZero() {
		return false
	}
	switch n.Status {
	case note.StatusOpen:
		return n.Archive == nil && n.Promotion == nil
	case note.StatusArchived:
		return n.Archive != nil && n.Promotion == nil && (n.Archive.PlanningSession == nil || n.Archive.Plan != nil) && (n.Archive.Plan == nil || strings.TrimSpace(n.Archive.Plan.ID) != "")
	case note.StatusPromoted:
		return n.Archive == nil && n.Promotion != nil && ((n.Promotion.Plan == nil) != (n.Promotion.PlanningSession == nil))
	default:
		return false
	}
}
