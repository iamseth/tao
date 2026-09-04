// Package noteeditor runs a bounded external-editor session for note text and tags.
package noteeditor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"slices"
	"strings"

	"github.com/iamseth/tao/internal/note"
)

const (
	maxEditorFileBytes = note.MaxText + 64*1024
	tagsHeader         = "tags:"
	bodySeparator      = "---"
)

// Runner executes an interactive editor attached to the supplied streams.
type Runner func(context.Context, string, []string, io.Reader, io.Writer, io.Writer) error

// Session owns one temporary note-editing buffer and external editor process.
type Session struct {
	Command []string
	Runner  Runner
	Input   io.Reader
	Output  io.Writer
	Error   io.Writer
	TempDir string
}

// Edit opens current in an external editor and returns validated buffer fields.
// A byte-for-byte equivalent text and equivalent tag list are reported unchanged.
func (s Session) Edit(ctx context.Context, current note.Note) (text string, tags []string, changed bool, err error) {
	command := s.Command
	if len(command) == 0 {
		command = strings.Fields(os.Getenv("EDITOR"))
	}
	if len(command) == 0 {
		command = []string{"nvim"}
	}
	if strings.TrimSpace(command[0]) == "" {
		return "", nil, false, errors.New("note editor command is empty")
	}
	runner := s.Runner
	if runner == nil {
		runner = DefaultRunner
	}
	file, err := os.CreateTemp(s.TempDir, "tao-note-*.md")
	if err != nil {
		return "", nil, false, fmt.Errorf("create note editor buffer: %w", err)
	}
	path := file.Name()
	defer func() { _ = os.Remove(path) }()
	if _, err := io.WriteString(file, formatBuffer(current)); err != nil {
		_ = file.Close()
		return "", nil, false, fmt.Errorf("write note editor buffer: %w", err)
	}
	if err := file.Close(); err != nil {
		return "", nil, false, fmt.Errorf("close note editor buffer: %w", err)
	}

	args := append(append([]string(nil), command[1:]...), path)
	if err := runner(ctx, command[0], args, s.Input, s.Output, s.Error); err != nil {
		return "", nil, false, fmt.Errorf("run note editor: %w", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", nil, false, fmt.Errorf("inspect note editor buffer: %w", err)
	}
	if info.Size() > maxEditorFileBytes {
		return "", nil, false, fmt.Errorf("note editor buffer exceeds %d bytes", maxEditorFileBytes)
	}
	content, err := os.ReadFile(path) // #nosec G304 -- path was allocated above and never exposed except to the editor.
	if err != nil {
		return "", nil, false, fmt.Errorf("read note editor buffer: %w", err)
	}
	text, tags, err = parseBuffer(string(content))
	if err != nil {
		return "", nil, false, err
	}
	return text, tags, text != current.Text || !slices.Equal(tags, current.Tags), nil
}

// DefaultRunner runs an interactive editor in the foreground.
func DefaultRunner(ctx context.Context, name string, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	cmd := exec.CommandContext(ctx, name, args...) // #nosec G204,G702 -- command is an explicit user-owned editor selection.
	cmd.Stdin = stdin
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	return cmd.Run()
}

func formatBuffer(current note.Note) string {
	var buffer strings.Builder
	buffer.WriteString("# Tao note editor\n")
	buffer.WriteString("# Edit tags below (one per line) and note text after ---.\n")
	buffer.WriteString(tagsHeader + "\n")
	for _, tag := range current.Tags {
		buffer.WriteString(tag)
		buffer.WriteByte('\n')
	}
	buffer.WriteString(bodySeparator + "\n")
	buffer.WriteString(current.Text)
	return buffer.String()
}

func parseBuffer(content string) (string, []string, error) {
	lines := strings.Split(content, "\n")
	tagsLine := -1
	separatorLine := -1
	for index, line := range lines {
		switch {
		case tagsLine < 0 && strings.TrimSpace(line) == tagsHeader:
			tagsLine = index
		case tagsLine >= 0 && strings.TrimSpace(line) == bodySeparator:
			separatorLine = index
		}
		if separatorLine >= 0 {
			break
		}
	}
	if tagsLine < 0 || separatorLine < 0 {
		return "", nil, errors.New("note editor buffer must retain the tags: and --- lines")
	}
	tags := make([]string, 0, separatorLine-tagsLine-1)
	for _, line := range lines[tagsLine+1 : separatorLine] {
		if tag := strings.TrimSpace(line); tag != "" {
			tags = append(tags, tag)
		}
	}
	text := strings.Join(lines[separatorLine+1:], "\n")
	if strings.TrimSpace(text) == "" {
		return "", nil, errors.New("note text is blank")
	}
	if len([]byte(text)) > note.MaxText {
		return "", nil, fmt.Errorf("note text exceeds %d bytes", note.MaxText)
	}
	return text, tags, nil
}
