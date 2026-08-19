package planning

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// TestPlanningSessionRepositoryReadsLegacyNoteProvenance verifies historical
// and unknown source fields remain readable while recognized note provenance
// is retained.
func TestPlanningSessionRepositoryReadsLegacyNoteProvenance(t *testing.T) {
	dataHome := t.TempDir()
	repoID := "repo-a"
	sessionID := "20260101-000000-legacy"
	legacy := `{
		"schema": "tao.planning.session.v1",
		"id": "` + sessionID + `",
		"title": "Legacy planning draft",
		"status": "draft",
		"repo": {"id": "` + repoID + `", "name": "Repo A", "root": "/work/repo-a"},
		"source": {"type": "note", "historical_field": true, "note": {"id": "note-9", "text": "old note", "tags": ["web"], "captured_at": "2026-01-01T00:00:00Z", "unknown": "ok"}},
		"created_at": "2026-01-01T00:00:00Z",
		"updated_at": "2026-01-01T00:00:00Z",
		"last_activity_at": "2026-01-01T00:00:00Z",
		"messages": [{"id": "msg-001", "role": "user", "content": "old note", "created_at": "2026-01-01T00:00:00Z"}]
	}`
	sessionDir := filepath.Join(dataHome, "repos", repoID, "planning-sessions", sessionID)
	if err := os.MkdirAll(sessionDir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sessionDir, "session.json"), []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}

	store := NewFileRepository(dataHome)
	loaded, err := store.GetSession(context.Background(), QualifyID(repoID, sessionID))
	if err != nil {
		t.Fatalf("legacy session with source.note should deserialize, got error: %v", err)
	}
	if loaded.ID != sessionID || loaded.Title != "Legacy planning draft" || len(loaded.Messages) != 1 {
		t.Fatalf("unexpected loaded legacy session: %+v", loaded)
	}
	if loaded.Source == nil || loaded.Source.Note == nil || loaded.Source.Note.ID != "note-9" {
		t.Fatalf("expected historical source note to remain readable: %+v", loaded.Source)
	}
}
