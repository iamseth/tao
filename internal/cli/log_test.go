package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/iamseth/tao/internal/agent/logrecord"
	mergepkg "github.com/iamseth/tao/internal/merge"
	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/internal/promptcapture"
	"github.com/iamseth/tao/internal/taodata"
)

func writeLogPrompt(t *testing.T, dir, body string) promptcapture.Written {
	t.Helper()
	written, err := promptcapture.Write(promptcapture.Target{Dir: dir, Role: "implementation", Template: "run"}, promptcapture.Meta{Model: "test-model", StartedAt: time.Date(2026, 10, 3, 1, 2, 3, 0, time.UTC)}, body)
	if err != nil {
		t.Fatal(err)
	}
	return written
}

func TestLogCapturedPrompts(t *testing.T) {
	fixture := newRunPlanFixture(t, plan.StatusPlanned, []string{"001-a"}, nil, "001-a", plan.StatusPending)
	var out bytes.Buffer
	run := func(args ...string) error {
		out.Reset()
		return (App{Out: &out, Err: &out}).Run(context.Background(), append([]string{"--plans-dir", fixture.root, "log", fixture.id}, args...))
	}
	if err := run("--prompts"); err != nil || out.String() != "No captured prompts for plan "+fixture.id+"\n" {
		t.Fatalf("empty: %q, %v", out.String(), err)
	}
	body := "role: body-key\n\nexact body without trailing newline"
	first := writeLogPrompt(t, promptcapture.Dir(fixture.dir), body)
	writeLogPrompt(t, promptcapture.Dir(fixture.dir), body)
	if err := run("--prompts"); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"1", "2", "2026-10-03T01:02:03Z", "implementation", "run", "test-model", first.Hash[:12]} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("listing missing %q: %s", want, out.String())
		}
	}
	for _, selector := range []string{"1", filepath.Base(first.Path)} {
		if err := run("--prompt", selector); err != nil || out.String() != body {
			t.Fatalf("read %q: %q, %v", selector, out.String(), err)
		}
	}
	for _, args := range [][]string{{"--prompt", "0"}, {"--prompt", "3"}, {"--prompt", "../escape"}, {"--prompt", `..\escape`}, {"--prompts", "--follow"}, {"--prompt", "1", "-f"}} {
		if err := run(args...); err == nil || !strings.Contains(err.Error(), "usage:") {
			t.Fatalf("%v: %v", args, err)
		}
	}
}

func TestLogCapturedPromptTruncated(t *testing.T) {
	dir := t.TempDir()
	body := strings.Repeat("x", promptcapture.MaxPromptBytes+1)
	written := writeLogPrompt(t, dir, body)
	var out bytes.Buffer
	if err := renderCapturedPrompts(&out, dir, "plan", "test", ""); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "524289 (truncated)") || !strings.Contains(out.String(), written.Hash[:12]) {
		t.Fatalf("truncated listing: %s", out.String())
	}
	out.Reset()
	if err := renderCapturedPrompts(&out, dir, "plan", "test", "1"); err != nil {
		t.Fatal(err)
	}
	if out.String() != body[:promptcapture.MaxPromptBytes] {
		t.Fatal("truncated body mismatch")
	}
}

func TestLogBatchCapturedPrompts(t *testing.T) {
	registry := logBatchRegistry{&fakeNoteRegistry{dir: t.TempDir(), current: taodata.Repo{ID: "repo"}}}
	store := mergepkg.NewBatchStore(registry.MergeBatchesDir(registry.current), registry.ActiveMergeBatchPath(registry.current))
	written := writeLogPrompt(t, store.PromptDir("batch-a"), "batch prompt")
	var out bytes.Buffer
	app := App{Out: &out, Err: &out, Registry: func() NoteRegistry { return registry }}
	if err := app.log(context.Background(), fakeRepository{}, []string{"--batch", "batch-a", "--prompts"}); err != nil || !strings.Contains(out.String(), written.Hash[:12]) {
		t.Fatalf("batch listing: %q, %v", out.String(), err)
	}
	out.Reset()
	if err := app.log(context.Background(), fakeRepository{}, []string{"--batch", "batch-a", "--prompt", "1"}); err != nil || out.String() != "batch prompt" {
		t.Fatalf("batch body: %q, %v", out.String(), err)
	}
}

