package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/iamseth/tao/internal/agentinput"
	"github.com/iamseth/tao/internal/commandrunner"
	"github.com/iamseth/tao/internal/gitops"
	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/internal/run"
)

func TestSliceBlockedGateEvidence(t *testing.T) {
	for _, mode := range []string{"owned", "outside path", "empty root", "missing root", "file root", "empty base", "bad base", "not git", "fingerprint error", "both groups"} {
		t.Run(mode, func(t *testing.T) {
			root := initTestGitRepo(t)
			runCLICommitGit(t, root, "config", "user.name", "Test")
			runCLICommitGit(t, root, "config", "user.email", "test@example.com")
			runCLICommitGit(t, root, "commit", "--allow-empty", "-m", "base")
			base := runCLICommitGit(t, root, "rev-parse", "HEAD")
			if err := os.WriteFile(filepath.Join(root, "owned.go"), []byte("owned\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			runCLICommitGit(t, root, "add", ".")
			runCLICommitGit(t, root, "commit", "-m", "plan work")
			fixture := newStartedSliceBlockedFixture(t)
			detail := resolveSliceBlockedDetail(t, fixture.dir)
			detail.State.Repo.BaseCommit = base
			detail.Slices.Slices[0].ExecutionRoot = root
			verified := mode == "owned" || mode == "outside path" || mode == "both groups"
			switch mode {
			case "empty root":
				detail.Slices.Slices[0].ExecutionRoot = ""
			case "missing root":
				detail.Slices.Slices[0].ExecutionRoot = filepath.Join(root, "missing")
			case "file root":
				detail.Slices.Slices[0].ExecutionRoot = filepath.Join(root, "owned.go")
			case "empty base":
				detail.State.Repo.BaseCommit = ""
			case "bad base":
				detail.State.Repo.BaseCommit = "nonexistent-base"
			case "not git":
				detail.Slices.Slices[0].ExecutionRoot = t.TempDir()
			}
			for name, value := range map[string]any{"state.json": detail.State, "slices.json": detail.Slices} {
				data, err := json.Marshal(value)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(fixture.dir, name), data, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			args := []string{"slice-blocked", "--plan-dir", fixture.dir, "--slice-id", "001-a", "--reason-file", writeSliceBlockedReason(t, "lint failed"), "--gate-command", " go test ./... ", "--failing-path", ` .\owned.go `}
			wantPaths := []string{"owned.go"}
			if mode == "outside path" {
				args = append(args, "--failing-path", "outside.go")
				wantPaths = []string{"outside.go", "owned.go"}
			}
			if mode == "both groups" {
				args = append(args, "--invalid-command", "go test ./missing", "--invalid-reason", "missing package")
			}
			var out, warnings bytes.Buffer
			app := App{Out: &out, Err: &warnings}
			if mode == "fingerprint error" {
				app.CommandRunner = func(ctx context.Context, cwd, name string, args []string, stdout, stderr io.Writer) error {
					if strings.HasSuffix(strings.Join(args, " "), "diff HEAD") {
						return errors.New("diff failed")
					}
					return commandrunner.DefaultLocal(ctx, cwd, name, args, stdout, stderr)
				}
			}
			if err := app.Run(context.Background(), args); err != nil {
				t.Fatal(err)
			}
			detail = resolveSliceBlockedDetail(t, fixture.dir)
			event := requireSliceBlockedEvent(t, detail.Events, "001-a")
			if event.Command != "go test ./..." || !reflect.DeepEqual(event.Paths, wantPaths) {
				t.Fatalf("gate evidence = %#v", event)
			}
			wantOwned := verified && mode != "outside path"
			if (event.BlockerClassification == plan.BlockerClassificationPlanOwned) != wantOwned {
				t.Fatalf("classification = %q, want owned %t", event.BlockerClassification, wantOwned)
			}
			if verified {
				head, parts, err := gitops.NewClient(root, nil).WorktreeFingerprintParts(context.Background())
				if err != nil {
					t.Fatal(err)
				}
				if event.HeadSHA != head || event.Fingerprint != plan.WorktreeFingerprint(append([]string{head}, parts...)...) || warnings.Len() != 0 {
					t.Fatalf("fingerprint evidence = %#v, warnings = %q", event, warnings.String())
				}
			} else if event.HeadSHA != "" || event.Fingerprint != "" || !strings.Contains(warnings.String(), "ownership could not be verified") {
				t.Fatalf("unverified evidence = %#v, warnings = %q", event, warnings.String())
			}
			if mode == "both groups" && countSliceBlockedEvents(detail.Events, plan.EventTypeVerificationCommandInvalid, "001-a") != 1 {
				t.Fatal("invalid-command evidence missing")
			}
		})
	}
}

func TestSliceBlockedNonASCIIPathRefusesUnchangedContinue(t *testing.T) {
	clearTaoEnv(t)
	ctx := context.Background()
	root := initTestGitRepo(t)
	runCLICommitGit(t, root, "config", "user.name", "Test")
	runCLICommitGit(t, root, "config", "user.email", "test@example.com")
	runCLICommitGit(t, root, "config", "core.quotePath", "true")
	runCLICommitGit(t, root, "commit", "--allow-empty", "-m", "base")
	base := strings.TrimSpace(runCLICommitGit(t, root, "rev-parse", "HEAD"))
	repoRoot := root
	root = filepath.Join(t.TempDir(), "worktree")
	const branch = "feature/non-ascii"
	runCLICommitGit(t, repoRoot, "worktree", "add", "-b", branch, root)
	const path = "café.go"
	if err := os.WriteFile(filepath.Join(root, path), []byte("package cafe\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runCLICommitGit(t, root, "add", ".")
	runCLICommitGit(t, root, "commit", "-m", "plan work")
	fixture := newStartedSliceBlockedFixture(t)
	detail := resolveSliceBlockedDetail(t, fixture.dir)
	head := strings.TrimSpace(runCLICommitGit(t, root, "rev-parse", "HEAD"))
	detail.State.Repo.Root, detail.State.Repo.BaseCommit = repoRoot, base
	detail.State.Workspace = &plan.Workspace{
		Strategy: plan.WorkspaceStrategyWorktree, Root: filepath.Dir(root), Path: root,
		Branch: branch, HeadSHA: head, LifecycleStatus: plan.WorkspaceStatusReady,
	}
	detail.Slices.Slices[0].ExecutionRoot = root
	detail.Slices.Slices[0].ExecutionStart = &plan.SliceExecutionStart{
		Branch: branch, Head: head, CommitPolicy: "slice", WorkspaceStrategy: plan.WorkspaceStrategyWorktree,
	}
	for name, value := range map[string]any{"state.json": detail.State, "slices.json": detail.Slices} {
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(fixture.dir, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	var out bytes.Buffer
	app := App{Out: &out, Err: &out, ProcessStarter: func(context.Context, string, string, []string) (run.Process, error) {
		t.Error("unchanged blocker must not start an agent")
		return nil, errors.New("unexpected agent handoff")
	}}
	if err := app.Run(ctx, []string{"slice-blocked", "--plan-dir", fixture.dir, "--slice-id", "001-a", "--reason-file", writeSliceBlockedReason(t, "lint failed"), "--gate-command", "go test ./...", "--failing-path", path}); err != nil {
		t.Fatal(err)
	}
	blocked := resolveSliceBlockedDetail(t, fixture.dir)
	event := requireSliceBlockedEvent(t, blocked.Events, "001-a")
	if event.BlockerClassification != plan.BlockerClassificationPlanOwned || !reflect.DeepEqual(event.Paths, []string{path}) || event.Fingerprint == "" {
		t.Errorf("non-ASCII ownership evidence = %#v", event)
	}
	err := app.run(ctx, plan.NewFileRepository(fixture.root), []string{"--continue", "--execution-mode", "isolated", "--commit-policy", "slice", "--no-review", fixture.id})
	if !errors.Is(err, run.ErrCannotStart) || !strings.Contains(err.Error(), "blocker unchanged") || !strings.Contains(err.Error(), "fix required in "+path) {
		t.Fatalf("expected unchanged non-ASCII blocker refusal, got %v", err)
	}
	after := resolveSliceBlockedDetail(t, fixture.dir)
	if !reflect.DeepEqual(blocked.State, after.State) || !reflect.DeepEqual(blocked.Slices, after.Slices) || !reflect.DeepEqual(blocked.Events, after.Events) {
		t.Fatal("unchanged blocker refusal mutated plan")
	}
}

func TestSliceBlockedGateEvidenceValidation(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{"gate alone", []string{"--gate-command", "lint"}, "--gate-command and --failing-path are required together"},
		{"path alone", []string{"--failing-path", "a.go"}, "--gate-command and --failing-path are required together"},
		{"empty gate", []string{"--gate-command", " ", "--failing-path", "a.go"}, "--gate-command and --failing-path are required together"},
		{"empty path", []string{"--gate-command", "lint", "--failing-path", " "}, "failing path must not be empty"},
		{"gate bound", []string{"--gate-command", strings.Repeat("x", agentinput.MaxTextRunes+1), "--failing-path", "a.go"}, "gate command exceeds"},
		{"path bound", []string{"--gate-command", "lint", "--failing-path", strings.Repeat("x", agentinput.MaxTextRunes+1)}, "failing path exceeds"},
	}
	many := []string{"--gate-command", "lint"}
	for i := range 65 {
		many = append(many, "--failing-path", fmt.Sprintf("file%d.go", i))
	}
	tests = append(tests, struct {
		name string
		args []string
		want string
	}{"path count", many, "at most 64 failing paths"})
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixture := newStartedSliceBlockedFixture(t)
			before := resolveSliceBlockedDetail(t, fixture.dir)
			args := append([]string{"slice-blocked", "--plan-dir", fixture.dir, "--slice-id", "001-a", "--reason-file", writeSliceBlockedReason(t, "blocked")}, tt.args...)
			app := App{Out: &bytes.Buffer{}, Err: &bytes.Buffer{}}
			if err := app.Run(context.Background(), args); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want %q", err, tt.want)
			}
			after := resolveSliceBlockedDetail(t, fixture.dir)
			if !reflect.DeepEqual(before.State, after.State) || !reflect.DeepEqual(before.Slices, after.Slices) || !reflect.DeepEqual(before.Events, after.Events) {
				t.Fatal("invalid gate evidence mutated plan")
			}
		})
	}
}

func TestSliceBlockedGateEvidenceAcceptsPathLimit(t *testing.T) {
	fs := flag.NewFlagSet("slice-blocked", flag.ContinueOnError)
	registerSliceBlockedFlags(fs)
	args := []string{"--gate-command", " lint "}
	for i := range 64 {
		args = append(args, "--failing-path", fmt.Sprintf(" file%d.go ", i))
	}
	if err := fs.Parse(args); err != nil {
		t.Fatal(err)
	}
	evidence, err := parseSliceBlockedGateEvidence(fs)
	if err != nil {
		t.Fatal(err)
	}
	if evidence.GateCommand != "lint" || len(evidence.FailingPaths) != 64 || evidence.FailingPaths[63] != "file63.go" {
		t.Fatalf("parsed evidence = %#v", evidence)
	}
}

func TestSliceBlockedRejectedNoteNotPublished(t *testing.T) {
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("TAO_DATA_HOME", home)
	fixture := newStartedSliceBlockedFixture(t)
	file := writeSliceBlockedReason(t, "private advisory")
	app := App{Out: &bytes.Buffer{}, Err: &bytes.Buffer{}}
	if err := app.Run(context.Background(), []string{"slice-blocked", "--plan-dir", fixture.dir, "--slice-id", "missing", "--reason-file", file, "--resume-note-file", file}); err == nil {
		t.Fatal("invalid block accepted")
	}
	if countSliceBlockedEvents(resolveSliceBlockedDetail(t, fixture.dir).Events, plan.EventTypeSliceBlocked, "missing") != 0 {
		t.Fatal("rejected block recorded an event")
	}
	if _, err := os.Stat(filepath.Join(home, "run-resume")); !os.IsNotExist(err) {
		t.Fatalf("rejected block created cache: %v", err)
	}
}

func TestSliceBlockedResumeNote(t *testing.T) {
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("TAO_DATA_HOME", home)
	fixture := newRunPlanFixture(t, plan.StatusPlanned, []string{"001-a"}, nil, "001-a", plan.StatusPending)
	record, err := plan.NewPlanRecord(fixture.dir, resolveSliceBlockedDetail(t, fixture.dir))
	if err != nil {
		t.Fatal(err)
	}
	if err := record.StartSlice("001-a", plan.SliceStartRequest{ExecutionRoot: t.TempDir(), StartedAt: time.Date(2026, 7, 19, 16, 0, 0, 0, time.UTC)}); err != nil {
		t.Fatal(err)
	}
	reason := writeSliceBlockedReason(t, "waiting")
	note := writeSliceBlockedReason(t, "last action: inspect; next action: fix; why: failing; do-not: commit")
	var warnings bytes.Buffer
	app := App{Out: &bytes.Buffer{}, Err: &warnings, Now: func() time.Time { return time.Date(2026, 7, 19, 17, 0, 0, 0, time.UTC) }}
	args := []string{"slice-blocked", "--plan-dir", fixture.dir, "--slice-id", "001-a", "--reason-file", reason}
	if err := app.Run(context.Background(), append(append([]string{}, args...), "--resume-note-file", note)); err != nil {
		t.Fatal(err)
	}
	store := run.ResumeNoteStore{}
	before := resolveSliceBlockedDetail(t, fixture.dir)
	if text, err := store.Load(before, "001-a"); err != nil || !strings.Contains(text, "last action") {
		t.Fatalf("note=%q err=%v warnings=%s", text, err, &warnings)
	}
	// Make cache cleanup unavailable; freshness must still suppress the old
	// envelope after permissions are repaired, even with an identical clock.
	if err := os.Chmod(filepath.Join(home, "run-resume"), 0o755); err != nil { //nolint:gosec // Deliberately unsafe directory permissions exercise failed cleanup.
		t.Fatal(err)
	}
	for _, path := range []string{"", filepath.Join(t.TempDir(), "missing")} {
		call := append([]string{}, args...)
		if path != "" {
			call = append(call, "--resume-note-file", path)
		}
		if err := app.Run(context.Background(), call); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(filepath.Join(home, "run-resume"), 0o700); err != nil { //nolint:gosec // Restore private directory traversal permissions.
			t.Fatal(err)
		}
		detail := resolveSliceBlockedDetail(t, fixture.dir)
		if text, _ := store.Load(detail, "001-a"); text != "" {
			t.Fatalf("stale note: %q", text)
		}
		if countSliceBlockedEvents(detail.Events, plan.EventTypeSliceBlocked, "001-a") != 1 {
			t.Fatal("repeat added block event")
		}
	}
	if !strings.Contains(warnings.String(), "resume note unavailable") {
		t.Fatalf("missing warning: %s", &warnings)
	}
}

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

func TestSliceBlockedCommandRejectsAbandonedPlanWithoutMutation(t *testing.T) {
	fixture := newRunPlanFixture(t, plan.StatusPlanned, []string{"001-a"}, nil, "001-a", plan.StatusPending)
	detail := resolveSliceBlockedDetail(t, fixture.dir)
	record, err := plan.NewPlanRecord(fixture.dir, detail)
	if err != nil {
		t.Fatal(err)
	}
	if err := record.Abandon("superseded by safer work", time.Date(2026, 7, 19, 16, 5, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	before := resolveSliceBlockedDetail(t, fixture.dir)
	reasonFile := writeSliceBlockedReason(t, "later blocker must not revive work")
	var out bytes.Buffer
	app := App{Out: &out, Err: &out}

	err = app.Run(context.Background(), []string{"slice-blocked", "--plan-dir", fixture.dir, "--slice-id", "001-a", "--reason-file", reasonFile})
	if err == nil || !strings.Contains(err.Error(), "plan "+fixture.id+" is abandoned: superseded by safer work") {
		t.Fatalf("slice-blocked error = %v", err)
	}
	after := resolveSliceBlockedDetail(t, fixture.dir)
	if !reflect.DeepEqual(after.State, before.State) || !reflect.DeepEqual(after.Slices, before.Slices) || !reflect.DeepEqual(after.Events, before.Events) {
		t.Fatalf("slice-blocked changed abandoned artifacts:\n got: %#v\nwant: %#v", after, before)
	}
	if out.Len() != 0 {
		t.Fatalf("slice-blocked emitted success output: %q", out.String())
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
		{name: "reason file bytes", reason: "x", writeBytes: int(agentinput.MaxFileBytes) + 1, want: "reason file exceeds 65536 byte limit"},
		{name: "reason runes", reason: strings.Repeat("x", agentinput.MaxTextRunes+1), want: "blocker reason exceeds 16384 rune limit"},
		{name: "invalid command", reason: "blocked", evidence: []string{"--invalid-command", strings.Repeat("x", agentinput.MaxTextRunes+1), "--invalid-reason", "missing package"}, want: "invalid command exceeds 16384 rune limit"},
		{name: "invalid reason", reason: "blocked", evidence: []string{"--invalid-command", "go test ./missing", "--invalid-reason", strings.Repeat("x", agentinput.MaxTextRunes+1)}, want: "invalid reason exceeds 16384 rune limit"},
		{name: "corrected command", reason: "blocked", evidence: []string{"--invalid-command", "go test ./missing", "--invalid-reason", "missing package", "--corrected-command", strings.Repeat("x", agentinput.MaxTextRunes+1)}, want: "corrected command exceeds 16384 rune limit"},
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
		"--gate-command[failing verification gate command]",
		"*--failing-path[repository-relative failing path (repeatable)]",
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
	if err := record.StartSlice("001-a", plan.SliceStartRequest{StartedAt: time.Date(2026, 7, 19, 16, 0, 0, 0, time.UTC)}); err != nil {
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
