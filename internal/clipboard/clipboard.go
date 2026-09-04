// Package clipboard copies bounded text to the operating system clipboard.
package clipboard

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"runtime"
	"strings"
)

// Runner executes one clipboard helper with text supplied on standard input.
type Runner func(context.Context, string, []string, io.Reader, io.Writer, io.Writer) error

// Session resolves and invokes an operating-system clipboard helper.
type Session struct {
	Commands [][]string
	Runner   Runner
	Error    io.Writer
}

// Copy places text on the clipboard without adding a trailing newline.
func (session Session) Copy(ctx context.Context, text string) error {
	if text == "" {
		return errors.New("clipboard text is blank")
	}
	commands := session.Commands
	if len(commands) == 0 {
		commands = defaultCommands()
	}
	if len(commands) == 0 {
		return fmt.Errorf("clipboard helpers are unsupported on %s", runtime.GOOS)
	}
	runner := session.Runner
	if runner == nil {
		runner = DefaultRunner
	}
	var failures []error
	for _, command := range commands {
		if len(command) == 0 || strings.TrimSpace(command[0]) == "" {
			continue
		}
		err := runner(ctx, command[0], command[1:], strings.NewReader(text), io.Discard, session.Error)
		if err == nil {
			return nil
		}
		failures = append(failures, fmt.Errorf("%s: %w", command[0], err))
	}
	if len(failures) == 0 {
		return errors.New("no clipboard helper is configured")
	}
	return fmt.Errorf("copy to clipboard: %w", errors.Join(failures...))
}

// DefaultRunner executes one clipboard helper process.
func DefaultRunner(ctx context.Context, name string, args []string, input io.Reader, output, errorOutput io.Writer) error {
	cmd := exec.CommandContext(ctx, name, args...) // #nosec G204,G702 -- names come from the fixed platform helper list.
	cmd.Stdin = input
	cmd.Stdout = output
	cmd.Stderr = errorOutput
	return cmd.Run()
}

func defaultCommands() [][]string {
	switch runtime.GOOS {
	case "darwin":
		return [][]string{{"pbcopy"}}
	case "linux":
		return [][]string{{"wl-copy"}, {"xclip", "-selection", "clipboard"}, {"xsel", "--clipboard", "--input"}}
	case "windows":
		return [][]string{{"clip.exe"}}
	default:
		return nil
	}
}