func TestLogRendersFramedAgentRunLog(t *testing.T) {
	fixture := newRunPlanFixture(t, plan.StatusPlanned, []string{"001-a"}, nil, "001-a", plan.StatusPending)
	var stored bytes.Buffer
	if err := logrecord.Write(&stored, logrecord.Record{Type: logrecord.TypeAssistant, Content: "agent output"}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(plan.LogPath(fixture.dir), stored.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	err := App{Out: &out, Err: &out}.Run(context.Background(), []string{"--plans-dir", fixture.root, "log", "20260430-1200"})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := out.String(), "assistant: agent output\n"; got != want {
		t.Fatalf("unexpected log output %q", got)
	}
	if strings.Contains(out.String(), logrecord.Prefix) {
		t.Fatalf("log exposed framing: %q", out.String())
	}
}

func TestLogPreservesLegacyUnframedOutput(t *testing.T) {
	fixture := newRunPlanFixture(t, plan.StatusPlanned, []string{"001-a"}, nil, "001-a", plan.StatusPending)
	want := "\x1b[32mlegacy agent output\x1b[0m\ntrailing output"
	if err := os.WriteFile(plan.LogPath(fixture.dir), []byte(want), 0o600); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if err := (App{Out: &out, Err: &out}).Run(context.Background(), []string{"--plans-dir", fixture.root, "log", fixture.id}); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); got != want {
		t.Fatalf("unexpected legacy log output %q", got)
	}
}

func TestLogFollowsAppendedOutput(t *testing.T) {
	fixture := newRunPlanFixture(t, plan.StatusPlanned, []string{"001-a"}, nil, "001-a", plan.StatusPending)
	logPath := plan.LogPath(fixture.dir)
	var initial bytes.Buffer
	if err := logrecord.Write(&initial, logrecord.Record{Type: logrecord.TypeAssistant, Content: "initial"}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(logPath, initial.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var framed bytes.Buffer
	if err := logrecord.Write(&framed, logrecord.Record{Type: logrecord.TypeToolResult, Name: "test", Content: "appended"}); err != nil {
		t.Fatal(err)
	}
	appended := framed.String()
	wantAppended := "✓ test\nappended\n"
	out := newNotifyingBuffer(wantAppended)
	var errOut bytes.Buffer
	app := App{Out: out, Err: &errOut}
	errCh := make(chan error, 1)
	go func() {
		errCh <- app.Run(ctx, []string{"--plans-dir", fixture.root, "log", fixture.id, "-f"})
	}()

	time.Sleep(50 * time.Millisecond)
	logFile, err := os.OpenFile(logPath, os.O_APPEND|os.O_WRONLY, 0o600) //nolint:gosec // G304: test-controlled log path
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(logFile, appended); err != nil {
		_ = logFile.Close()
		t.Fatal(err)
	}
	if err := logFile.Close(); err != nil {
		t.Fatal(err)
	}

	select {
	case <-out.done:
	case <-time.After(2 * time.Second):
		cancel()
		t.Fatalf("expected appended output, got %q", out.String())
	}
	cancel()
	if err := <-errCh; !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context cancellation, got %v", err)
	}
	if got, want := out.String(), "assistant: initial\n"+wantAppended; !strings.Contains(got, want) {
		t.Fatalf("unexpected followed output %q", got)
	}
	if strings.Contains(out.String(), logrecord.Prefix) {
		t.Fatalf("followed log exposed framing: %q", out.String())
	}
}

type logBatchRegistry struct {
	*fakeNoteRegistry
}

func (r logBatchRegistry) MergeBatchesDir(repo taodata.Repo) string {
	return filepath.Join(r.dir, repo.ID, "merge-batches")
}

func (r logBatchRegistry) ActiveMergeBatchPath(repo taodata.Repo) string {
	return filepath.Join(r.MergeBatchesDir(repo), "active.json")
}

func TestLogBatch(t *testing.T) {
	registry := logBatchRegistry{&fakeNoteRegistry{dir: t.TempDir(), current: taodata.Repo{ID: "repo"}}}
	store := mergepkg.NewBatchStore(registry.MergeBatchesDir(registry.current), registry.ActiveMergeBatchPath(registry.current))
	var out bytes.Buffer
	app := App{Out: &out, Err: &out, Registry: func() NoteRegistry { return registry }}
	ctx := context.Background()
	if err := app.log(ctx, fakeRepository{}, []string{"--batch"}); err == nil || !strings.Contains(err.Error(), "no active merge batch") {
		t.Fatalf("missing active batch error = %v", err)
	}
	state, err := store.Initialize(mergepkg.BatchState{ID: "batch-a", Status: mergepkg.BatchStatusCompleted}, "2026-09-26T21:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	want := mergepkg.FormatBatchTransitionLine(mergepkg.BatchTransition{At: "2026-09-26T21:00:00Z", Sequence: 1, To: state.Status, State: state}) + "\n"
	for _, args := range [][]string{{"--batch"}, {"--batch", "batch-a"}, {"--batch", "--follow"}, {"--batch", "-f", "batch-a"}} {
		out.Reset()
		ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		err := app.Run(ctx, append([]string{"--plans-dir", t.TempDir(), "log"}, args...))
		cancel()
		if err != nil || out.String() != want {
			t.Fatalf("log %v = %q, %v; want %q", args, out.String(), err, want)
		}
	}
	if err := store.ClearActive(state.ID); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := app.log(ctx, fakeRepository{}, []string{"--batch", state.ID}); err != nil || out.String() != want {
		t.Fatalf("named historical batch = %q, %v", out.String(), err)
	}
	if err := app.log(ctx, fakeRepository{}, []string{"--batch", "missing"}); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing named batch error = %v", err)
	}
	if err := app.log(ctx, fakeRepository{}, []string{"--batch", "one", "two"}); err == nil || !strings.Contains(err.Error(), "usage:") {
		t.Fatalf("extra arguments error = %v", err)
	}
}

func TestLogBatchUsage(t *testing.T) {
	var out bytes.Buffer
	if err := (App{Out: &out, Err: &out}).Run(context.Background(), []string{"log", "--help"}); err != nil {
		t.Fatal(err)
	}
	for _, usage := range []string{"log (lo) [--follow | --prompts | --prompt VALUE] <plan-id-or-slug>", "log (lo) --batch [--follow | --prompts | --prompt VALUE] [batch-id]"} {
		if !strings.Contains(out.String(), usage) {
			t.Fatalf("help missing %q: %s", usage, out.String())
		}
	}
}

func TestLogUsageRepoAndMissingLogErrors(t *testing.T) {
	var out bytes.Buffer
	app := App{Out: &out, Err: &out}
	if err := app.log(context.Background(), fakeRepository{}, nil); err == nil {
		t.Fatal("expected log usage error")
	}
	err := app.log(context.Background(), fakeRepository{err: errors.New("nope")}, []string{"x"})
	if err == nil || !strings.Contains(err.Error(), "nope") {
		t.Fatalf("expected repo error, got %v", err)
	}

	root := t.TempDir()
	planID := "20260430-1200-run-plan"
	writeRunPlan(t, root, planID, plan.StatusPlanned, []string{"001-a"}, nil, "001-a", plan.StatusPending)
	err = app.log(context.Background(), plan.NewFileRepository(root), []string{planID})
	if err == nil || !strings.Contains(err.Error(), "agent log not found") {
		t.Fatalf("expected missing log error, got %v", err)
	}
}
