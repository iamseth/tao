package cli

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/iamseth/tao/internal/herdr"
	"github.com/iamseth/tao/internal/note"
	"github.com/iamseth/tao/internal/taodata"
)

func planningFixture(t *testing.T) (App, taodata.Repo, note.CatalogNote) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	registered := taodata.Repo{Schema: taodata.RepoSchema, ID: taodata.RepoID(root), Name: "selected", Root: root}
	app, _, _ := noteTestApp(t, strings.NewReader("input"), registered)
	app.AcquireNotePromotionLock = func(context.Context, string, string, string) (func() error, error) {
		t.Fatal("interactive planning must not hold a promotion lock")
		return nil, nil
	}
	registry := app.registry().(*fakeNoteRegistry)
	registry.current = taodata.Repo{ID: "dashboard-checkout", Root: "/not-the-selected-repo"}
	n, err := app.noteRepository(registered).Create(context.Background(), "$(touch bad); --resume\n\x1b[31m untrusted prose", nil)
	if err != nil {
		t.Fatal(err)
	}
	return app, registered, note.CatalogNote{RepositoryID: registered.ID, RepositoryRoot: root, RepositoryName: "not a selector", ID: n.ID, Text: "stale prose"}
}

func TestUINotePlanningCommand(t *testing.T) {
	for _, kind := range []string{"", "pi", "claude"} {
		t.Run("agent="+kind, func(t *testing.T) {
			t.Setenv("TAO_AGENT", kind)
			t.Setenv("TAO_MODEL", "must-not-be-a-flag")
			t.Setenv("TAO_SESSION_TIMEOUT", "1ns")
			t.Setenv("HERDR", "1")
			t.Setenv("HERDR_PANE_ID", "private")
			app, registered, item := planningFixture(t)
			before := planningFiles(t, app.registry().(*fakeNoteRegistry).dir)
			launcher := newUINotePlanningLauncher(app, app.In, app.Out)
			calls := 0
			launcher.run = func(cmd *exec.Cmd) error {
				calls++
				want := kind
				if want == "" {
					want = "pi"
				}
				if !reflect.DeepEqual(cmd.Args, []string{want, "/tao-plan note:" + item.ID}) || cmd.Dir != registered.Root {
					t.Fatalf("command = %+v", cmd)
				}
				if cmd.Stdin != app.In || cmd.Stdout != app.Out || cmd.Stderr != app.Err {
					t.Fatal("streams not attached")
				}
				if !reflect.DeepEqual(cmd.Env, herdr.StripInjectedEnv(os.Environ())) {
					t.Fatal("environment mismatch")
				}
				return nil
			}
			if err := launcher.Launch(context.Background(), item); err != nil {
				t.Fatal(err)
			}
			if calls != 1 {
				t.Fatalf("launch calls = %d", calls)
			}
			if after := planningFiles(t, app.registry().(*fakeNoteRegistry).dir); !reflect.DeepEqual(before, after) {
				t.Fatal("launch wrote metadata")
			}
		})
	}
}

