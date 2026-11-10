package cli

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/iamseth/tao/internal/insights"
	"github.com/iamseth/tao/internal/note"
	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/internal/taodata"
	"github.com/iamseth/tao/internal/testenv"
)

// TestInsightsDefectsIntegration is the assembled CLI/local-Git boundary gate.
func TestInsightsDefectsIntegration(t *testing.T) {
	testenv.SkipIfShort(t)
	for _, viaNote := range []bool{false, true} {
		name := "commit"
		if viaNote {
			name = "bug-note"
		}
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			git := func(args ...string) string {
				t.Helper()
				cmd := exec.Command("git", append([]string{"-C", root}, args...)...) // #nosec G204 -- test-owned fixture.
				cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1", "GIT_AUTHOR_DATE=2026-01-02T00:00:00Z", "GIT_COMMITTER_DATE=2026-01-02T00:00:00Z")
				b, err := cmd.CombinedOutput()
				if err != nil {
					t.Fatalf("git %v: %s: %v", args, b, err)
				}
				return strings.TrimSpace(string(b))
			}
			write := func(path, text string) {
				t.Helper()
				if err := os.WriteFile(path, []byte(text), 0600); err != nil { // #nosec G703 -- paths belong to test-created temporary fixtures.
					t.Fatal(err)
				}
			}
			git("init", "-b", "main")
			git("config", "user.name", "Fixture")
			git("config", "user.email", "fixture@example.invalid")
			git("commit", "--allow-empty", "-m", "chore: seed")
			target, other, fixPlan := "20260101-000000-target", "20260101-000001-other", "20260102-000000-repair"
			write(filepath.Join(root, "file"), "first\n")
			git("add", "file")
			git("commit", "-m", "feat: target\n\nTao-Plan: "+target)
			merged := git("rev-parse", "HEAD")
			write(filepath.Join(root, "file"), "bad\n")
			git("add", "file")
			git("commit", "-m", "feat: other\n\nTao-Plan: "+other)
			otherMerge := git("rev-parse", "HEAD")
			write(filepath.Join(root, "file"), "good\n")
			git("add", "file")
			message := "fix: repair\n\nFixes-Plan: " + target
			if viaNote {
				message = "chore: repair\n\nTao-Plan: " + fixPlan
			}
			git("commit", "-m", message)
			tip := git("rev-parse", "HEAD")
			registered := taodata.Repo{ID: "local", Root: root}
			app, out, _ := noteTestApp(t, nil, registered)
			notesDir := app.registry().NotesDir(registered)
			if err := os.MkdirAll(notesDir, 0700); err != nil {
				t.Fatal(err)
			}
			if viaNote {
				n := note.Note{Schema: note.Schema, ID: "fixture", Repo: note.RepoReference{ID: registered.ID}, Status: note.StatusArchived, Tags: []string{" BUG "}, Text: "Fixes-Plan: " + target, CreatedAt: time.Now(), UpdatedAt: time.Now(), Archive: &note.ArchiveMetadata{Plan: &note.PlanLink{ID: fixPlan}}}
				b, err := json.Marshal(n)
				if err != nil {
					t.Fatal(err)
				}
				write(filepath.Join(notesDir, "fixture.json"), string(b))
			}
			var summaries []plan.PlanSummary
			for i, id := range []string{target, other} {
				dir := t.TempDir()
				sha := merged
				if i == 1 {
					sha = otherMerge
				}
				write(filepath.Join(dir, "events.jsonl"), `{"type":"plan_created","timestamp":"2026-01-01T00:00:00Z","agent":"pi"}`+"\n"+`{"type":"plan_merged","timestamp":"2026-01-02T00:00:00Z","merged_default_sha":"`+sha+`"}`+"\n")
				summaries = append(summaries, plan.PlanSummary{ID: id, Dir: dir, Status: plan.StatusCompleted})
			}
			app.Repository = func(string) Repository { return fakeRepository{summaries: summaries} }
			if err := app.Run(context.Background(), []string{"insights", "--trend", "--json"}); err != nil {
				t.Fatal(err)
			}
			var trend insights.TrendReport
			if err := json.Unmarshal(out.Bytes(), &trend); err != nil {
				t.Fatal(err)
			}
			d := trend.Defects
			if d == nil || d.Coverage.Scan.PinnedTip != tip || d.Count.Numerator != 1 || d.Count.Denominator != 2 || len(d.Plans) != 2 || d.Plans[0].ID != target || d.Plans[0].Methods.Explicit != 1 || d.Plans[1].WindowFixes != 0 {
				t.Fatalf("unexpected attribution: %s", out)
			}
			out.Reset()
			if err := app.Run(context.Background(), []string{"insights", "--trend"}); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(out.String(), tip) || !strings.Contains(out.String(), "assertion") || strings.Contains(out.String(), "chore: repair") {
				t.Fatalf("Markdown: %s", out)
			}
			// One readable store with no checkout and one damaged metadata record retain history.
			registry := taodata.NewRegistry("")
			if viaNote {
				b, err := os.ReadFile(filepath.Join(notesDir, "fixture.json")) // #nosec G304 -- test-owned fixture.
				if err != nil {
					t.Fatal(err)
				}
				catalogNotes := registry.NotesDir(registered)
				if err := os.MkdirAll(catalogNotes, 0700); err != nil {
					t.Fatal(err)
				}
				write(filepath.Join(catalogNotes, "fixture.json"), string(b))
				// A similar tag is not the exact normalized bug tag.
				write(filepath.Join(notesDir, "fixture.json"), strings.Replace(string(b), " BUG ", "bugfix", 1))
				out.Reset()
				if err := app.Run(context.Background(), []string{"insights", "--trend", "--json"}); err != nil {
					t.Fatal(err)
				}
				var unclassified insights.TrendReport
				if err := json.Unmarshal(out.Bytes(), &unclassified); err != nil {
					t.Fatal(err)
				}
				if unclassified.Defects == nil || unclassified.Defects.Count.Numerator != 0 {
					t.Fatalf("bugfix tag classified: %s", out)
				}
			}
			for _, id := range []string{"missing", "damaged"} {
				if err := registry.WriteRepo(taodata.Repo{Schema: taodata.RepoSchema, ID: id, Root: filepath.Join(t.TempDir(), "absent")}); err != nil {
					t.Fatal(err)
				}
			}
			write(filepath.Join(registry.DataHome, "repos", "damaged", "repo.json"), "{")
			out.Reset()
			if err := app.Run(context.Background(), []string{"insights", "--all-repos", "--trend", "--json"}); err != nil {
				t.Fatal(err)
			}
			trend = insights.TrendReport{}
			if err := json.Unmarshal(out.Bytes(), &trend); err != nil {
				t.Fatal(err)
			}
			if len(trend.DefectRepositories) != 3 || trend.Coverage.Plans != 6 {
				t.Fatalf("stores lost: %s", out)
			}
			for _, report := range trend.DefectRepositories {
				if report.RepositoryID == "local" {
					if report.Coverage.Scan.PinnedTip != tip || report.Count.Numerator != 1 {
						t.Fatalf("local pin: %+v", report)
					}
				} else if report.Availability != insights.DefectUnavailable || report.Count.Numerator != 0 {
					t.Fatalf("borrowed history: %+v", report)
				}
			}
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			if err := app.Run(ctx, []string{"insights", "--trend"}); !errors.Is(err, context.Canceled) {
				t.Fatalf("cancel: %v", err)
			}
			if git("rev-parse", "HEAD") != tip || git("status", "--porcelain") != "" {
				t.Fatal("collection mutated fixture")
			}
		})
	}
}

