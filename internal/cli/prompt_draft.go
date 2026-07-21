package cli

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode"
)

var draftPromptCommand = commandMetadata{
	name:                  "draft-prompt",
	minPrefix:             "dr",
	usageLines:            []string{"draft-prompt (dr) <name> [--from FILE] [--force]"},
	completionDescription: "Save a reusable planning prompt draft",
	long:                  "Save prompt text as a local reusable planning draft under .tao/prompt-drafts. Drafts are workspace-local, can be read from stdin or a file, and are intended for starting fresh agent planning sessions.",
	examples: "  tao draft-prompt web-foundation < prompt.md\n" +
		"  tao draft-prompt web-foundation --from prompt.md\n" +
		"  tao draft-prompt web-foundation --from prompt.md --force",
	registerFlags: registerDraftPromptFlags,
	completion: completionContext{
		flagValues:    map[string]completionFlagValue{"from": {kind: completionValuePath, label: "path"}},
		argumentSpecs: []string{":draft name:"},
	},
	execute: func(c commandContext) error {
		return c.app.draftPrompt(c.args)
	},
}

func registerDraftPromptFlags(fs *flag.FlagSet) {
	fs.String("from", "", "read prompt text from file instead of stdin")
	fs.Bool("force", false, "overwrite an existing draft")
}

func (a App) draftPrompt(args []string) error {
	fs, positional, err := a.parseArgs("draft-prompt", args, registerDraftPromptFlags)
	if err != nil {
		return err
	}
	if err := requirePositionals(positional, 1, "usage: tao draft-prompt <name> [--from FILE] [--force]"); err != nil {
		return err
	}
	filename, err := promptDraftFilename(positional[0])
	if err != nil {
		return err
	}
	content, err := readPromptDraftContent(a.input(), flagStringValue(fs, "from"))
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(content)) == "" {
		return errors.New("prompt draft content is empty")
	}
	dir := filepath.Join(".tao", "prompt-drafts")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create prompt draft directory: %w", err)
	}
	path := filepath.Join(dir, filename)
	flags := os.O_WRONLY | os.O_CREATE
	if flagBoolValue(fs, "force") {
		flags |= os.O_TRUNC
	} else {
		flags |= os.O_EXCL
	}
	file, err := os.OpenFile(path, flags, 0o600) //nolint:gosec // G304: draft path derived from internal plan data
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return fmt.Errorf("prompt draft already exists: %s (use --force to overwrite)", path)
		}
		return fmt.Errorf("create prompt draft: %w", err)
	}
	if _, err := file.Write(content); err != nil {
		_ = file.Close()
		return fmt.Errorf("write prompt draft: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close prompt draft: %w", err)
	}
	if err := writef(a.Out, "saved %s\n", path); err != nil {
		return err
	}
	return writef(a.Out, "start Pi planning: pi @%s\n", path)
}

func readPromptDraftContent(in io.Reader, from string) ([]byte, error) {
	if strings.TrimSpace(from) == "" {
		content, err := io.ReadAll(in)
		if err != nil {
			return nil, fmt.Errorf("read prompt draft from stdin: %w", err)
		}
		return content, nil
	}
	content, err := os.ReadFile(from) // #nosec G304 -- explicit local file input selected by the caller.
	if err != nil {
		return nil, fmt.Errorf("read prompt draft file: %w", err)
	}
	return content, nil
}

func promptDraftFilename(name string) (string, error) {
	base := strings.TrimSpace(name)
	if base == "" {
		return "", errors.New("prompt draft name is empty")
	}
	if strings.ContainsAny(base, `/\\`) {
		return "", errors.New("prompt draft name must not contain path separators")
	}
	base = strings.TrimSuffix(base, ".md")
	var b strings.Builder
	lastDash := false
	for _, r := range strings.ToLower(base) {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
			lastDash = false
		case r == '-' || r == '_' || unicode.IsSpace(r):
			if b.Len() > 0 && !lastDash {
				b.WriteByte('-')
				lastDash = true
			}
		}
	}
	slug := strings.Trim(b.String(), "-")
	if slug == "" {
		return "", errors.New("prompt draft name must include at least one letter or digit")
	}
	return slug + ".md", nil
}
