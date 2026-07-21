package plan

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
)

func TestLogHelpersReadTailAndFollowExistingContent(t *testing.T) {
	planDir := t.TempDir()
	log, err := OpenLogAppend(planDir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := log.WriteString("one\ntwo\nthree\n"); err != nil {
		t.Fatal(err)
	}
	if err := log.Close(); err != nil {
		t.Fatal(err)
	}

	content, err := ReadLog(planDir)
	if err != nil {
		t.Fatal(err)
	}
	if content != "one\ntwo\nthree\n" {
		t.Fatalf("unexpected log content %q", content)
	}
	tail, err := ReadLogTail(planDir, 2)
	if err != nil {
		t.Fatal(err)
	}
	if tail != "two\nthree\n" {
		t.Fatalf("unexpected log tail %q", tail)
	}
	all, err := ReadLogTail(planDir, 0)
	if err != nil || all != content {
		t.Fatalf("expected full tail, got %q, %v", all, err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var out bytes.Buffer
	err = FollowLog(ctx, planDir, &out)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context canceled from FollowLog, got %v", err)
	}
	if out.String() != content {
		t.Fatalf("FollowLog copied %q, want %q", out.String(), content)
	}
}

func TestReadLogTailHandlesMissingLogAndLastLinesEdges(t *testing.T) {
	missing, err := ReadLogTail(t.TempDir(), 10)
	if err != nil || missing != "" {
		t.Fatalf("missing log tail = %q, %v; want empty", missing, err)
	}
	if got := lastLines("", 2); got != "" {
		t.Fatalf("empty lastLines = %q", got)
	}
	if got := lastLines("a\nb\n", 0); got != "a\nb\n" {
		t.Fatalf("nonpositive lastLines = %q", got)
	}
	if got := lastLines("a\nb", 5); got != "a\nb" {
		t.Fatalf("short lastLines = %q", got)
	}
	if got := lastLines("a\nb\nc", 1); !strings.HasSuffix(got, "c\n") {
		t.Fatalf("expected trailing newline in truncated tail, got %q", got)
	}
}
