package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/iamseth/tao/internal/intervention"
	"github.com/iamseth/tao/internal/note"
	"github.com/iamseth/tao/internal/taodata"
	"github.com/iamseth/tao/internal/testenv"
)

func TestInitJSONRegistersRepoAndAllocatesPlan(t *testing.T) {
	repoRoot := initTestGitRepo(t)
	dataHome := t.TempDir()
	t.Setenv("TAO_DATA_HOME", dataHome)

	var out bytes.Buffer
	app := newTestApp(App{Out: &out, Err: &out})
	withWorkingDir(t, repoRoot, func() {
		if err := app.Run(context.Background(), []string{"init", "--slug", "Example Plan", "--json"}); err != nil {
			t.Fatalf("init failed: %v\n%s", err, out.String())
		}
	})

	var payload struct {
		Schema   string `json:"schema"`
		DataHome string `json:"data_home"`
		Repo     struct {
			Schema    string `json:"schema"`
			UpdatedAt string `json:"updated_at"`
			ID        string `json:"id"`
			Name      string `json:"name"`
			Root      string `json:"root"`
			Branch    string `json:"branch"`
			RemoteURL string `json:"remote_url"`
		} `json:"repo"`
		Plan struct {
			ID  string `json:"id"`
			Dir string `json:"dir"`
		} `json:"plan"`
	}
	decoder := json.NewDecoder(bytes.NewReader(out.Bytes()))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&payload); err != nil {
		t.Fatalf("invalid JSON output: %v\n%s", err, out.String())
	}
	if payload.Schema != "tao.init.v1" || payload.DataHome != dataHome {
		t.Fatalf("unexpected init payload: %+v", payload)
	}
	if payload.Repo.ID == "" || payload.Repo.Root != repoRoot || payload.Repo.Branch != "main" || payload.Repo.RemoteURL != "https://example.com/repo.git" {
		t.Fatalf("unexpected repo payload: %+v", payload.Repo)
	}
	if !strings.HasSuffix(payload.Plan.ID, "-example-plan") {
		t.Fatalf("unexpected plan id %q", payload.Plan.ID)
	}
	if payload.Plan.Dir != filepath.Join(dataHome, "repos", payload.Repo.ID, "plans", payload.Plan.ID) {
		t.Fatalf("unexpected plan dir %q", payload.Plan.Dir)
	}
	if _, err := os.Stat(filepath.Join(dataHome, "repos", payload.Repo.ID, "repo.json")); err != nil {
		t.Fatalf("repo.json not written: %v", err)
	}
	if info, err := os.Stat(payload.Plan.Dir); err != nil || !info.IsDir() {
		t.Fatalf("plan dir not created: info=%v err=%v", info, err)
	}
	if covered, err := intervention.ReadCoverage(payload.Plan.Dir, payload.Plan.ID); err != nil || !covered {
		t.Fatalf("init allocation coverage = %v, %v", covered, err)
	}
	entries, err := os.ReadDir(payload.Plan.Dir)
	if err != nil || len(entries) != 1 || entries[0].Name() != "intervention-coverage.json" {
		t.Fatalf("init wrote core artifacts or sidecars: %v, %v", entries, err)
	}
}

func TestInitDefaultOutputRegistersWithoutPlan(t *testing.T) {
	repoRoot := initTestGitRepo(t)
	dataHome := t.TempDir()
	t.Setenv("TAO_DATA_HOME", dataHome)

	var out bytes.Buffer
	app := newTestApp(App{Out: &out, Err: &out})
	withWorkingDir(t, repoRoot, func() {
		if err := app.Run(context.Background(), []string{"init"}); err != nil {
			t.Fatalf("init failed: %v\n%s", err, out.String())
		}
	})
	text := out.String()
	for _, want := range []string{"registered Tao repository", "data home: " + dataHome, "repo: "} {
		if !strings.Contains(text, want) {
			t.Fatalf("init output missing %q: %s", want, text)
		}
	}
	plansRoot := filepath.Join(dataHome, "repos")
	if _, err := os.Stat(plansRoot); err != nil {
		t.Fatalf("repo registration not written: %v", err)
	}
	if err := filepath.WalkDir(plansRoot, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Name() == "intervention-coverage.json" || entry.Name() == "plans" {
			t.Errorf("registration allocated plan storage: %s", path)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(text, "plan dir:") {
		t.Fatalf("default init should not allocate a plan: %s", text)
	}
}

func initTestGitRepo(t *testing.T) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	initRunGit(t, root, "init", "-b", "main")
	initRunGit(t, root, "remote", "add", "origin", "https://example.com/repo.git")
	return root
}

func initRunGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...) //nolint:gosec // G204: fixed git command with test args
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, out)
	}
}

func withWorkingDir(t *testing.T, dir string, fn func()) {
	t.Helper()
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := os.Chdir(old); err != nil {
			t.Fatal(err)
		}
	}()
	fn()
}

