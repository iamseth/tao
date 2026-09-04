package clipboard

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestCopyUsesExactTextAndFallsBackBetweenHelpers(t *testing.T) {
	var calls []string
	session := Session{
		Commands: [][]string{{"missing"}, {"copy", "--clipboard"}},
		Runner: func(_ context.Context, name string, args []string, input io.Reader, _, _ io.Writer) error {
			content, err := io.ReadAll(input)
			if err != nil {
				return err
			}
			calls = append(calls, name+" "+strings.Join(args, " ")+"="+string(content))
			if name == "missing" {
				return errors.New("not found")
			}
			return nil
		},
	}
	if err := session.Copy(context.Background(), "note-123"); err != nil {
		t.Fatal(err)
	}
	want := []string{"missing =note-123", "copy --clipboard=note-123"}
	if strings.Join(calls, "|") != strings.Join(want, "|") {
		t.Fatalf("clipboard calls=%v, want %v", calls, want)
	}
}

func TestCopyRejectsBlankAndReportsHelperFailures(t *testing.T) {
	if err := (Session{}).Copy(context.Background(), ""); err == nil {
		t.Fatal("blank clipboard copy succeeded")
	}
	session := Session{
		Commands: [][]string{{"copy"}},
		Runner: func(context.Context, string, []string, io.Reader, io.Writer, io.Writer) error {
			return errors.New("failed")
		},
	}
	if err := session.Copy(context.Background(), "note"); err == nil || !strings.Contains(err.Error(), "copy: failed") {
		t.Fatalf("helper failure error=%v", err)
	}
}
