package cli

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"
)

func TestCapturePlanningSessionCommandReportsRemovedSupport(t *testing.T) {
	var out, errOut bytes.Buffer
	app := App{Out: &out, Err: &errOut, CommandRunner: func(ctx context.Context, cwd string, name string, args []string, stdout io.Writer, stderr io.Writer) error {
		t.Fatalf("capture-planning-session should not invoke external commands, got %s %v", name, args)
		return nil
	}}

	err := app.Run(context.Background(), []string{"capture-planning-session", "--plan-dir", t.TempDir(), "--session-id", "legacy", "--planning-started-at", "not-a-time", "--raw"})
	if err == nil || !strings.Contains(err.Error(), planningSessionCaptureRemovedMessage) {
		t.Fatalf("expected removed capture error, got %v", err)
	}
	if out.Len() != 0 || errOut.Len() != 0 {
		t.Fatalf("expected no capture output, stdout=%q stderr=%q", out.String(), errOut.String())
	}
}

func TestCapturePlanningSessionCommandRequiresPlanDir(t *testing.T) {
	app := App{Out: io.Discard, Err: io.Discard}

	err := app.Run(context.Background(), []string{"capture-planning-session"})
	if err == nil || !strings.Contains(err.Error(), "--plan-dir is required") {
		t.Fatalf("expected required plan-dir error, got %v", err)
	}
}
