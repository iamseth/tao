package cli

import (
	"context"
	"io"
	"testing"

	"github.com/iamseth/tao/internal/clipboard"
)

func TestUIClipboardCopiesExactNoteID(t *testing.T) {
	var copied string
	service := &uiClipboard{session: clipboard.Session{
		Commands: [][]string{{"copy"}},
		Runner: func(_ context.Context, _ string, _ []string, input io.Reader, _, _ io.Writer) error {
			content, err := io.ReadAll(input)
			copied = string(content)
			return err
		},
	}}
	if err := service.Copy(context.Background(), "note-123"); err != nil {
		t.Fatal(err)
	}
	if copied != "note-123" {
		t.Fatalf("copied value=%q", copied)
	}
}
