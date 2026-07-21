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

func TestSliceBlockedCommandBlocksCurrentSlice(t *testing.T) {
	fixture := newStartedSliceBlockedFixture(t)
	reasonFile := writeSliceBlockedReason(t, "  dependency service is unavailable  ")
	blockedAt := time.Date(2026, 7, 19, 16, 10, 0, 0, time.UTC)
	var out bytes.Buffer
	app := App{Out: &out, Err: &out, Now: func() time.Time { return blockedAt }}

	if err := app.Run(context.Background(), []string{"slice-blocked", "--plan-dir", fixture.dir, "--slice-id", "001-a", "--reason-file", reasonFile}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Slice blocked: 001-a") {
		t.Fatalf("command output = %q", out.String())
	}

	detail := resolveSliceBlockedDetail(t, fixture.dir)
	if detail.State.Status != plan.StatusBlocked || detail.State.Plan.CurrentSlice == nil || *detail.State.Plan.CurrentSlice != "001-a" {
		t.Fatalf("blocked plan state = %#v", detail.State)
	}
	if len(detail.State.Plan.PendingSlices) == 0 || detail.State.Plan.PendingSlices[0] != "001-a" {
		t.Fatalf("pending slices = %v, want selected slice retained", detail.State.Plan.PendingSlices)
	}
	slice := detail.Slices.Slices[0]
	if slice.Status != plan.StatusBlocked || slice.BlockerNote != "dependency service is unavailable" {
		t.Fatalf("blocked slice = %#v", slice)
	}
	event := requireSliceBlockedEvent(t, detail.Events, "001-a")
	if event.Reason != slice.BlockerNote || event.Timestamp != blockedAt {
		t.Fatalf("slice_blocked event = %#v", event)
	}
}

func TestSliceBlockedCommandRepeatIsIdempotent(t *testing.T) {
	fixture := newStartedSliceBlockedFixture(t)
	reasonFile := writeSliceBlockedReason(t, "invalid verification setup")
	blockedAt := time.Date(2026, 7, 19, 16, 15, 0, 0, time.UTC)
	app := App{Out: &bytes.Buffer{}, Err: &bytes.Buffer{}, Now: func() time.Time { return blockedAt }}
	args := []string{
		"slice-blocked", "--plan-dir", fixture.dir, "--slice-id", "001-a", "--reason-file", reasonFile,
		"--invalid-command", "go test ./missing", "--invalid-reason", "package path does not exist", "--corrected-command", "go test ./internal/cli",
	}

	if err := app.Run(context.Background(), args); err != nil {
		t.Fatal(err)
	}
	if err := app.Run(context.Background(), args); err != nil {
		t.Fatal(err)
	}

	detail := resolveSliceBlockedDetail(t, fixture.dir)
	if got := countSliceBlockedEvents(detail.Events, plan.EventTypeSliceBlocked, "001-a"); got != 1 {
		t.Fatalf("slice_blocked event count = %d, want 1", got)
	}
	if got := countSliceBlockedEvents(detail.Events, plan.EventTypeVerificationCommandInvalid, "001-a"); got != 1 {
		t.Fatalf("verification_command_invalid event count = %d, want 1", got)
	}
}

func TestSliceBlockedCommandEmitsVerificationCommandInvalid(t *testing.T) {
	fixture := newStartedSliceBlockedFixture(t)
	reasonFile := writeSliceBlockedReason(t, "verification command cannot load tests")
	blockedAt := time.Date(2026, 7, 19, 16, 20, 0, 0, time.UTC)
	app := App{Out: &bytes.Buffer{}, Err: &bytes.Buffer{}, Now: func() time.Time { return blockedAt }}

	if err := app.Run(context.Background(), []string{
		"slice-blocked", "--plan-dir", fixture.dir, "--slice-id", "001-a", "--reason-file", reasonFile,
		"--invalid-command", " go test ./internal/missing ",
		"--invalid-reason", " package directory is missing ",
		"--corrected-command", " go test ./internal/plan ",
	}); err != nil {
		t.Fatal(err)
	}

	detail := resolveSliceBlockedDetail(t, fixture.dir)
	var invalidEvent *plan.Event
	for i := range detail.Events {
		if detail.Events[i].Type == plan.EventTypeVerificationCommandInvalid && detail.Events[i].SliceID == "001-a" {
			invalidEvent = &detail.Events[i]
			break
		}
	}
	if invalidEvent == nil {
		t.Fatal("verification_command_invalid event not found")
	}
	if invalidEvent.PlanID != fixture.id || invalidEvent.Timestamp != blockedAt || invalidEvent.Command != "go test ./internal/missing" || invalidEvent.Reason != "package directory is missing" || invalidEvent.CorrectedCommand != "go test ./internal/plan" {
		t.Fatalf("verification_command_invalid event = %#v", invalidEvent)
	}
}

