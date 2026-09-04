package noteeditor

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/iamseth/tao/internal/note"
)

func TestSessionEditsTextAndTagsThroughBoundedBuffer(t *testing.T) {
	var gotName string
	var gotArgs []string
	session := Session{
		Command: []string{"nvim", "--clean"},
		TempDir: t.TempDir(),
		Runner: func(_ context.Context, name string, args []string, _ io.Reader, _, _ io.Writer) error {
			gotName, gotArgs = name, append([]string(nil), args...)
			path := args[len(args)-1]
			content, err := os.ReadFile(path) //nolint:gosec // path is the session-owned test buffer.
			if err != nil {
				return err
			}
			if !strings.Contains(string(content), "tags:\none\ntwo\n---\nold text") {
				t.Fatalf("initial editor buffer = %q", content)
			}
			return os.WriteFile(path, []byte("# instructions\ntags:\ntier0\nbackend\n---\nnew text\nwith detail\n"), 0o600) //nolint:gosec // G703: path is the session-owned test buffer.
		},
	}
	text, tags, changed, err := session.Edit(context.Background(), note.Note{Text: "old text", Tags: []string{"one", "two"}})
	if err != nil {
		t.Fatal(err)
	}
	if gotName != "nvim" || len(gotArgs) != 2 || gotArgs[0] != "--clean" || filepath.Ext(gotArgs[1]) != ".md" {
		t.Fatalf("editor invocation name=%q args=%v", gotName, gotArgs)
	}
	if text != "new text\nwith detail\n" || !slices.Equal(tags, []string{"tier0", "backend"}) || !changed {
		t.Fatalf("edited text=%q tags=%v changed=%t", text, tags, changed)
	}
	if _, err := os.Stat(gotArgs[1]); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("editor buffer was not removed: %v", err)
	}
}

func TestSessionHonorsEditorEnvironment(t *testing.T) {
	t.Setenv("EDITOR", "custom-editor --wait")
	var gotName string
	var gotArgs []string
	session := Session{TempDir: t.TempDir(), Runner: func(_ context.Context, name string, args []string, _ io.Reader, _, _ io.Writer) error {
		gotName, gotArgs = name, append([]string(nil), args...)
		return nil
	}}
	if _, _, _, err := session.Edit(context.Background(), note.Note{Text: "same"}); err != nil {
		t.Fatal(err)
	}
	if gotName != "custom-editor" || len(gotArgs) != 2 || gotArgs[0] != "--wait" {
		t.Fatalf("EDITOR invocation name=%q args=%v", gotName, gotArgs)
	}
}

func TestSessionLeavesUnchangedBufferUnmodified(t *testing.T) {
	current := note.Note{Text: "same", Tags: []string{"one"}}
	session := Session{TempDir: t.TempDir(), Runner: func(context.Context, string, []string, io.Reader, io.Writer, io.Writer) error { return nil }}
	text, tags, changed, err := session.Edit(context.Background(), current)
	if err != nil || changed || text != current.Text || !slices.Equal(tags, current.Tags) {
		t.Fatalf("unchanged edit text=%q tags=%v changed=%t err=%v", text, tags, changed, err)
	}
}

func TestSessionRejectsMalformedOrFailedEditorWithoutReturningChanges(t *testing.T) {
	failure := errors.New("editor failed")
	tests := []struct {
		name   string
		runner Runner
		want   string
	}{
		{name: "process failure", runner: func(context.Context, string, []string, io.Reader, io.Writer, io.Writer) error { return failure }, want: "editor failed"},
		{name: "missing metadata", runner: func(_ context.Context, _ string, args []string, _ io.Reader, _, _ io.Writer) error {
			return os.WriteFile(args[len(args)-1], []byte("plain text"), 0o600) //nolint:gosec // G703: path is the session-owned test buffer.
		}, want: "must retain"},
		{name: "blank text", runner: func(_ context.Context, _ string, args []string, _ io.Reader, _, _ io.Writer) error {
			return os.WriteFile(args[len(args)-1], []byte("tags:\none\n---\n"), 0o600) //nolint:gosec // G703: path is the session-owned test buffer.
		}, want: "blank"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			session := Session{TempDir: t.TempDir(), Runner: test.runner}
			_, _, changed, err := session.Edit(context.Background(), note.Note{Text: "old"})
			if err == nil || !strings.Contains(err.Error(), test.want) || changed {
				t.Fatalf("changed=%t err=%v, want %q", changed, err, test.want)
			}
		})
	}
}