type defectTrackingRegistry struct {
	*fakeNoteRegistry
	noteReads int
}

func (r *defectTrackingRegistry) NotesDir(repo taodata.Repo) string {
	r.noteReads++
	return r.fakeNoteRegistry.NotesDir(repo)
}

func TestInsightsDefectsModeIsolation(t *testing.T) {
	repo := taodata.Repo{ID: "defects-repo", Root: t.TempDir()}
	app, out, diagnostic := noteTestApp(t, nil, repo)
	app.Repository = func(string) Repository { return fakeRepository{} }
	registry := &defectTrackingRegistry{fakeNoteRegistry: app.registry().(*fakeNoteRegistry)}
	app.Registry = func() NoteRegistry { return registry }
	calls := 0
	probes := [][]string{{"symbolic-ref", "--quiet", "refs/remotes/origin/HEAD"}, {"rev-parse", "--verify", "--end-of-options", "refs/heads/main^{commit}"}, {"rev-parse", "--verify", "--end-of-options", "refs/heads/master^{commit}"}}
	prefix := []string{"-C", repo.Root, "--no-optional-locks", "--no-lazy-fetch", "--no-pager", "--no-replace-objects", "--literal-pathspecs", "-c", "diff.autoRefreshIndex=false", "-c", "core.fsmonitor=false", "-c", "core.quotePath=false", "-c", "blame.ignoreRevsFile="}
	app.CommandRunner = func(_ context.Context, _, name string, args []string, _, _ io.Writer) error {
		calls++
		if name != "git" || calls > len(probes) || !reflect.DeepEqual(args, append(append([]string(nil), prefix...), probes[calls-1]...)) {
			t.Fatalf("unexpected command: %s %v", name, args)
		}
		return errors.New("missing local default ref")
	}
	for _, mode := range [][]string{nil, {"--digest"}, {"--scorecard"}} {
		if err := app.Run(context.Background(), append([]string{"insights"}, mode...)); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 0 || registry.noteReads != 0 {
		t.Fatalf("non-trend reads: Git=%d notes=%d", calls, registry.noteReads)
	}
	out.Reset()
	if err := app.Run(context.Background(), []string{"insights", "--trend", "--json"}); err != nil {
		t.Fatal(err)
	}
	if calls == 0 || !strings.Contains(out.String(), `"defects"`) {
		t.Fatalf("collector not wired: calls=%d output=%s", calls, out)
	}
	calls = 0
	registry.noteReads = 0
	out.Reset()
	if err := app.Run(context.Background(), []string{"--plans-dir", t.TempDir(), "insights", "--trend", "--json"}); err != nil {
		t.Fatal(err)
	}
	if calls != 0 || registry.noteReads != 0 || !strings.Contains(diagnostic.String(), "explicit --plans-dir") {
		t.Fatalf("explicit-store isolation: calls=%d diagnostics=%s", calls, diagnostic)
	}
}