func planningFiles(t *testing.T, root string) map[string]string {
	t.Helper()
	files := map[string]string{}
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		data, err := os.ReadFile(path) //nolint:gosec // Test-owned metadata tree.
		files[path] = string(data)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

type planningReadStore struct {
	NoteRepository
	read func(context.Context, string) (note.Note, error)
}

func (s planningReadStore) Get(ctx context.Context, id string) (note.Note, error) {
	return s.read(ctx, id)
}

func TestUINotePlanningRejectsIneligibleSelection(t *testing.T) {
	for _, damage := range []string{"agent", "missing-repo", "repo-prefix", "repo-id", "schema", "root", "relative-root", "noncanonical-root", "root-id", "health", "stale-root", "missing-note", "note-prefix", "note-id", "note-repo", "note-root", "promoted", "archived", "archive-metadata", "plan-linked", "cancelled"} {
		t.Run(damage, func(t *testing.T) {
			t.Setenv("TAO_AGENT", "pi")
			app, registered, item := planningFixture(t)
			registry := app.registry().(*fakeNoteRegistry)
			store := app.noteRepository(registered)
			current, err := store.Get(context.Background(), item.ID)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch damage {
			case "agent":
				t.Setenv("TAO_AGENT", "unknown")
			case "missing-repo":
				registry.repos = nil
			case "repo-prefix":
				item.RepositoryID = item.RepositoryID[:5]
			case "repo-id":
				app.Registry = func() NoteRegistry {
					return uiCreationRegistry{NoteRegistry: registry, read: func(string) (taodata.Repo, error) { r := registered; r.ID = "wrong"; return r, nil }}
				}
			case "schema":
				registry.repos[0].Schema = "bad"
			case "root":
				registry.repos[0].Root = ""
			case "relative-root":
				registry.repos[0].Root = "relative"
			case "noncanonical-root":
				registry.repos[0].Root += "/."
			case "root-id":
				registry.repos[0].Root, _ = filepath.EvalSymlinks(t.TempDir())
			case "health":
				app.RepoHealthCheck = func(context.Context, taodata.Repo) taodata.RepoHealth {
					return taodata.RepoHealth{Error: true, Status: taodata.RepoHealthNotGitRepo}
				}
			case "stale-root":
				item.RepositoryRoot = "/stale"
			case "missing-note":
				item.ID = "missing"
			case "note-prefix":
				item.ID = item.ID[:8]
			case "note-id":
				current.ID = "wrong"
			case "note-repo":
				current.Repo.ID = "wrong"
			case "note-root":
				current.Repo.Root = "/wrong"
			case "promoted":
				current.Status = note.StatusPromoted
			case "archived":
				current.Status = note.StatusArchived
			case "archive-metadata":
				current.Archive = &note.ArchiveMetadata{}
			case "plan-linked":
				current.Promotion = &note.PromotionLinks{Plan: &note.PlanLink{ID: "plan"}}
			case "cancelled":
				cancel()
			}
			if strings.HasPrefix(damage, "note-") && damage != "note-prefix" || damage == "promoted" || damage == "archived" || damage == "archive-metadata" || damage == "plan-linked" {
				app.NoteRepository = func(string, note.RepoReference) NoteRepository {
					return planningReadStore{NoteRepository: store, read: func(context.Context, string) (note.Note, error) { return current, nil }}
				}
			}
			before := planningFiles(t, registry.dir)
			launcher := newUINotePlanningLauncher(app, app.In, app.Out)
			launcher.run = func(*exec.Cmd) error { t.Fatal("ineligible selection started a process"); return nil }
			if err := launcher.Launch(ctx, item); err == nil {
				t.Fatal("expected rejection")
			}
			if after := planningFiles(t, registry.dir); !reflect.DeepEqual(before, after) {
				t.Fatal("rejection wrote metadata")
			}
		})
	}
}

func TestUINotePlanningReturnsProcessErrors(t *testing.T) {
	for _, failure := range []error{os.ErrNotExist, &exec.ExitError{}, errors.New("start failed")} {
		app, _, item := planningFixture(t)
		t.Setenv("TAO_AGENT", "pi")
		launcher := newUINotePlanningLauncher(app, app.In, app.Out)
		calls := 0
		launcher.run = func(*exec.Cmd) error { calls++; return failure }
		before := planningFiles(t, app.registry().(*fakeNoteRegistry).dir)
		if err := launcher.Launch(context.Background(), item); !errors.Is(err, failure) {
			t.Fatalf("error = %v", err)
		}
		if calls != 1 {
			t.Fatal("process retried")
		}
		if after := planningFiles(t, app.registry().(*fakeNoteRegistry).dir); !reflect.DeepEqual(before, after) {
			t.Fatal("failure wrote metadata")
		}
	}
}
