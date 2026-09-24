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

func TestSessionCompose(t *testing.T) {
	tests := []struct {
		name, buffer, wantError string
		unchanged, failure      bool
		text                    string
		tags                    []string
	}{
		{name: "text and tags", buffer: "tags:\ntier1\nbackend\n---\nnew text\n", text: "new text\n", tags: []string{"tier1", "backend"}},
		{name: "text only", buffer: "tags:\n---\nhello", text: "hello"},
		{name: "maximum text", buffer: "tags:\n---\n" + strings.Repeat("x", note.MaxText), text: strings.Repeat("x", note.MaxText)},
		{name: "unchanged blank", unchanged: true},
		{name: "whitespace", buffer: "tags:\n---\n \t\n"},
		{name: "tags only", buffer: "tags:\ntier1\n---\n \n"},
		{name: "missing tags", buffer: "---\n", wantError: "must retain"},
		{name: "missing separator", buffer: "tags:\n", wantError: "must retain"},
		{name: "oversized file", buffer: strings.Repeat(" ", maxEditorFileBytes+1), wantError: "buffer exceeds"},
		{name: "oversized text", buffer: "tags:\n---\n" + strings.Repeat("x", note.MaxText+1), wantError: "text exceeds"},
		{name: "oversized blank body", buffer: "tags:\n---\n" + strings.Repeat(" ", note.MaxText+1), wantError: "text exceeds"},
		{name: "editor failure", failure: true, wantError: "editor failed"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			session := Session{TempDir: dir, Runner: func(_ context.Context, _ string, args []string, _ io.Reader, _, _ io.Writer) error {
				path := args[len(args)-1]
				content, err := os.ReadFile(path) //nolint:gosec // G304: path is the session-owned test buffer.
				if err != nil {
					return err
				}
				if !strings.HasPrefix(string(content), "# Destination: project (repo-1)\n") || !strings.HasSuffix(string(content), "tags:\n---\n") {
					t.Fatalf("initial buffer = %q", content)
				}
				if test.failure {
					return errors.New("editor failed")
				}
				if test.unchanged {
					return nil
				}
				return os.WriteFile(path, []byte(test.buffer), 0o600) //nolint:gosec // G703: path is the session-owned test buffer.
			}}
			text, tags, ready, err := session.Compose(context.Background(), "project (repo-1)")
			if test.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantError) {
					t.Fatalf("error = %v, want %q", err, test.wantError)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if text != test.text || !slices.Equal(tags, test.tags) || ready != (test.text != "") {
				t.Fatalf("Compose = %q %v %t", text, tags, ready)
			}
			entries, err := os.ReadDir(dir)
			if err != nil || len(entries) != 0 {
				t.Fatalf("buffer cleanup: %v, %v", entries, err)
			}
		})
	}
}

func TestSessionComposeSanitizesDestination(t *testing.T) {
	session := Session{TempDir: t.TempDir(), Runner: func(_ context.Context, _ string, args []string, _ io.Reader, _, _ io.Writer) error {
		content, err := os.ReadFile(args[len(args)-1]) //nolint:gosec // G703: path is the session-owned test buffer.
		if err != nil {
			return err
		}
		header, rest, _ := strings.Cut(string(content), "\n")
		if strings.ContainsAny(header, "\x1b\r\u202e") || len([]rune(header)) > 272 || !strings.HasPrefix(rest, "# Tao note editor\n") {
			t.Fatalf("unsafe destination header: %q", content)
		}
		return nil
	}}
	text, tags, ready, err := session.Compose(context.Background(), "evil\x1b\r\u202e\ntags:\n---\ninjected"+strings.Repeat("x", 500))
	if err != nil || ready || text != "" || len(tags) != 0 {
		t.Fatalf("header became content: %q %v %t %v", text, tags, ready, err)
	}
}

func TestSessionComposeEditorSelectionAndStreams(t *testing.T) {
	for _, test := range []struct {
		name, env, want string
		command, args   []string
	}{
		{name: "explicit", env: "ignored", command: []string{"editor", "--wait"}, want: "editor", args: []string{"--wait"}},
		{name: "environment", env: "custom --wait", want: "custom", args: []string{"--wait"}},
		{name: "fallback", env: " \t", want: "nvim"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("EDITOR", test.env)
			input := strings.NewReader("input")
			var output, stderr strings.Builder
			session := Session{TempDir: t.TempDir(), Command: test.command, Input: input, Output: &output, Error: &stderr,
				Runner: func(_ context.Context, name string, args []string, in io.Reader, out, errOut io.Writer) error {
					if name != test.want || !slices.Equal(args[:len(args)-1], test.args) || in != input || out != &output || errOut != &stderr {
						t.Fatalf("invocation = %q %v, streams = %v %v %v", name, args, in, out, errOut)
					}
					return nil
				}}
			if _, _, _, err := session.Compose(context.Background(), "repo"); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestSessionComposeCancellationCleansBuffer(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	dir := t.TempDir()
	session := Session{TempDir: dir, Runner: func(context.Context, string, []string, io.Reader, io.Writer, io.Writer) error {
		cancel()
		return nil
	}}
	_, _, ready, err := session.Compose(ctx, "repo")
	if ready || !errors.Is(err, context.Canceled) {
		t.Fatalf("ready=%t err=%v", ready, err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("buffer cleanup: %v, %v", entries, err)
	}
}

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
		{name: "oversized text", runner: func(_ context.Context, _ string, args []string, _ io.Reader, _, _ io.Writer) error {
			return os.WriteFile(args[len(args)-1], []byte("tags:\n---\n"+strings.Repeat("x", note.MaxText+1)), 0o600) //nolint:gosec // G703: path is the session-owned test buffer.
		}, want: "text exceeds"},
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
