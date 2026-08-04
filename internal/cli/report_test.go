package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/iamseth/tao/internal/plan"
)

func TestReportRequiresOnePlanAndExplicitOutput(t *testing.T) {
	fixture := newRunPlanFixture(t, plan.StatusPlanned, []string{"slice-a"}, nil, "slice-a", plan.StatusPending)
	app := reportTestApp(fixture.root, &bytes.Buffer{}, &bytes.Buffer{})
	for _, test := range []struct {
		args []string
		want string
	}{
		{args: []string{fixture.id}, want: "requires --output"},
		{args: []string{"--output", "-"}, want: "usage: tao report"},
		{args: []string{"--output", "-", fixture.id, "extra"}, want: "usage: tao report"},
	} {
		if err := app.Run(context.Background(), append([]string{"report"}, test.args...)); err == nil || !strings.Contains(err.Error(), test.want) {
			t.Fatalf("report %v error = %v, want containing %q", test.args, err, test.want)
		}
	}
}

func TestReportStdoutIsPureMarkdownInBothModesAndReadablePhases(t *testing.T) {
	for _, status := range []string{plan.StatusPlanned, plan.StatusInProgress, plan.StatusInReview, plan.StatusReviewed, plan.StatusChangesRequested, plan.StatusCompleted} {
		t.Run(status, func(t *testing.T) {
			fixture := newRunPlanFixture(t, status, []string{"slice-a"}, nil, "slice-a", plan.StatusPending)
			for _, planningOnly := range []bool{false, true} {
				var out, errOut bytes.Buffer
				app := reportTestApp(fixture.root, &out, &errOut)
				args := []string{"report", "--output", "-", fixture.id}
				if planningOnly {
					args = append(args, "--planning-only")
				}
				if err := app.Run(context.Background(), args); err != nil {
					t.Fatalf("Run(%v) failed: %v", args, err)
				}
				if !strings.HasPrefix(out.String(), "# Plan Report\n") || !strings.Contains(out.String(), "- Schema: tao.plan-report.v1") {
					t.Fatalf("stdout is not a report: %q", out.String())
				}
				if errOut.Len() != 0 {
					t.Fatalf("stdout mode diagnostics = %q", errOut.String())
				}
				if planningOnly {
					for _, forbidden := range []string{"## Execution Summary", "## Review and Outcome", "- Status:"} {
						if strings.Contains(out.String(), forbidden) {
							t.Fatalf("planning-only report contains %q", forbidden)
						}
					}
				} else if !strings.Contains(out.String(), "## Execution Summary") {
					t.Fatal("full report omitted execution summary")
				}
			}
		})
	}
}

func TestReportFileIsExclusiveOwnerOnlyAndForceReplaces(t *testing.T) {
	fixture := newRunPlanFixture(t, plan.StatusPlanned, []string{"slice-a"}, nil, "slice-a", plan.StatusPending)
	output := filepath.Join(fixture.root, "exports", "report.md")
	if err := os.Mkdir(filepath.Dir(output), 0o750); err != nil {
		t.Fatal(err)
	}
	var errOut bytes.Buffer
	app := reportTestApp(fixture.root, &bytes.Buffer{}, &errOut)
	args := []string{"report", "--output", output, fixture.id}
	if err := app.Run(context.Background(), args); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(output)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("output mode = %o, want 600", got)
	}
	original, err := os.ReadFile(output) //nolint:gosec // test-owned temporary path
	if err != nil {
		t.Fatal(err)
	}
	if err := app.Run(context.Background(), args); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("exclusive write error = %v", err)
	}
	if got, _ := os.ReadFile(output); !bytes.Equal(got, original) { //nolint:gosec // test-owned temporary path
		t.Fatal("exclusive failure changed existing report")
	}
	if err := os.Chmod(output, 0o644); err != nil { //nolint:gosec // intentionally tests replacement with an overly broad existing mode
		t.Fatal(err)
	}
	if err := app.Run(context.Background(), []string{"report", "--force", "--planning-only", "--output", output, fixture.id}); err != nil {
		t.Fatal(err)
	}
	info, _ = os.Stat(output)
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("forced output mode = %o, want 600", info.Mode().Perm())
	}
	if got, _ := os.ReadFile(output); !strings.Contains(string(got), "- Mode: planning-only") { //nolint:gosec // test-owned temporary path
		t.Fatalf("forced output was not replaced: %q", got)
	}
	if !strings.Contains(errOut.String(), "Report written.") {
		t.Fatalf("file status missing from stderr: %q", errOut.String())
	}
}

func TestReportRejectsPlanDirectoryAndCanonicalSymlinkDestinations(t *testing.T) {
	fixture := newRunPlanFixture(t, plan.StatusPlanned, []string{"slice-a"}, nil, "slice-a", plan.StatusPending)
	link := filepath.Join(fixture.root, "plan-link")
	if err := os.Symlink(fixture.dir, link); err != nil {
		t.Fatal(err)
	}
	app := reportTestApp(fixture.root, &bytes.Buffer{}, &bytes.Buffer{})
	for _, output := range []string{
		filepath.Join(fixture.dir, "report.md"),
		filepath.Join(link, "report.md"),
		fixture.dir,
	} {
		err := app.Run(context.Background(), []string{"report", "--output", output, fixture.id})
		if err == nil || !strings.Contains(err.Error(), "outside the selected plan directory") {
			t.Fatalf("output %q error = %v", output, err)
		}
	}
}

func TestReportWriteFailureIsNonSensitiveAndLeavesPlanArtifactsUnchanged(t *testing.T) {
	fixture := newRunPlanFixture(t, plan.StatusPlanned, []string{"slice-a"}, nil, "slice-a", plan.StatusPending)
	statePath := filepath.Join(fixture.dir, "state.json")
	before, err := os.ReadFile(statePath) //nolint:gosec // test-owned fixture path
	if err != nil {
		t.Fatal(err)
	}
	secretPath := filepath.Join(fixture.root, "missing-secret@example.com", "report.md")
	app := reportTestApp(fixture.root, &bytes.Buffer{}, &bytes.Buffer{})
	err = app.Run(context.Background(), []string{"report", "--output", secretPath, fixture.id})
	if err == nil || !strings.Contains(err.Error(), "could not install") {
		t.Fatalf("write error = %v", err)
	}
	if strings.Contains(err.Error(), "secret@example.com") || strings.Contains(err.Error(), fixture.dir) {
		t.Fatalf("write error disclosed source text or a local path: %v", err)
	}
	after, err := os.ReadFile(statePath) //nolint:gosec // test-owned fixture path
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("report generation changed plan state")
	}
}

func reportTestApp(root string, out, errOut *bytes.Buffer) App {
	return App{
		Out: out,
		Err: errOut,
		Now: func() time.Time { return time.Date(2026, 8, 4, 16, 30, 0, 0, time.UTC) },
		Repository: func(string) Repository {
			return plan.NewFileRepository(root)
		},
	}
}
