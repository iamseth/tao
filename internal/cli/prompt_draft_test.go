package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDraftPromptCommandSavesStdinToLocalDraft(t *testing.T) {
	t.Chdir(t.TempDir())
	var out bytes.Buffer
	app := App{In: strings.NewReader("/plan make the web feel great\n"), Out: &out, Err: &out}

	if err := app.Run(context.Background(), []string{"draft-prompt", "Web Foundation Plan"}); err != nil {
		t.Fatalf("draft-prompt failed: %v", err)
	}

	path := filepath.Join(".tao", "prompt-drafts", "web-foundation-plan.md")
	content, err := os.ReadFile(path) //nolint:gosec // G304: test-controlled draft path
	if err != nil {
		t.Fatalf("expected prompt draft file: %v", err)
	}
	if got := string(content); got != "/plan make the web feel great\n" {
		t.Fatalf("unexpected prompt draft content %q", got)
	}
	body := out.String()
	for _, want := range []string{"saved " + path, "pi @" + path} {
		if !strings.Contains(body, want) {
			t.Fatalf("expected output to include %q, got %q", want, body)
		}
	}
}

func TestDraftPromptCommandReadsFromFileAndRefusesOverwrite(t *testing.T) {
	t.Chdir(t.TempDir())
	source := filepath.Join(t.TempDir(), "prompt.md")
	if err := os.WriteFile(source, []byte("/plan from file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	app := App{Out: &out, Err: &out}

	if err := app.Run(context.Background(), []string{"dr", "file prompt", "--from", source}); err != nil {
		t.Fatalf("draft-prompt failed: %v", err)
	}
	if err := app.Run(context.Background(), []string{"dr", "file prompt", "--from", source}); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("expected overwrite error, got %v", err)
	}
}

func TestPromptDraftFilenameSanitizesName(t *testing.T) {
	got, err := promptDraftFilename(" Web UI: Plan A.md ")
	if err != nil {
		t.Fatalf("promptDraftFilename failed: %v", err)
	}
	if got != "web-ui-plan-a.md" {
		t.Fatalf("promptDraftFilename = %q", got)
	}
	if _, err := promptDraftFilename("../bad"); err == nil {
		t.Fatal("expected path separator error")
	}
}