func TestInitSourceNote(t *testing.T) {
	testenv.SkipIfShort(t)
	for _, tc := range []struct {
		name, selector, status, destination, want string
		noSlug, foreign, otherRepo                bool
	}{
		{name: "open exact", selector: "abcdef01", status: "open"},
		{name: "open prefix", selector: "abcd", status: "open"},
		{name: "archived", selector: "abcd", status: "archived", want: "archived"},
		{name: "archived linked", selector: "abcd", status: "archived", destination: "old-plan", want: "old-plan"},
		{name: "promoted plan", selector: "abcd", status: "promoted", destination: "old-plan", want: "old-plan"},
		{name: "legacy planning", selector: "abcd", status: "promoted", destination: "session", want: "old-session"},
		{name: "missing", selector: "eeee", status: "open", want: "note not found"},
		{name: "ambiguous", selector: "ab", status: "open", want: "ambiguous"},
		{name: "malformed selector", selector: "../bad", status: "open", want: "invalid note id"},
		{name: "empty", selector: "", status: "open", want: "note"},
		{name: "missing slug", selector: "abcd", status: "open", noSlug: true, want: "slug"},
		{name: "foreign identity", selector: "abcd", status: "open", foreign: true, want: "repository"},
		{name: "other repository", selector: "abcd", status: "open", otherRepo: true, want: "note not found"},
		{name: "malformed record", selector: "abcd", status: "broken", want: "decode note"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := initTestGitRepo(t)
			home := t.TempDir()
			t.Setenv("TAO_DATA_HOME", home)
			withWorkingDir(t, root, func() {
				ctx := context.Background()
				registry := taodata.NewRegistry("")
				repo, err := registry.RegisterCurrent(ctx)
				if err != nil {
					t.Fatal(err)
				}
				dir := registry.NotesDir(repo)
				if tc.otherRepo {
					dir = filepath.Join(home, "repos", "foreign", "notes")
				}
				if err := os.MkdirAll(dir, 0700); err != nil {
					t.Fatal(err)
				}
				n := note.Note{Schema: note.Schema, ID: "abcdef01", Repo: note.RepoReference{ID: repo.ID}, Text: "source", CreatedAt: time.Now(), UpdatedAt: time.Now(), Status: note.Status(tc.status)}
				if tc.foreign {
					n.Repo.ID = "foreign"
				}
				if tc.status == "archived" {
					n.Archive = &note.ArchiveMetadata{ArchivedAt: time.Now()}
					if tc.destination != "" {
						n.Archive.Plan = &note.PlanLink{ID: tc.destination}
					}
				}
				if tc.status == "promoted" {
					n.Promotion = &note.PromotionLinks{}
					if tc.destination == "session" {
						n.Promotion.PlanningSession = &note.PlanningSessionLink{ID: "old-session"}
					} else {
						n.Promotion.Plan = &note.PlanLink{ID: tc.destination}
					}
				}
				content, err := json.Marshal(n)
				if err != nil {
					t.Fatal(err)
				}
				if tc.status == "broken" {
					content = []byte("{")
				}
				path := filepath.Join(dir, n.ID+".json")
				if err := os.WriteFile(path, content, 0600); err != nil {
					t.Fatal(err)
				}
				if tc.name == "ambiguous" {
					if err := os.WriteFile(filepath.Join(dir, "ab000002.json"), content, 0600); err != nil {
						t.Fatal(err)
					}
				}
				before := initFileSnapshot(t, home)
				var out bytes.Buffer
				app := newTestApp(App{Out: &out, Err: &out})
				args := []string{"init", "--note", tc.selector, "--json"}
				if !tc.noSlug {
					args = append(args, "--slug", "new-plan")
				}
				err = app.Run(ctx, args)
				if tc.want != "" {
					if err == nil || !strings.Contains(err.Error(), tc.want) {
						t.Fatalf("wanted %q, got %v", tc.want, err)
					}
					if tc.status == "archived" || tc.status == "promoted" {
						if !strings.Contains(err.Error(), n.ID) || !strings.Contains(err.Error(), tc.status) {
							t.Fatalf("missing resolved identity/status: %v", err)
						}
					}
					if after := initFileSnapshot(t, home); !reflect.DeepEqual(before, after) {
						t.Fatalf("refusal changed note/allocation files: before=%v after=%v", before, after)
					}
				} else {
					if err != nil {
						t.Fatal(err)
					}
					var payload struct {
						SourceNote struct {
							ID         string `json:"id"`
							Repository string `json:"repository"`
						} `json:"source_note"`
						Plan taodata.PlanAllocation `json:"plan"`
					}
					if err := json.Unmarshal(out.Bytes(), &payload); err != nil {
						t.Fatal(err)
					}
					if payload.SourceNote.ID != n.ID || payload.SourceNote.Repository != repo.ID || payload.Plan.ID == "" {
						t.Fatalf("bad payload: %s", out.String())
					}
					got, err := os.ReadFile(path) // #nosec G304 -- path is a note fixture in an isolated test data home.
					if err != nil || !bytes.Equal(content, got) {
						t.Fatalf("preflight mutated note: %v", err)
					}
					// Preflight is not a reservation: another plan can consume the note.
					store := note.NewRepository(dir, n.Repo)
					if _, err := store.ArchiveToPlan(ctx, n.ID, note.PlanLink{ID: "racing-plan"}); err != nil {
						t.Fatal(err)
					}
					if _, err := store.ArchiveToPlan(ctx, n.ID, note.PlanLink{ID: payload.Plan.ID}); err == nil {
						t.Fatal("conflicting archive accepted")
					}
				}
			})
		})
	}
}

// Ignore registration timestamps, but capture every note and allocation artifact.
func initFileSnapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	files := map[string]string{}
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Name() == "repo.json" {
			return nil
		}
		if entry.IsDir() {
			files[path] = "directory"
			return nil
		}
		content, err := os.ReadFile(path) // #nosec G304 G122 -- isolated test-owned fixture tree has no concurrent writers or symlinks.
		if err != nil {
			return err
		}
		files[path] = string(content)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}