func TestSliceBlockedCommandRejectsOversizedInputs(t *testing.T) {
	tests := []struct {
		name       string
		reason     string
		evidence   []string
		want       string
		writeBytes int
	}{
		{name: "reason file bytes", reason: "x", writeBytes: int(sliceAgentInputMaxFileBytes) + 1, want: "reason file exceeds 65536 byte limit"},
		{name: "reason runes", reason: strings.Repeat("x", sliceAgentInputMaxTextRunes+1), want: "blocker reason exceeds 16384 rune limit"},
		{name: "invalid command", reason: "blocked", evidence: []string{"--invalid-command", strings.Repeat("x", sliceAgentInputMaxTextRunes+1), "--invalid-reason", "missing package"}, want: "invalid command exceeds 16384 rune limit"},
		{name: "invalid reason", reason: "blocked", evidence: []string{"--invalid-command", "go test ./missing", "--invalid-reason", strings.Repeat("x", sliceAgentInputMaxTextRunes+1)}, want: "invalid reason exceeds 16384 rune limit"},
		{name: "corrected command", reason: "blocked", evidence: []string{"--invalid-command", "go test ./missing", "--invalid-reason", "missing package", "--corrected-command", strings.Repeat("x", sliceAgentInputMaxTextRunes+1)}, want: "corrected command exceeds 16384 rune limit"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixture := newStartedSliceBlockedFixture(t)
			reason := tt.reason
			if tt.writeBytes > 0 {
				reason = strings.Repeat("x", tt.writeBytes)
			}
			reasonFile := writeSliceBlockedReason(t, reason)
			args := []string{"slice-blocked", "--plan-dir", fixture.dir, "--slice-id", "001-a", "--reason-file", reasonFile}
			args = append(args, tt.evidence...)
			app := App{Out: &bytes.Buffer{}, Err: &bytes.Buffer{}}

			err := app.Run(context.Background(), args)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("command error = %v, want text %q", err, tt.want)
			}
			detail := resolveSliceBlockedDetail(t, fixture.dir)
			if detail.State.Status != plan.StatusInProgress || detail.Slices.Slices[0].Status != plan.StatusInProgress {
				t.Fatalf("oversized input mutated plan: state=%q slice=%q", detail.State.Status, detail.Slices.Slices[0].Status)
			}
		})
	}
}

func TestSliceBlockedCommandReportsUnknownAndCompletedSlices(t *testing.T) {
	t.Run("unknown slice", func(t *testing.T) {
		fixture := newStartedSliceBlockedFixture(t)
		reasonFile := writeSliceBlockedReason(t, "blocked")
		app := App{Out: &bytes.Buffer{}, Err: &bytes.Buffer{}}
		err := app.Run(context.Background(), []string{"slice-blocked", "--plan-dir", fixture.dir, "--slice-id", "missing", "--reason-file", reasonFile})
		if err == nil || !strings.Contains(err.Error(), "slice missing not found") {
			t.Fatalf("unknown-slice error = %v", err)
		}
	})

	t.Run("completed slice", func(t *testing.T) {
		fixture := newRunPlanFixture(t, plan.StatusInReview, nil, []string{"001-a"}, "001-a", plan.StatusCompleted)
		reasonFile := writeSliceBlockedReason(t, "blocked")
		app := App{Out: &bytes.Buffer{}, Err: &bytes.Buffer{}}
		err := app.Run(context.Background(), []string{"slice-blocked", "--plan-dir", fixture.dir, "--slice-id", "001-a", "--reason-file", reasonFile})
		if err == nil || !strings.Contains(err.Error(), "slice 001-a is completed and cannot be blocked") {
			t.Fatalf("completed-slice error = %v", err)
		}
	})
}

func TestSliceBlockedCommandHelpAndMetadataDerivedCompletion(t *testing.T) {
	var out bytes.Buffer
	app := App{Out: &out, Err: &out}
	if err := app.Run(context.Background(), []string{"help"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), topLevelHelpRow(t, "slice-blocked")) {
		t.Fatalf("top-level help does not contain slice-blocked: %q", out.String())
	}

	out.Reset()
	if err := app.completion([]string{"zsh"}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"slice-blocked:Block a slice after an exceptional stop",
		"--reason-file[file containing the blocker reason]",
		"--corrected-command[mechanically equivalent corrected verification command]",
	} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("zsh completion missing %q", want)
		}
	}
}

func newStartedSliceBlockedFixture(t *testing.T) runPlanFixture {
	t.Helper()
	fixture := newRunPlanFixture(t, plan.StatusInProgress, []string{"001-a"}, nil, "001-a", plan.StatusPending)
	detail := resolveSliceBlockedDetail(t, fixture.dir)
	record, err := plan.NewPlanRecord(fixture.dir, detail)
	if err != nil {
		t.Fatal(err)
	}
	if err := record.StartSlice("001-a", time.Date(2026, 7, 19, 16, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	return fixture
}

func writeSliceBlockedReason(t *testing.T, reason string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "reason.txt")
	if err := os.WriteFile(path, []byte(reason), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func resolveSliceBlockedDetail(t *testing.T, planDir string) *plan.PlanDetail {
	t.Helper()
	detail, err := plan.NewFileRepository(filepath.Dir(planDir)).ResolvePlan(context.Background(), planDir)
	if err != nil {
		t.Fatal(err)
	}
	return detail
}

func requireSliceBlockedEvent(t *testing.T, events []plan.Event, sliceID string) plan.Event {
	t.Helper()
	for _, event := range events {
		if event.Type == plan.EventTypeSliceBlocked && event.SliceID == sliceID {
			return event
		}
	}
	t.Fatalf("slice_blocked event for %s not found", sliceID)
	return plan.Event{}
}

func countSliceBlockedEvents(events []plan.Event, eventType string, sliceID string) int {
	count := 0
	for _, event := range events {
		if event.Type == eventType && event.SliceID == sliceID {
			count++
		}
	}
	return count
}
