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
	"github.com/iamseth/tao/internal/promptcapture"
)

func TestReportIgnoresCapturedPrompts(t *testing.T) {
	fixture := newRunPlanFixture(t, plan.StatusPlanned, []string{"001-a"}, nil, "001-a", plan.StatusPending)
	for _, planningOnly := range []bool{false, true} {
		render := func() string {
			var out, errOut bytes.Buffer
			args := []string{"report", "--output", "-", fixture.id}
			if planningOnly {
				args = append(args, "--planning-only")
			}
			if err := reportTestApp(fixture.root, &out, &errOut).Run(context.Background(), args); err != nil {
				t.Fatal(err)
			}
			return out.String()
		}
		before := render()
		writeLogPrompt(t, promptcapture.Dir(fixture.dir), "PROMPT-CAPTURE-SENTINEL-7f3a")
		after := render()
		if before != after || strings.Contains(after, "PROMPT-CAPTURE-SENTINEL-7f3a") || strings.Contains(after, "prompts/") {
			t.Fatalf("report exposed capture: %s", after)
		}
	}
}

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
				mode := "full"
				if planningOnly {
					mode = "planning-only"
				}
				report := out.String()
				if !strings.HasPrefix(report, "---\nschema: tao.plan-report.v1\nmode: "+mode+"\n") ||
					!strings.Contains(report, "\nplan: Run Plan\nplan-id: ") ||
					!strings.Contains(report, "\n---\n\n# Run Plan\n") {
					t.Fatalf("stdout is not a %s report: %q", mode, report)
				}
				if errOut.Len() != 0 {
					t.Fatalf("stdout mode diagnostics = %q", errOut.String())
				}
				if planningOnly {
					if !strings.Contains(report, "\nstatus: planned\n") {
						t.Fatal("planning-only report omitted its schema-owned planned status")
					}
					for _, forbidden := range []string{"## Implementation", "## Implementation Summary", "## Review and Outcome", "### Slice", "Status:", "Kind:", "Commit:", "Verification:"} {
						if strings.Contains(report, forbidden) {
							t.Fatalf("planning-only report contains %q", forbidden)
						}
					}
					if !strings.Contains(report, "## Planned Slices\n\n- Slice 1: A") {
						t.Fatal("planning-only report omitted flat planned-slice fields")
					}
				} else if !strings.Contains(report, "## Implementation") || !strings.Contains(report, "## Implementation Summary") || !strings.Contains(report, "## Review and Outcome") {
					t.Fatal("full report omitted implementation or outcome sections")
				}
			}
		})
	}
}

func TestReportLatestVerificationAttemptDoesNotBecomeCompletionEvidence(t *testing.T) {
	fixture := newRunPlanFixture(t, plan.StatusInProgress, []string{"slice-a"}, nil, "slice-a", plan.StatusInProgress)
	record, err := plan.NewFileRepository(fixture.root).ResolvePlanRecord(context.Background(), fixture.dir)
	if err != nil {
		t.Fatal(err)
	}
	record.Detail().Slices.Slices[0].VerificationResults = []plan.VerificationRun{{Command: "historical", Result: "passed"}}
	record.Detail().Slices.Slices[0].VerificationAttempt = &plan.SliceVerificationSnapshot{
		AttemptID: "private-attempt", ExecutionRoot: "/private/root",
		Runs: []plan.VerificationRun{{Command: "latest-command", Result: "failed", Source: plan.VerificationSourceTao, Details: "password=output-secret"}},
	}
	if err := record.PersistArtifacts(); err != nil {
		t.Fatal(err)
	}
	if err := plan.AppendEvent(fixture.dir, plan.Event{Type: plan.EventTypeVerificationClaimMismatch, SliceID: "slice-a", Command: "claim-command", ClaimedResult: "passed", Result: "failed", Message: "password=claim-secret"}); err != nil {
		t.Fatal(err)
	}
	for _, planningOnly := range []bool{false, true} {
		var out, errOut bytes.Buffer
		args := []string{"report", "--output", "-", fixture.id}
		if planningOnly {
			args = append(args, "--planning-only")
		}
		if err := reportTestApp(fixture.root, &out, &errOut).Run(context.Background(), args); err != nil {
			t.Fatal(err)
		}
		for _, forbidden := range []string{"private-attempt", "/private/root", "latest-command", "claim-command", "output-secret", "claim-secret"} {
			if strings.Contains(out.String(), forbidden) {
				t.Fatalf("report leaked %q", forbidden)
			}
		}
		if planningOnly {
			if strings.Contains(out.String(), "**Verification**") {
				t.Fatal("planning report includes execution evidence")
			}
		} else if !strings.Contains(out.String(), "1/1 passed") {
			t.Fatalf("latest attempt inflated completed verification totals: %s", out.String())
		}
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
	if got, _ := os.ReadFile(output); !strings.HasPrefix(string(got), "---\nschema: tao.plan-report.v1\nmode: planning-only\n") { //nolint:gosec // test-owned temporary path
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
